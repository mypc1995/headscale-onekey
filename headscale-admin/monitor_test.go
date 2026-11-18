package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseProcStat(t *testing.T) {
	raw := "cpu  1000 50 300 6000 100 20 10 5 0 0\ncpu0 100 5 30 600 10 2 1 0 0 0\nintr 12345\n"
	total, idle, ok := parseProcStat(raw)
	if !ok {
		t.Fatal("parseProcStat returned ok=false")
	}
	if want := uint64(1000 + 50 + 300 + 6000 + 100 + 20 + 10 + 5); total != want {
		t.Fatalf("total = %d, want %d", total, want)
	}
	if want := uint64(6100); idle != want {
		t.Fatalf("idle = %d, want %d", idle, want)
	}
	if _, _, ok := parseProcStat("intr 1\n"); ok {
		t.Fatal("stat without cpu line should not parse")
	}
}

func TestCPUPercent(t *testing.T) {
	// 500 个 tick 中有 400 个空闲，使用率应为 20%。
	got := cpuPercent(7485, 6100, 7985, 6500)
	if got < 19.99 || got > 20.01 {
		t.Fatalf("cpuPercent = %v, want 20", got)
	}
	if got := cpuPercent(0, 0, 7985, 6500); got != 0 {
		t.Fatalf("first sample = %v, want 0", got)
	}
	if got := cpuPercent(7985, 6500, 7985, 6500); got != 0 {
		t.Fatalf("same counters = %v, want 0", got)
	}
}

func TestParseMeminfo(t *testing.T) {
	raw := "MemTotal:       8000000 kB\nMemFree:        1000000 kB\nMemAvailable:   3000000 kB\nBuffers:         100000 kB\nCached:          500000 kB\n"
	total, avail, ok := parseMeminfo(raw)
	if !ok {
		t.Fatal("parseMeminfo returned ok=false")
	}
	if total != 8000000*1024 {
		t.Fatalf("total = %d", total)
	}
	if avail != 3000000*1024 {
		t.Fatalf("available = %d", avail)
	}

	// 老内核没有 MemAvailable，退化到 free + buffers + cached。
	old := "MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 150 kB\n"
	total, avail, ok = parseMeminfo(old)
	if !ok || total != 1000*1024 || avail != 300*1024 {
		t.Fatalf("fallback = %d/%d ok=%v", avail, total, ok)
	}
	if _, _, ok := parseMeminfo("MemFree: 1 kB\n"); ok {
		t.Fatal("meminfo without MemTotal should not parse")
	}
}

func TestNodeStatFromMap(t *testing.T) {
	now := time.Unix(1700000000, 0)
	item := map[string]any{
		"id":               float64(7),
		"name":             "laptop",
		"online":           true,
		"ip_addresses":     []any{"100.64.0.2", "fd7a::2"},
		"last_seen":        map[string]any{"seconds": "1699999990"},
		"expiry":           map[string]any{"seconds": "1700100000"},
		"tags":             []any{"tag:dev"},
		"approved_routes":  []any{"10.0.0.0/8"},
		"available_routes": []any{"10.0.0.0/8", "192.168.0.0/16"},
		"user":             map[string]any{"name": "alice"},
		"host_info":        map[string]any{"os": "linux", "ipn_version": "1.80.0"},
		"register_method":  "REGISTER_METHOD_AUTH_KEY",
	}
	n := nodeStatFromMap(item, now)
	if n.ID != 7 || n.Name != "laptop" || n.User != "alice" {
		t.Fatalf("identity = %+v", n)
	}
	if !n.Online || n.State != "online" {
		t.Fatalf("state = %s online=%v", n.State, n.Online)
	}
	if n.Version != "1.80.0" || n.OS != "linux" {
		t.Fatalf("host info = %q/%q", n.Version, n.OS)
	}
	if len(n.IPs) != 2 || len(n.Tags) != 1 {
		t.Fatalf("ips=%v tags=%v", n.IPs, n.Tags)
	}
	if n.ApprovedRoutes != 1 || n.AvailableRoutes != 2 {
		t.Fatalf("routes = %d/%d", n.ApprovedRoutes, n.AvailableRoutes)
	}
	if !n.LastSeen.Equal(time.Unix(1699999990, 0)) {
		t.Fatalf("last seen = %v", n.LastSeen)
	}

	// 密钥到期即使在线也应标记为 expired，提示续期。
	expired := nodeStatFromMap(map[string]any{
		"id": float64(8), "online": true,
		"expiry": map[string]any{"seconds": "1699999999"},
	}, now)
	if expired.State != "expired" {
		t.Fatalf("expired state = %s", expired.State)
	}

	// host_info 为 JSON 字符串时也能解析。
	strInfo := nodeStatFromMap(map[string]any{
		"id":        float64(9),
		"host_info": `{"os":"windows","ipn_version":"1.78.1"}`,
	}, now)
	if strInfo.OS != "windows" || strInfo.Version != "1.78.1" {
		t.Fatalf("string host_info = %q/%q", strInfo.OS, strInfo.Version)
	}
}

