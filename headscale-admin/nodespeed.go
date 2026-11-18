package main

// 节点网速上报通道。
//
// 控制面拿不到节点的流量计数器：Tailscale 客户端不上报主机流量，headscale 的
// CLI 与 Prometheus 指标也不暴露按节点的计数。所以节点侧自己采集累计收发
// 字节数，再回连管理界面上报，管理界面换算成速率展示（与本机 CPU 利用率的
// tick 差值同一思路）。
//
// 两条数据来源可以并存，HTTP 上报优先：
//   - HTTP 上报：节点 agent 调用 POST /api/speed，带独立上报令牌。
//     节点没有管理界面账号，也不该有；所以这里不用管理员会话，而是单独的令牌。
//   - 计数器文件：HSA_NODE_SPEED_FILE 指向的 JSON 文件，由同机旁路采集器写入。
//     适合采集器与管理界面同机部署的场景。

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// speedReportBodyLimit 单次上报请求体的最大字节数。
	speedReportBodyLimit = 64 << 10
	// speedReportLimit 单次上报允许的最大条目数。
	speedReportLimit = 512
	// speedReportTTL 上报数据超过这个时间没有更新就丢弃，
	// 避免节点下线后计数器一直挂在内存里。
	speedReportTTL = 10 * time.Minute
	// maxSpeedReportEntries 内存中最多保留多少个节点的计数器。
	// 令牌持有者若用大量不同的 key 上报，不加这个上限就能把内存撑爆。
	maxSpeedReportEntries = 4096
)

// speedPayload 是节点侧上报的单条累计计数器。
// node 是节点标识：节点 ID、节点名或 tailnet IP 都认，三种方式服务端都能匹配。
type speedPayload struct {
	Node    string `json:"node"`
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
	Token   string `json:"token,omitempty"`
}

// speedItem 是单个节点最近一次上报的状态。
type speedItem struct {
	cur     speedCounter
	curAt   time.Time
	rate    speedRate
	hasPrev bool
}

// speedReport 汇总节点侧上报的累计计数器，并在收到新上报时就地换算速率。
// 速率用「本次与上次上报的差值 ÷ 两次上报的时间差」计算，口径与本机 CPU
// 利用率的 tick 差值一致；这样不依赖管理界面的采样节奏，节点连续上报两次
// 之后速度就能显示出来。
type speedReport struct {
	mu    sync.Mutex
	items map[string]*speedItem
	// enabled 表示上报通道是否启用（配置了令牌）。未启用时接口返回 404。
	enabled bool
}

func newSpeedReport(enabled bool) *speedReport {
	return &speedReport{items: map[string]*speedItem{}, enabled: enabled}
}

// isEnabled 返回上报通道是否启用。
func (s *speedReport) isEnabled() bool {
	if s == nil {
		return false
	}
	return s.enabled
}

// put 记录一次上报并换算速率。计数器回退（节点或 agent 重启）时按 0 处理，
// 不产生负速率。返回 false 表示因为条目数达到上限而没有接收。
func (s *speedReport) put(key string, c speedCounter, now time.Time) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[key]; !exists && len(s.items) >= maxSpeedReportEntries {
		return false
	}
	it, ok := s.items[key]
	if !ok {
		s.items[key] = &speedItem{cur: c, curAt: now}
		return true
	}
	rate := speedRate{}
	if dt := now.Sub(it.curAt).Seconds(); dt > 0 && c.rx >= it.cur.rx && c.tx >= it.cur.tx {
		rate = speedRate{
			rx: float64(c.rx-it.cur.rx) / dt / 1024,
			tx: float64(c.tx-it.cur.tx) / dt / 1024,
		}
	}
	it.rate = rate
	it.cur = c
	it.curAt = now
	it.hasPrev = true
	return true
}

// rates 返回当前各节点的速率，同时清掉长时间没有更新的条目。
func (s *speedReport) rates(now time.Time) map[string]speedRate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]speedRate, len(s.items))
	for k, it := range s.items {
		if now.Sub(it.curAt) > speedReportTTL {
			delete(s.items, k)
			continue
		}
		out[k] = it.rate
	}
	return out
}

// speedReporter 负责节点网速上报接口的鉴权与写入。
type speedReporter struct {
	mu    sync.RWMutex
	token string
	data  *speedReport
}

func newSpeedReporter(token string) *speedReporter {
	token = strings.TrimSpace(token)
	return &speedReporter{token: token, data: newSpeedReport(token != "")}
}

