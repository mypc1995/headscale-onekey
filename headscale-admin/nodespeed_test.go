package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// postSpeed 发起一次上报请求，返回 HTTP 状态码与响应体。
func postSpeed(t *testing.T, r *speedReporter, token string, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/speed", strings.NewReader(body))
	if token != "" {
		req.Header.Set("X-Node-Speed-Token", token)
	}
	rec := httptest.NewRecorder()
	r.handle(rec, req)
	return rec.Code, rec.Body.String()
}

func TestSpeedReporterDisabledWithoutToken(t *testing.T) {
	// 没有配置令牌时整个通道必须关闭，绝不能变成匿名可写接口。
	r := newSpeedReporter("")
	code, body := postSpeed(t, r, "", `{"node":"12","rx_bytes":1,"tx_bytes":2}`)
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", code, body)
	}
	if len(r.data.rates(time.Now())) != 0 {
		t.Fatal("disabled reporter must not accept data")
	}
}

func TestSpeedReporterRejectsWrongToken(t *testing.T) {
	r := newSpeedReporter("right-token")
	cases := []struct {
		name  string
		token string
	}{
		{"完全错误", "wrong-token"},
		{"前缀相同", "right"},
		{"多一个字符", "right-tokens"},
		{"空令牌", ""},
	}
	for _, c := range cases {
		code, body := postSpeed(t, r, c.token, `{"node":"12","rx_bytes":1,"tx_bytes":2}`)
		if code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401 (body %s)", c.name, code, body)
		}
	}
	if len(r.data.rates(time.Now())) != 0 {
		t.Fatal("rejected requests must not write counters")
	}
}

