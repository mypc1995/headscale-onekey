package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NodeStat 是单个节点的监控数据。原始字段来自 `headscale nodes list -o json`，
// 线上没有采集节点的 CPU / 内存（Tailscale 客户端不上报这些数据），
// 因此这里监控的是控制面能看到的状态类指标。
// 实时网速（RxRate/TxRate）同样不在控制面：节点间流量走 WireGuard 直连或
// DERP，headscale 既没有按节点的流量计数器，CLI 和 metrics 也不暴露。
// 所以网速由节点侧采集代理把计数器写进 HSA_NODE_SPEED_FILE，
// 这里按两次采样的差值换算速率；文件不存在时 SpeedAvail=false，界面显示占位。
type NodeStat struct {
	ID uint64 `json:"id"`
	// Name 是节点的受管名（given_name）：`nodes rename` 改的就是它，也是 MagicDNS 名。
	Name string `json:"name"`
	// ClientName 是客户端上报的主机名（JSON 里的 name），客户端重连就会变，只能当参考信息。
	ClientName      string    `json:"client_name"`
	User            string    `json:"user"`
	IPs             []string  `json:"ips"`
	Online          bool      `json:"online"`
	State           string    `json:"state"` // online | offline | expired
	LastSeen        time.Time `json:"last_seen"`
	Version         string    `json:"version"`
	OS              string    `json:"os"`
	Expiry          time.Time `json:"expiry"`
	Tags            []string  `json:"tags"`
	ApprovedRoutes  int       `json:"approved_routes"`
	AvailableRoutes int       `json:"available_routes"`
	PendingRoutes   int       `json:"pending_routes"`
	Register        string    `json:"register"`

	// 实时网速（KB/s，与实时网速面板的口径一致）。SpeedAvail=false 表示没有可用数据源，前端显示占位。
	RxRate     float64 `json:"rx_rate"`
	TxRate     float64 `json:"tx_rate"`
	SpeedAvail bool    `json:"speed_avail"`

	// 以下字段只在渲染时按语言填充，不参与缓存。
	StateText     string `json:"state_text"`
	LastSeenText  string `json:"last_seen_text"`
	LastSeenAbs   string `json:"last_seen_abs"`
	ExpiryText    string `json:"expiry_text"`
	OSText        string `json:"os_text"`
	RegisterText  string `json:"register_text"`
	SpeedDownText string `json:"speed_down_text"`
	SpeedUpText   string `json:"speed_up_text"`
}

// NodeSnapshot 是节点监控的汇总快照，同时作为 JSON 输出结构。
type NodeSnapshot struct {
	Available bool       `json:"available"`
	Error     string     `json:"error,omitempty"`
	Service   string     `json:"service"`
	ServiceOK bool       `json:"service_ok"`
	Total     int        `json:"total"`
	Online    int        `json:"online"`
	Offline   int        `json:"offline"`
	Expired   int        `json:"expired"`
	Nodes     []NodeStat `json:"nodes"`
	History   []int      `json:"history"` // 每次采集的在线节点数
	Updated   string     `json:"updated"`
	// SpeedSource 表示网速数据源这次是否可用。
	// false 时各节点网速显示占位，并提示如何接入采集代理。
	SpeedSource bool `json:"speed_source"`
	// SpeedChannel 标明当前生效的数据来源：api（节点侧上报）或 file（计数器文件）。
	SpeedChannel string `json:"speed_channel,omitempty"`
	// SpeedEnabled 表示节点侧上报通道（POST /api/speed）是否已启用。
	SpeedEnabled bool `json:"speed_enabled"`
}

// NodeMonitor 周期性执行 headscale CLI，把节点状态缓存起来，
// 让页面请求不再直接等待 CLI；CLI 失败时保留上一次成功的数据并标记过期。
// 网速计数器来自节点侧代理写入的 JSON 文件，这里保存上一轮计数，
// 用差值除以间隔得到速率（与 cpuPercent 的 tick 差值思路一致）。
type NodeMonitor struct {
	hs      *HSClient
	mu      sync.RWMutex
	history []int
	snap    NodeSnapshot

	speedFile   string
	prevSpeed   map[string]speedCounter
	prevSpeedAt time.Time
	// reported 是节点侧 HTTP 上报通道的数据源；为空表示该通道未启用。
	reported *speedReport
}

// speedCounter 是某个节点一次采样读到的累计收/发字节数。
type speedCounter struct {
	rx uint64
	tx uint64
}

const (
	nodeHistoryLen   = 60
	nodeSamplePeriod = 15 * time.Second
)