// enabled 表示上报通道是否启用。没有配置令牌时整个接口关闭并返回 404，
// 避免它变成一个任何人都能写的匿名接口。
func (r *speedReporter) enabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.token != ""
}

// tokenValue 返回上报令牌，供管理界面下发给管理员分发给节点 agent。
func (r *speedReporter) tokenValue() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.token
}

// loadSpeedToken 读取节点网速上报令牌。
// 优先读文件（HSA_NODE_SPEED_TOKEN_FILE），其次读环境变量（HSA_NODE_SPEED_TOKEN）。
// 文件优先是为了让令牌不出现在 systemd unit 里——unit 文件里的 Environment=
// 对同机用户是可见的。
func loadSpeedToken(value, file string) string {
	if path := strings.TrimSpace(file); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if t := strings.TrimSpace(string(b)); t != "" {
				return t
			}
		}
	}
	return strings.TrimSpace(value)
}

// handle 接收节点侧上报的累计计数器。
func (r *speedReporter) handle(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !r.enabled() {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "上报通道未启用"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, speedReportBodyLimit))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "请求体读取失败"})
		return
	}
	// 令牌在请求头里时根本不用碰请求体；只有令牌可能藏在请求体里时，
	// 才做一次廉价的子串扫描再决定要不要完整解析。
	// 这样未认证的请求不会白白消耗一次 JSON 解析。
	if !r.headerTokenOK(req) && !bytes.Contains(bytes.ToLower(body), []byte("token")) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "令牌无效"})
		return
	}
	items, perr := parseSpeedPayload(body)
	// 先鉴权再校验请求体：令牌不对时统一返回 401，不泄露请求体的解析细节。
	if !r.authorized(req, items) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "令牌无效"})
		return
	}
	if perr != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": perr.Error()})
		return
	}
	// 缺节点标识的条目直接拒绝整个请求：静默丢弃会让 agent 以为自己上报成功了，
	// 而速度列一直不显示，很难排查。
	for _, it := range items {
		if strings.TrimSpace(it.Node) == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "缺少节点标识 node"})
			return
		}
	}
	now := time.Now()
	accepted := 0
	for _, it := range items {
		if r.data.put(strings.TrimSpace(it.Node), speedCounter{rx: it.RxBytes, tx: it.TxBytes}, now) {
			accepted++
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "accepted": accepted})
}

// headerTokenOK 判断请求头里带的令牌是否正确。
// 令牌可以放在 X-Node-Speed-Token 头或 Authorization: Bearer 头里。
func (r *speedReporter) headerTokenOK(req *http.Request) bool {
	r.mu.RLock()
	want := r.token
	r.mu.RUnlock()
	if want == "" {
		return false
	}
	candidates := []string{strings.TrimSpace(req.Header.Get("X-Node-Speed-Token"))}
	if auth := strings.TrimSpace(req.Header.Get("Authorization")); len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		candidates = append(candidates, strings.TrimSpace(auth[7:]))
	}
	for _, c := range candidates {
		if c != "" && subtle.ConstantTimeCompare([]byte(c), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

// authorized 校验上报令牌。令牌可以从 X-Node-Speed-Token 头、
// Authorization: Bearer 头或请求体的 token 字段提供，方便不同语言的 agent 接入。
func (r *speedReporter) authorized(req *http.Request, items []speedPayload) bool {
	if r.headerTokenOK(req) {
		return true
	}
	r.mu.RLock()
	want := r.token
	r.mu.RUnlock()
	if want == "" {
		return false
	}
	for _, it := range items {
		if v := strings.TrimSpace(it.Token); v != "" {
			if subtle.ConstantTimeCompare([]byte(v), []byte(want)) == 1 {
				return true
			}
		}
	}
	return false
}

// parseSpeedPayload 解析上报请求体：既接受单条对象，也接受数组批量上报。
func parseSpeedPayload(body []byte) ([]speedPayload, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, errors.New("请求体为空")
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []speedPayload
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, errors.New("请求体格式错误")
		}
		if len(arr) > speedReportLimit {
			return nil, fmt.Errorf("单次最多上报 %d 条", speedReportLimit)
		}
		return arr, nil
	}
	var one speedPayload
	if err := json.Unmarshal([]byte(trimmed), &one); err != nil {
		return nil, errors.New("请求体格式错误")
	}
	return []speedPayload{one}, nil
}
