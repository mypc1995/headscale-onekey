package main

import (
	"encoding/json"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NodeStat 是单个节点的监控数据。原始字段来自 `headscale nodes list -o json`，
// 线上没有采集节点的 CPU / 内存（Tailscale 客户端不上报这些数据），
// 因此这里监控的是控制面能看到的状态类指标。
type NodeStat struct {
	ID              uint64    `json:"id"`
	Name            string    `json:"name"`
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
	Register        string    `json:"register"`

	// 以下字段只在渲染时按语言填充，不参与缓存。
	StateText    string `json:"state_text"`
	LastSeenText string `json:"last_seen_text"`
	LastSeenAbs  string `json:"last_seen_abs"`
	ExpiryText   string `json:"expiry_text"`
	OSText       string `json:"os_text"`
	RegisterText string `json:"register_text"`
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
}

// NodeMonitor 周期性执行 headscale CLI，把节点状态缓存起来，
// 让页面请求不再直接等待 CLI；CLI 失败时保留上一次成功的数据并标记过期。
type NodeMonitor struct {
	hs      *HSClient
	mu      sync.RWMutex
	history []int
	snap    NodeSnapshot
}

const (
	nodeHistoryLen   = 60
	nodeSamplePeriod = 15 * time.Second
)

func NewNodeMonitor(hs *HSClient) *NodeMonitor {
	m := &NodeMonitor{hs: hs}
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
	if runtime.GOOS == "linux" {
		if out, err := systemctl("is-active", "headscale"); err == nil || strings.TrimSpace(out) != "" {
			if v := strings.TrimSpace(out); v != "" {
				snap.Service = v
			}
		}
		snap.ServiceOK = snap.Service == "active"
	}

	raw, err := m.hs.Nodes()
	if err != nil {
		snap.Available = false
		snap.Error = err.Error()
		m.mu.Lock()
		prev := m.snap
		snap.Nodes = append([]NodeStat{}, prev.Nodes...)
		snap.Total, snap.Online, snap.Offline, snap.Expired = prev.Total, prev.Online, prev.Offline, prev.Expired
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
		Name:            firstNonEmpty(str(item["name"]), str(item["given_name"])),
		User:            str(asMap(item["user"])["name"]),
		IPs:             stringList(item["ip_addresses"]),
		Online:          str(item["online"]) == "true",
		LastSeen:        nodeTime(item["last_seen"]),
		Expiry:          nodeTime(item["expiry"]),
		Tags:            stringList(item["tags"]),
		ApprovedRoutes:  len(asSlice(item["approved_routes"])),
		AvailableRoutes: len(asSlice(item["available_routes"])),
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