func NewNodeMonitor(hs *HSClient, reported *speedReport) *NodeMonitor {
	m := &NodeMonitor{
		hs:        hs,
		speedFile: os.Getenv("HSA_NODE_SPEED_FILE"),
		prevSpeed: map[string]speedCounter{},
		reported:  reported,
	}
	m.sample()
	go func() {
		t := time.NewTicker(nodeSamplePeriod)
		defer t.Stop()
		for range t.C {
			m.sample()
		}
	}()
	return m
}

func (m *NodeMonitor) sample() {
	now := time.Now()
	snap := NodeSnapshot{
		Available: true,
		Service:   "unknown",
		Nodes:     []NodeStat{},
		Updated:   now.Format("15:04:05"),
	}
	// 服务状态按平台去问：Linux 问 systemd，Windows 查进程/计划任务。
	if out, err := svcIsActive(hsServiceName); err == nil || strings.TrimSpace(out) != "" {
		if v := strings.TrimSpace(out); v != "" {
			snap.Service = v
		}
	}
	snap.ServiceOK = snap.Service == "active"

	raw, err := m.hs.Nodes()
	if err != nil {
		snap.Available = false
		snap.Error = err.Error()
		m.mu.Lock()
		prev := m.snap
		snap.Nodes = append([]NodeStat{}, prev.Nodes...)
		snap.Total, snap.Online, snap.Offline, snap.Expired = prev.Total, prev.Online, prev.Offline, prev.Expired
		snap.SpeedSource = prev.SpeedSource
		// 通道状态与 CLI 无关：CLI 读取失败时也要保留，不能被一起抹掉，
		// 否则运维会误以为上报通道没开。
		snap.SpeedChannel = prev.SpeedChannel
		snap.SpeedEnabled = m.reported.isEnabled()
		snap.History = append([]int{}, m.history...)
		m.snap = snap
		m.mu.Unlock()
		return
	}

	for _, item := range raw {
		n := nodeStatFromMap(item, now)
		snap.Total++
		if n.Online {
			snap.Online++
		}
		if n.State == "expired" {
			snap.Expired++
		}
		snap.Nodes = append(snap.Nodes, n)
	}
	snap.Offline = snap.Total - snap.Online

	rates, channel, speedOK := m.nodeSpeedRates(snap.Nodes, now)
	for i := range snap.Nodes {
		key := strconv.FormatUint(snap.Nodes[i].ID, 10)
		if r, ok := rates[key]; ok {
			snap.Nodes[i].RxRate = r.rx
			snap.Nodes[i].TxRate = r.tx
			snap.Nodes[i].SpeedAvail = true
		}
	}
	snap.SpeedSource = speedOK
	snap.SpeedChannel = channel
	snap.SpeedEnabled = m.reported.isEnabled()

	m.mu.Lock()
	m.history = append(m.history, snap.Online)
	if len(m.history) > nodeHistoryLen {
		m.history = m.history[len(m.history)-nodeHistoryLen:]
	}
	snap.History = append([]int{}, m.history...)
	m.snap = snap
	m.mu.Unlock()
}

func (m *NodeMonitor) Snapshot() NodeSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.snap
	s.Nodes = append([]NodeStat{}, s.Nodes...)
	s.History = append([]int{}, s.History...)
	return s
}

// speedRate 是换算后的速率（KB/s，与 fmtRate 的输入口径一致）。
type speedRate struct {
	rx float64
	tx float64
}

// parseSpeedFile 解析节点侧采集代理写入的计数器文件。
// 格式是一个按节点 ID 或节点名索引的 JSON 对象：
//
//	{"12": {"rx_bytes": 1024, "tx_bytes": 2048}, "laptop": {...}}
//
// 返回的 map 同时按原样保留两种键，匹配在 nodeSpeedRates 里做。
func parseSpeedFile(content []byte) map[string]speedCounter {
	var raw map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
		Rx      uint64 `json:"rx"`
		Tx      uint64 `json:"tx"`
	}
	if json.Unmarshal(content, &raw) != nil {
		return map[string]speedCounter{}
	}
	out := map[string]speedCounter{}
	for key, v := range raw {
		rx, tx := v.RxBytes, v.TxBytes
		if rx == 0 && tx == 0 {
			rx, tx = v.Rx, v.Tx
		}
		out[key] = speedCounter{rx: rx, tx: tx}
	}
	return out
}

