package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestSpeedSettingsTokenVisibility 验证节点网速上报令牌只对 admin 角色下发。
//
// 上报令牌等价于节点侧的写入凭据：拿到它就能往 /api/speed 写任意节点的
// 计数器。readonly 账号只是看，不该拿到这个凭据。
func TestSpeedSettingsTokenVisibility(t *testing.T) {
	a := &App{
		speed:    newSpeedReporter("secret-token"),
		port:     "3001",
		dataFile: filepath.Join(t.TempDir(), "app.db"),
	}

	cases := []struct {
		name      string
		role      string
		wantToken string
	}{
		{"管理员可见", "admin", "secret-token"},
		{"只读账号不可见", "readonly", ""},
		{"未登录不可见", "", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://192.0.2.10:3001/settings", nil)
		if c.role != "" {
			req = req.WithContext(context.WithValue(req.Context(), sessionKey, &Session{Role: c.role}))
		}
		p := &ConfigPage{}
		a.applySpeedSettings(req, p)

		if p.SpeedToken != c.wantToken {
			t.Fatalf("%s: token = %q，期望 %q", c.name, p.SpeedToken, c.wantToken)
		}
		// 地址与通道状态对所有角色都可见，它们不是凭据。
		if p.SpeedEndpoint != "http://192.0.2.10:3001/api/speed" {
			t.Fatalf("%s: endpoint = %q", c.name, p.SpeedEndpoint)
		}
		if !p.SpeedEnabled {
			t.Fatalf("%s: 通道应显示为已启用", c.name)
		}
	}
}

// TestSpeedSettingsChannelDisabled 验证没有配置令牌时通道显示为未启用，
// 且页面上不会出现一个空的令牌框。
func TestSpeedSettingsChannelDisabled(t *testing.T) {
	a := &App{
		speed:    newSpeedReporter(""),
		port:     "3001",
		dataFile: filepath.Join(t.TempDir(), "app.db"),
	}
	req := httptest.NewRequest(http.MethodGet, "http://192.0.2.10:3001/settings", nil)
	req = req.WithContext(context.WithValue(req.Context(), sessionKey, &Session{Role: "admin"}))
	p := &ConfigPage{}
	a.applySpeedSettings(req, p)

	if p.SpeedEnabled {
		t.Fatal("没有令牌时通道不应显示为已启用")
	}
	if p.SpeedToken != "" {
		t.Fatalf("没有令牌时不应下发令牌，得到 %q", p.SpeedToken)
	}
}

// TestSpeedEndpointScheme 验证管理界面开了 HTTPS 之后，上报地址也用 https。
func TestSpeedEndpointScheme(t *testing.T) {
	a := &App{
		speed:    newSpeedReporter("t"),
		port:     "3001",
		dataFile: filepath.Join(t.TempDir(), "app.db"),
	}
	req := httptest.NewRequest(http.MethodGet, "https://192.0.2.10:3001/settings", nil)
	if got := a.speedEndpoint(req); got != "https://192.0.2.10:3001/api/speed" {
		t.Fatalf("endpoint = %q，期望 https", got)
	}
	req = httptest.NewRequest(http.MethodGet, "http://192.0.2.10:3001/settings", nil)
	if got := a.speedEndpoint(req); got != "http://192.0.2.10:3001/api/speed" {
		t.Fatalf("endpoint = %q，期望 http", got)
	}
}

// TestTrustedHost 验证上报地址里的主机名来源可信。
//
// Host 头是客户端自己写的：伪造它就能让管理员复制到一个指向别处的
// 上报地址。所以只有按地址直连、或配置里出现过的域名才会被采用。
func TestTrustedHost(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("server_url: https://hs.example.com\ndns:\n  base_domain: hs.example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{port: "3001", hs: &HSClient{Config: cfg}}

	cases := []struct {
		name string
		host string
		want string
	}{
		{"按 IP 直连", "192.0.2.10:3001", "192.0.2.10:3001"},
		{"本机", "localhost:3001", "localhost:3001"},
		{"配置里的域名", "hs.example.com:3001", "hs.example.com:3001"},
		{"来路不明的域名", "evil.example.net:3001", "127.0.0.1:3001"},
		{"空 Host", "", "127.0.0.1:3001"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://"+c.host+"/settings", nil)
		if c.host == "" {
			req.Host = ""
		}
		if got := a.trustedHost(req); got != c.want {
			t.Fatalf("%s: host = %q，期望 %q", c.name, got, c.want)
		}
	}
}
