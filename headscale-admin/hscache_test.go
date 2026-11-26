package main

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTTLCache 验证 TTL 缓存的过期行为。
func TestTTLCache(t *testing.T) {
	var c ttlCache[string]
	if _, ok := c.get(time.Second); ok {
		t.Fatal("空缓存不该命中")
	}
	c.set("v1")
	if v, ok := c.get(time.Second); !ok || v != "v1" {
		t.Fatalf("应当命中且值为 v1，得到 %q %v", v, ok)
	}
	// 过期之后必须不再命中。
	if _, ok := c.get(-time.Second); ok {
		t.Fatal("过期后不该命中")
	}
}

// TestHSVersionCached 验证版本号只查一次 headscale。
//
// 版本号要起一个 headscale 进程才能拿到，而它一年也变不了几次。
// 每次页面刷新都去问一遍，纯属浪费。
func TestHSVersionCached(t *testing.T) {
	c := &HSClient{Bin: "/usr/bin/headscale"}
	calls := 0
	c.exec = func(ctx context.Context, name string, args ...string) (string, string, error) {
		calls++
		return "headscale version v0.29.7", "", nil
	}
	// 注意：Version() 走的是 execCombined，不走 c.exec，
	// 所以这里直接测缓存本身的行为。
	c.versionCache.set("v0.29.7")
	before := calls
	if v := c.Version(); v != "v0.29.7" {
		t.Fatalf("应当命中缓存，得到 %q", v)
	}
	if calls != before {
		t.Fatalf("命中缓存时不该再起进程")
	}
}

// TestHSVersionFailureNotCached 验证失败的结果不会被缓存。
//
// 一次偶发的失败被缓存住，页面上的版本号就会长时间显示「未知」。
func TestHSVersionFailureNotCached(t *testing.T) {
	c := &HSClient{Bin: "/usr/bin/headscale"}
	// 模拟 headscale 不存在：执行必然失败。
	old := execRunner
	defer func() { execRunner = old }()
	calls := 0
	execRunner = func(ctx context.Context, name string, args ...string) (string, string, error) {
		calls++
		return "", "not found", os.ErrNotExist
	}
	if v := c.Version(); v != "未知" {
		t.Fatalf("失败时应当返回未知，得到 %q", v)
	}
	if v := c.Version(); v != "未知" {
		t.Fatalf("失败时应当返回未知，得到 %q", v)
	}
	if calls < 2 {
		t.Fatalf("失败结果不该被缓存，第二次应当重试（调用 %d 次）", calls)
	}
}

// TestHSCachedListAccessors 验证带缓存的列表接口命中缓存后不再起进程。
func TestHSCachedListAccessors(t *testing.T) {
	c := &HSClient{Bin: "/usr/bin/headscale"}
	calls := 0
	c.exec = func(ctx context.Context, name string, args ...string) (string, string, error) {
		calls++
		return `[{"id":1,"name":"default"}]`, "", nil
	}
	if _, err := c.cachedUsers(); err != nil {
		t.Fatal(err)
	}
	first := calls
	if _, err := c.cachedUsers(); err != nil {
		t.Fatal(err)
	}
	if calls != first {
		t.Fatalf("命中缓存时不该再起进程（%d -> %d）", first, calls)
	}
	// 缓存过期后要重新取。
	c.usersCache.set(nil)
	c.usersCache.at = time.Now().Add(-time.Hour)
	if _, err := c.cachedUsers(); err != nil {
		t.Fatal(err)
	}
	if calls == first {
		t.Fatal("缓存过期后应当重新取数据")
	}
}

// TestSecurityHeaders 验证安全响应头都在，且 CSP 不放开内联脚本。
func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	head := w.Header()

	for k, want := range map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "same-origin",
	} {
		if got := head.Get(k); got != want {
			t.Errorf("%s = %q，期望 %q", k, got, want)
		}
	}
	csp := head.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP 缺少 %q，当前值：%s", want, csp)
		}
	}
	if strings.Contains(csp, "'unsafe-inline'") && !strings.Contains(csp, "style-src 'self' 'unsafe-inline'") {
		t.Errorf("只有 style-src 可以放开 unsafe-inline，当前值：%s", csp)
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("script-src 不该放开内联脚本：%s", csp)
	}
	if head.Get("Strict-Transport-Security") != "" {
		t.Error("明文请求不该加 HSTS")
	}

	// HTTPS 才加 HSTS。
	r2 := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	r2.TLS = &tls.ConnectionState{}
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Header().Get("Strict-Transport-Security") == "" {
		t.Error("HTTPS 请求应当带 HSTS")
	}
}

// TestNoInlineScriptInTemplates 验证模板里没有内联脚本。
//
// CSP 的 script-src 不带 'unsafe-inline'：模板里一旦出现内联脚本，
// 浏览器会直接把它挡掉，功能就静默失效了。
func TestNoInlineScriptInTemplates(t *testing.T) {
	entries, err := os.ReadDir("templates")
	if err != nil {
		t.Skipf("读不到模板目录: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("templates", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for i := 0; ; {
			j := strings.Index(s[i:], "<script")
			if j < 0 {
				break
			}
			start := i + j
			// 找到这一段标签的结尾
			end := strings.Index(s[start:], ">")
			if end < 0 {
				break
			}
			tag := s[start : start+end]
			if !strings.Contains(tag, "src=") {
				t.Errorf("%s 里有内联脚本：%s", e.Name(), strings.TrimSpace(tag))
			}
			i = start + end
		}
	}
}