func TestNodeRelativeTime(t *testing.T) {
	now := time.Unix(1700000000, 0)
	cases := []struct {
		at   time.Time
		lang string
		want string
	}{
		{time.Time{}, "zh", "从未"},
		{now.Add(-3 * time.Second), "zh", "刚刚"},
		{now.Add(-42 * time.Second), "zh", "42 秒前"},
		{now.Add(-5 * time.Minute), "zh", "5 分钟前"},
		{now.Add(-3 * time.Hour), "zh", "3 小时前"},
		{now.Add(-50 * time.Hour), "zh", "2 天前"},
		{now.Add(-42 * time.Second), "en", "42s ago"},
	}
	for _, c := range cases {
		if got := relTime(c.at, c.lang, now); got != c.want {
			t.Errorf("relTime(%v) = %q, want %q", c.at, got, c.want)
		}
	}
	if got := expiryText(time.Time{}, "zh", now); got != "长期有效" {
		t.Errorf("no expiry = %q", got)
	}
	if got := expiryText(now.Add(-time.Hour), "zh", now); got != "已过期" {
		t.Errorf("expired = %q", got)
	}
	if got := expiryText(now.Add(48*time.Hour), "zh", now); !strings.Contains(got, "2 天") {
		t.Errorf("expiry in 2 days = %q", got)
	}
}

func TestNewNodesViewLocalizes(t *testing.T) {
	now := time.Now()
	snap := NodeSnapshot{
		Available: true,
		Service:   "active",
		Total:     1,
		Online:    1,
		Nodes: []NodeStat{{
			ID: 1, Name: "n1", Online: true, State: "online",
			LastSeen: now.Add(-30 * time.Second),
			Expiry:   now.Add(10 * 24 * time.Hour),
			OS:       "linux", Register: "REGISTER_METHOD_AUTH_KEY",
		}},
	}
	zh := newNodesView(snap, "zh")
	if zh.Nodes[0].StateText != "在线" || zh.Nodes[0].OSText != "Linux" {
		t.Fatalf("zh view = %+v", zh.Nodes[0])
	}
	if zh.Nodes[0].RegisterText != "预授权密钥" {
		t.Fatalf("register = %q", zh.Nodes[0].RegisterText)
	}
	en := newNodesView(snap, "en")
	if en.Nodes[0].StateText != "Online" || !strings.Contains(en.Nodes[0].ExpiryText, "days left") {
		t.Fatalf("en view = %+v", en.Nodes[0])
	}
	// 视图不能修改原始快照。
	if snap.Nodes[0].StateText != "" {
		t.Fatal("snapshot was mutated by view")
	}
}

func TestEvaluateAlerts(t *testing.T) {
	th := AlertThresholds{CPU: 85, Mem: 90, LoadRatio: 2}
	base := alertInput{
		Host:  HostSnapshot{Available: true, CPUPercent: 10, MemPercent: 20, CPUCount: 4, Load1: 0.2},
		Nodes: NodeSnapshot{Service: "active", Nodes: []NodeStat{{Name: "a", Online: true, State: "online"}}},
		Lang:  "zh",
	}
	if got := evaluateAlerts(base, th); len(got) != 0 {
		t.Fatalf("healthy input produced alerts: %+v", got)
	}

	hot := base
	hot.Host.CPUPercent = 95
	hot.Host.MemPercent = 93
	hot.Host.Load1 = 9.5
	got := evaluateAlerts(hot, th)
	keys := map[string]bool{}
	for _, a := range got {
		keys[a.Key] = true
	}
	for _, k := range []string{"host.cpu", "host.mem", "host.load"} {
		if !keys[k] {
			t.Errorf("missing alert %s in %+v", k, got)
		}
	}

	down := base
	down.Nodes.Service = "failed"
	down.Nodes.Nodes = []NodeStat{
		{Name: "a", Online: false, State: "offline"},
		{Name: "b", Online: false, State: "expired"},
	}
	down.Nodes.Error = "cli failed"
	got = evaluateAlerts(down, th)
	keys = map[string]bool{}
	for _, a := range got {
		keys[a.Key] = true
		if a.Key == "service" && a.Level != "critical" {
			t.Errorf("service alert level = %s", a.Level)
		}
	}
	for _, k := range []string{"service", "nodes.offline", "nodes.expired", "nodes.error"} {
		if !keys[k] {
			t.Errorf("missing alert %s in %+v", k, got)
		}
	}
}

func TestAlertThresholdsFromEnv(t *testing.T) {
	t.Setenv("HSA_ALERT_CPU", "70")
	t.Setenv("HSA_ALERT_MEM", "75")
	t.Setenv("HSA_ALERT_LOAD", "1.5")
	th := defaultAlertThresholds()
	if th.CPU != 70 || th.Mem != 75 || th.LoadRatio != 1.5 {
		t.Fatalf("thresholds = %+v", th)
	}
	t.Setenv("HSA_ALERT_CPU", "abc")
	t.Setenv("HSA_ALERT_MEM", "500")
	if th := defaultAlertThresholds(); th.CPU != 85 || th.Mem != 100 {
		t.Fatalf("fallback thresholds = %+v", th)
	}
}