// nodeSpeedRates 汇总各节点的速率，数据来源有两个，节点侧上报优先：
//   - HTTP 上报：速率在上报到达时就按两次上报的差值算好了，这里直接取用；
//   - 计数器文件：按管理界面自身的采样节奏（每 15 秒）做差值换算。
//
// 返回值依次是「按节点 ID 归档的速率」「当前生效的数据来源」「数据源是否可用」。
func (m *NodeMonitor) nodeSpeedRates(nodes []NodeStat, now time.Time) (map[string]speedRate, string, bool) {
	apiRates := map[string]speedRate{}
	if m.reported != nil {
		apiRates = m.reported.rates(now)
	}
	fileRates, _ := m.fileSpeedRates(nodes, now)

	out := map[string]speedRate{}
	channel := ""
	for _, n := range nodes {
		idKey := strconv.FormatUint(n.ID, 10)
		if r, ok := lookupRate(apiRates, n); ok {
			out[idKey] = r
			channel = "api"
			continue
		}
		if r, ok := fileRates[idKey]; ok {
			out[idKey] = r
			if channel == "" {
				channel = "file"
			}
		}
	}
	if len(out) == 0 {
		return out, "", false
	}
	return out, channel, true
}

// fileSpeedRates 从计数器文件按两次采样的差值换算每个节点的速率，
// 结果按节点 ID 归档。文件不存在或内容无法解析时返回不可用。
func (m *NodeMonitor) fileSpeedRates(nodes []NodeStat, now time.Time) (map[string]speedRate, bool) {
	rates := map[string]speedRate{}
	if m.speedFile == "" {
		return rates, false
	}
	content, err := os.ReadFile(m.speedFile)
	if err != nil {
		return rates, false
	}
	counters := parseSpeedFile(content)
	if len(counters) == 0 {
		return rates, false
	}

	m.mu.Lock()
	prev, prevAt := m.prevSpeed, m.prevSpeedAt
	next := map[string]speedCounter{}
	m.prevSpeed = next
	m.prevSpeedAt = now
	m.mu.Unlock()

	dt := now.Sub(prevAt).Seconds()
	for _, n := range nodes {
		idKey := strconv.FormatUint(n.ID, 10)
		cur, ok := counters[idKey]
		if !ok && n.Name != "" {
			cur, ok = counters[n.Name]
		}
		if !ok {
			continue
		}
		next[idKey] = cur
		p, hasPrev := prev[idKey]
		if !hasPrev || dt <= 0 || cur.rx < p.rx || cur.tx < p.tx {
			rates[idKey] = speedRate{}
			continue
		}
		rates[idKey] = speedRate{
			rx: float64(cur.rx-p.rx) / dt / 1024,
			tx: float64(cur.tx-p.tx) / dt / 1024,
		}
	}
	return rates, true
}

// lookupRate 按节点 ID、节点名、tailnet IP 依次查找该节点的速率。
// 节点侧 agent 只知道自己的主机名或 tailnet IP，ID 只有服务端清楚，
// 所以三种标识都认。
func lookupRate(rates map[string]speedRate, n NodeStat) (speedRate, bool) {
	if r, ok := rates[strconv.FormatUint(n.ID, 10)]; ok {
		return r, true
	}
	if n.Name != "" {
		if r, ok := rates[n.Name]; ok {
			return r, true
		}
	}
	for _, ip := range n.IPs {
		if r, ok := rates[ip]; ok {
			return r, true
		}
	}
	return speedRate{}, false
}

// newNodesView 复制一份快照并填充本地化文案，避免修改缓存中的原始数据。
func newNodesView(s NodeSnapshot, lang string) NodeSnapshot {
	now := time.Now()
	out := s
	out.Nodes = make([]NodeStat, len(s.Nodes))
	for i, n := range s.Nodes {
		n.StateText = tr(lang, "state."+n.State)
		n.LastSeenText = relTime(n.LastSeen, lang, now)
		if n.LastSeen.IsZero() {
			n.LastSeenAbs = "—"
		} else {
			n.LastSeenAbs = n.LastSeen.Local().Format("01-02 15:04")
		}
		n.ExpiryText = expiryText(n.Expiry, lang, now)
		n.OSText = friendlyOS(n.OS)
		n.RegisterText = friendlyRegister(n.Register, lang)
		if n.SpeedAvail {
			n.SpeedDownText = fmtRate(n.RxRate)
			n.SpeedUpText = fmtRate(n.TxRate)
		} else {
			n.SpeedDownText, n.SpeedUpText = "—", "—"
		}
		if n.User == "" {
			n.User = "—"
		}
		out.Nodes[i] = n
	}
	return out
}