func TestSpeedReporterAcceptsTokenInThreePlaces(t *testing.T) {
	r := newSpeedReporter("right-token")

	// 请求头
	code, body := postSpeed(t, r, "right-token", `{"node":"12","rx_bytes":100,"tx_bytes":200}`)
	if code != http.StatusOK {
		t.Fatalf("header token: status = %d, want 200 (body %s)", code, body)
	}

	// Authorization: Bearer
	req := httptest.NewRequest(http.MethodPost, "/api/speed", strings.NewReader(`{"node":"13","rx_bytes":1,"tx_bytes":1}`))
	req.Header.Set("Authorization", "Bearer right-token")
	rec := httptest.NewRecorder()
	r.handle(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer token: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// 请求体字段
	code, body = postSpeed(t, r, "", `{"node":"14","rx_bytes":1,"tx_bytes":1,"token":"right-token"}`)
	if code != http.StatusOK {
		t.Fatalf("body token: status = %d, want 200 (body %s)", code, body)
	}

	rates := r.data.rates(time.Now())
	for _, key := range []string{"12", "13", "14"} {
		if _, ok := rates[key]; !ok {
			t.Fatalf("missing counters for %s: %+v", key, rates)
		}
	}
}

func TestSpeedReporterBatchPayload(t *testing.T) {
	r := newSpeedReporter("tok")
	code, body := postSpeed(t, r, "tok", `[{"node":"12","rx_bytes":1,"tx_bytes":2},{"node":"13","rx_bytes":3,"tx_bytes":4}]`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", code, body)
	}
	var resp struct {
		OK       bool `json:"ok"`
		Accepted int  `json:"accepted"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("response not json: %v (%s)", err, body)
	}
	if !resp.OK || resp.Accepted != 2 {
		t.Fatalf("response = %+v, want ok with 2 accepted", resp)
	}
}

func TestSpeedReporterRejectsBadPayload(t *testing.T) {
	r := newSpeedReporter("tok")
	for _, body := range []string{"", "not json", `{"rx_bytes":1}`} {
		code, _ := postSpeed(t, r, "tok", body)
		if code != http.StatusBadRequest {
			t.Fatalf("payload %q: status = %d, want 400", body, code)
		}
	}
	// 令牌不对时优先返回 401，不泄露请求体的解析细节。
	code, _ := postSpeed(t, r, "bad", "not json")
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for bad token", code)
	}
}

func TestLoadSpeedTokenPrefersFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("file-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadSpeedToken("env-token", path); got != "file-token" {
		t.Fatalf("token = %q, want file-token", got)
	}
	if got := loadSpeedToken("env-token", filepath.Join(dir, "missing")); got != "env-token" {
		t.Fatalf("token = %q, want env-token fallback", got)
	}
	if got := loadSpeedToken("", ""); got != "" {
		t.Fatalf("token = %q, want empty", got)
	}
}

func TestSpeedReportRates(t *testing.T) {
	s := newSpeedReport(true)
	t0 := time.Now()

	// 第一次上报只有基准值，速率为 0。
	s.put("12", speedCounter{rx: 1000, tx: 500}, t0)
	rates := s.rates(t0)
	if r := rates["12"]; r.rx != 0 || r.tx != 0 {
		t.Fatalf("first rate = %+v, want 0", r)
	}

	// 15 秒内多了 30KB/15KB → 2 KB/s 下行、1 KB/s 上行。
	s.put("12", speedCounter{rx: 1000 + 30*1024, tx: 500 + 15*1024}, t0.Add(15*time.Second))
	rates = s.rates(t0.Add(15 * time.Second))
	r := rates["12"]
	if math.Abs(r.rx-2.0) > 0.01 || math.Abs(r.tx-1.0) > 0.01 {
		t.Fatalf("rate = %+v, want 2.0/1.0 KB/s", r)
	}

	// 计数器回退（节点或 agent 重启）按 0 处理，不产生负速率。
	s.put("12", speedCounter{rx: 10, tx: 5}, t0.Add(30*time.Second))
	rates = s.rates(t0.Add(30 * time.Second))
	if r := rates["12"]; r.rx != 0 || r.tx != 0 {
		t.Fatalf("counter reset rate = %+v, want 0", r)
	}

	// 超过 TTL 没有更新的条目要被清掉。
	s.put("13", speedCounter{rx: 1, tx: 1}, t0)
	rates = s.rates(t0.Add(speedReportTTL + time.Second))
	if _, ok := rates["13"]; ok {
		t.Fatalf("stale entry survived: %+v", rates)
	}
}

func TestNodeSpeedRatesFromAPIChannel(t *testing.T) {
	rep := newSpeedReport(true)
	m := &NodeMonitor{prevSpeed: map[string]speedCounter{}, reported: rep}
	nodes := []NodeStat{
		{ID: 12, Name: "laptop", IPs: []string{"100.64.0.5"}},
		{ID: 13, Name: "phone"},
	}

	// 没有任何上报时数据源不可用。
	if _, _, ok := m.nodeSpeedRates(nodes, time.Now()); ok {
		t.Fatal("no report should mark source unavailable")
	}

	t0 := time.Now()
	// 用 tailnet IP 上报：节点侧只知道自己的 IP，ID 只有服务端清楚。
	rep.put("100.64.0.5", speedCounter{rx: 1000, tx: 500}, t0)
	rates, channel, ok := m.nodeSpeedRates(nodes, t0)
	if !ok {
		t.Fatal("api report should be available")
	}
	if channel != "api" {
		t.Fatalf("channel = %q, want api", channel)
	}
	if r := rates["12"]; r.rx != 0 || r.tx != 0 {
		t.Fatalf("first rate = %+v, want 0", r)
	}
	if _, found := rates["13"]; found {
		t.Fatal("node without report must not get a rate entry")
	}

	rep.put("100.64.0.5", speedCounter{rx: 1000 + 30*1024, tx: 500 + 15*1024}, t0.Add(15*time.Second))
	rates, channel, ok = m.nodeSpeedRates(nodes, t0.Add(15*time.Second))
	if !ok || channel != "api" {
		t.Fatalf("ok=%v channel=%q, want true/api", ok, channel)
	}
	r := rates["12"]
	if math.Abs(r.rx-2.0) > 0.01 || math.Abs(r.tx-1.0) > 0.01 {
		t.Fatalf("rate = %+v, want 2.0/1.0 KB/s", r)
	}
}

func TestNodeSpeedRatesMatchesByNameAndID(t *testing.T) {
	rep := newSpeedReport(true)
	m := &NodeMonitor{prevSpeed: map[string]speedCounter{}, reported: rep}
	nodes := []NodeStat{{ID: 12, Name: "laptop"}, {ID: 13, Name: "phone"}}
	t0 := time.Now()

	// 按节点名上报。
	rep.put("laptop", speedCounter{rx: 1, tx: 1}, t0)
	// 按节点 ID 上报。
	rep.put("13", speedCounter{rx: 1, tx: 1}, t0)
	rates, _, ok := m.nodeSpeedRates(nodes, t0)
	if !ok {
		t.Fatal("reports should be available")
	}
	if _, found := rates["12"]; !found {
		t.Fatalf("name-matched node missing: %+v", rates)
	}
	if _, found := rates["13"]; !found {
		t.Fatalf("id-matched node missing: %+v", rates)
	}
}

func TestNodeSpeedRatesPrefersAPIOverFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "speed.json")
	writeSpeedFile(t, file, `{"12": {"rx_bytes": 1000, "tx_bytes": 1000}}`)

	rep := newSpeedReport(true)
	m := &NodeMonitor{speedFile: file, prevSpeed: map[string]speedCounter{}, reported: rep}
	nodes := []NodeStat{{ID: 12, Name: "laptop"}}
	t0 := time.Now()

	rep.put("12", speedCounter{rx: 1, tx: 1}, t0)
	_, channel, ok := m.nodeSpeedRates(nodes, t0)
	if !ok {
		t.Fatal("api report should be available")
	}
	if channel != "api" {
		t.Fatalf("channel = %q, want api to win over file", channel)
	}
}

func TestSpeedReportEntryCap(t *testing.T) {
	// 令牌持有者若用大量不同的 key 上报，不能把内存撑爆。
	s := newSpeedReport(true)
	now := time.Now()
	for i := 0; i < maxSpeedReportEntries; i++ {
		if !s.put(strconv.Itoa(i), speedCounter{rx: 1, tx: 1}, now) {
			t.Fatalf("entry %d should be accepted", i)
		}
	}
	if s.put("overflow", speedCounter{rx: 1, tx: 1}, now) {
		t.Fatal("entry beyond the cap must be rejected")
	}
	// 已有条目仍然可以正常更新，不能被上限卡死。
	if !s.put("0", speedCounter{rx: 2, tx: 2}, now) {
		t.Fatal("updating an existing entry must still work")
	}
}

func TestParseSpeedPayload(t *testing.T) {
	items, err := parseSpeedPayload([]byte(`{"node":"12","rx_bytes":10,"tx_bytes":20}`))
	if err != nil || len(items) != 1 || items[0].Node != "12" || items[0].RxBytes != 10 || items[0].TxBytes != 20 {
		t.Fatalf("single payload = %+v err=%v", items, err)
	}
	items, err = parseSpeedPayload([]byte(`[{"node":"a"},{"node":"b"}]`))
	if err != nil || len(items) != 2 {
		t.Fatalf("batch payload = %+v err=%v", items, err)
	}
	for _, bad := range []string{"", "   ", "{", "not json"} {
		if _, err := parseSpeedPayload([]byte(bad)); err == nil {
			t.Fatalf("payload %q should fail to parse", bad)
		}
	}
	// 超过上限的批量要报错，避免一次请求撑爆内存。
	big := make([]byte, 0, speedReportLimit*40)
	big = append(big, '[')
	for i := 0; i < speedReportLimit+1; i++ {
		if i > 0 {
			big = append(big, ',')
		}
		big = append(big, []byte(`{"node":"n"}`)...)
	}
	big = append(big, ']')
	if _, err := parseSpeedPayload(big); err == nil {
		t.Fatal("oversized batch should be rejected")
	}
}