func TestParseSpeedFile(t *testing.T) {
	counters := parseSpeedFile([]byte(`{"12": {"rx_bytes": 1000, "tx_bytes": 2000}, "laptop": {"rx": 5, "tx": 6}}`))
	if c := counters["12"]; c.rx != 1000 || c.tx != 2000 {
		t.Fatalf("id entry = %+v", c)
	}
	if c := counters["laptop"]; c.rx != 5 || c.tx != 6 {
		t.Fatalf("name entry = %+v", c)
	}
	if got := parseSpeedFile([]byte("not json")); len(got) != 0 {
		t.Fatalf("invalid json parsed to %+v", got)
	}
	if got := parseSpeedFile([]byte(`{"a": {}}`)); len(got) != 1 {
		t.Fatalf("empty counters should still keep the key: %+v", got)
	}
}

func TestNodeSpeedRates(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/speed.json"
	nodes := []NodeStat{{ID: 12, Name: "laptop"}, {ID: 13, Name: "phone"}}
	m := &NodeMonitor{speedFile: file, prevSpeed: map[string]speedCounter{}}

	// 数据源不存在：整体不可用，节点显示占位。
	if _, ok := m.nodeSpeedRates(nodes, time.Now()); ok {
		t.Fatal("missing file should mark source unavailable")
	}

	// 首轮采样只有计数器，速率为 0 但数据可用。
	t0 := time.Now()
	writeSpeedFile(t, file, `{"12": {"rx_bytes": 1000, "tx_bytes": 500}}`)
	rates, ok := m.nodeSpeedRates(nodes, t0)
	if !ok {
		t.Fatal("first sample should be available")
	}
	if r := rates["12"]; r.rx != 0 || r.tx != 0 {
		t.Fatalf("first sample rate = %+v, want 0", r)
	}
	if _, found := rates["13"]; found {
		t.Fatal("node without counters must not get a rate entry")
	}

	// 15 秒后增量 1500/300 字节 → 约 0.098 KB/s 下行、0.02 KB/s 上行。
	writeSpeedFile(t, file, `{"12": {"rx_bytes": 2500, "tx_bytes": 800}}`)
	rates, ok = m.nodeSpeedRates(nodes, t0.Add(15*time.Second))
	if !ok {
		t.Fatal("second sample should be available")
	}
	r := rates["12"]
	if wantRx, wantTx := 1500.0/15/1024, 300.0/15/1024; r.rx < wantRx*0.99 || r.rx > wantRx*1.01 || r.tx < wantTx*0.99 || r.tx > wantTx*1.01 {
		t.Fatalf("rate = %+v, want ~%v/%v KB/s", r, wantRx, wantTx)
	}

	// 计数器回退（节点或 agent 重启）按 0 处理，不产生负速率。
	writeSpeedFile(t, file, `{"12": {"rx_bytes": 10, "tx_bytes": 5}}`)
	rates, _ = m.nodeSpeedRates(nodes, t0.Add(30*time.Second))
	if r := rates["12"]; r.rx != 0 || r.tx != 0 {
		t.Fatalf("counter reset rate = %+v, want 0", r)
	}

	// 也支持按节点名匹配。
	writeSpeedFile(t, file, `{"laptop": {"rx_bytes": 100, "tx_bytes": 100}}`)
	m2 := &NodeMonitor{speedFile: file, prevSpeed: map[string]speedCounter{}}
	m2.nodeSpeedRates(nodes, t0)
	writeSpeedFile(t, file, `{"laptop": {"rx_bytes": 250, "tx_bytes": 100}}`)
	rates, _ = m2.nodeSpeedRates(nodes, t0.Add(15*time.Second))
	want := 150.0 / 15 / 1024
	if r := rates["12"]; r.rx < want*0.99 || r.rx > want*1.01 {
		t.Fatalf("name-matched rate = %+v, want ~%v KB/s", r, want)
	}
}

func writeSpeedFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProtoTimeFormatsNumbers(t *testing.T) {
	// JSON 解码后的秒数是 float64，不能被科学计数法带偏。
	// 时间按本机时区渲染，期望值必须同样按时区推导，不能写死某个时区的字符串。
	const sec = 1794975942
	want := time.Unix(sec, 0).Format("2006-01-02 15:04")
	if got := protoTime(map[string]any{"seconds": float64(sec)}); got != want {
		t.Fatalf("float64 seconds = %q, want %q", got, want)
	}
	if got := protoTime(map[string]any{"seconds": "1794975942"}); got != want {
		t.Fatalf("string seconds = %q, want %q", got, want)
	}
	if got := protoTime(map[string]any{"seconds": float64(0)}); got != "—" {
		t.Fatalf("zero seconds = %q", got)
	}
}

func TestMaskedKey(t *testing.T) {
	if !maskedKey("hskey-auth-kOMPJmNkPSAZ-***") {
		t.Fatal("masked key not detected")
	}
	if maskedKey("hskey-auth-fullkeyvalue123") {
		t.Fatal("plain key reported as masked")
	}
}