// nodeStatFromMap 把 headscale 的 JSON 节点对象转成监控结构。
// 字段全部做兼容取值：不同版本对 client_version / host_info 的命名略有差异。
func nodeStatFromMap(item map[string]any, now time.Time) NodeStat {
	n := NodeStat{
		// 受管名（given_name）优先：它是 `nodes rename` 改的字段，也是 MagicDNS 用的名字。
		// 老版本 headscale 没有 given_name 时回落到客户端上报名。
		Name:            firstNonEmpty(str(item["given_name"]), str(item["name"])),
		ClientName:      str(item["name"]),
		User:            str(asMap(item["user"])["name"]),
		IPs:             stringList(item["ip_addresses"]),
		Online:          str(item["online"]) == "true",
		LastSeen:        nodeTime(item["last_seen"]),
		Expiry:          nodeTime(item["expiry"]),
		Tags:            stringList(item["tags"]),
		ApprovedRoutes:  len(asSlice(item["approved_routes"])),
		AvailableRoutes: len(asSlice(item["available_routes"])),
		PendingRoutes:   len(pendingRoutes(stringList(item["available_routes"]), stringList(item["approved_routes"]))),
		Register:        str(item["register_method"]),
	}
	n.ID, _ = strconv.ParseUint(strings.Trim(fmt.Sprint(item["id"]), `"`), 10, 64)
	if n.Name == "" {
		n.Name = fmt.Sprintf("#%d", n.ID)
	}

	hi := hostInfoMap(item)
	n.Version = firstNonEmpty(
		str(item["client_version"]),
		str(hi["ipn_version"]),
		str(hi["client_version"]),
		str(hi["version"]),
	)
	n.OS = firstNonEmpty(str(hi["os"]), str(item["os"]))

	switch {
	case !n.Expiry.IsZero() && !n.Expiry.After(now):
		n.State = "expired"
	case n.Online:
		n.State = "online"
	default:
		n.State = "offline"
	}
	return n
}

func hostInfoMap(item map[string]any) map[string]any {
	if hi, ok := item["host_info"].(map[string]any); ok {
		return hi
	}
	// 部分版本把 host_info 存成 JSON 字符串。
	if s, ok := item["host_info"].(string); ok && s != "" && strings.HasPrefix(strings.TrimSpace(s), "{") {
		var out map[string]any
		if json.Unmarshal([]byte(s), &out) == nil {
			return out
		}
	}
	return map[string]any{}
}

func nodeTime(v any) time.Time {
	switch x := v.(type) {
	case map[string]any:
		sec, _ := strconv.ParseInt(strings.Trim(fmt.Sprint(x["seconds"]), `"`), 10, 64)
		if sec > 0 {
			return time.Unix(sec, 0)
		}
	case string:
		if t, err := time.Parse(time.RFC3339, x); err == nil {
			return t
		}
		if sec, err := strconv.ParseInt(x, 10, 64); err == nil && sec > 0 {
			return time.Unix(sec, 0)
		}
	case float64:
		if x > 0 {
			return time.Unix(int64(x), 0)
		}
	}
	return time.Time{}
}

func stringList(v any) []string {
	out := []string{}
	for _, item := range asSlice(v) {
		if s := str(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func friendlyOS(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return "—"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	case "darwin", "macos", "mac os x":
		return "macOS"
	case "ios":
		return "iOS"
	case "android":
		return "Android"
	default:
		return raw
	}
}

func friendlyRegister(raw, lang string) string {
	u := strings.ToUpper(raw)
	switch {
	case raw == "1":
		return tr(lang, "nodeMon.reg.authKey")
	case raw == "2":
		return tr(lang, "nodeMon.reg.cli")
	case raw == "3":
		return "OIDC"
	case strings.Contains(u, "OIDC"):
		return "OIDC"
	case strings.Contains(u, "AUTH_KEY"), strings.Contains(u, "AUTHKEY"):
		return tr(lang, "nodeMon.reg.authKey")
	case strings.Contains(u, "CLI"):
		return tr(lang, "nodeMon.reg.cli")
	case strings.TrimSpace(raw) == "":
		return "—"
	default:
		return raw
	}
}

func relTime(t time.Time, lang string, now time.Time) string {
	if t.IsZero() {
		return tr(lang, "nodeMon.never")
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < 10*time.Second:
		return tr(lang, "nodeMon.justNow")
	case d < time.Minute:
		return tr(lang, "nodeMon.secAgo", int(d.Seconds()))
	case d < time.Hour:
		return tr(lang, "nodeMon.minAgo", int(d.Minutes()))
	case d < 24*time.Hour:
		return tr(lang, "nodeMon.hourAgo", int(d.Hours()))
	default:
		return tr(lang, "nodeMon.dayAgo", int(d.Hours())/24)
	}
}

func expiryText(t time.Time, lang string, now time.Time) string {
	if t.IsZero() {
		return tr(lang, "nodeMon.noExpiry")
	}
	if !t.After(now) {
		return tr(lang, "nodeMon.expiredAt")
	}
	days := int(math.Ceil(t.Sub(now).Hours() / 24))
	return tr(lang, "nodeMon.expiresIn", days)
}
