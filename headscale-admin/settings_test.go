package main

import (
	"context"
	"net/http"
	"net/http/httptest"
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
		req := httptest.NewRequest(http.MethodGet, "http://hs.example.com:3001/settings", nil)
		if c.role != "" {
			req = req.WithContext(context.WithValue(req.Context(), sessionKey, &Session{Role: c.role}))
		}
		p := &ConfigPage{}
		a.applySpeedSettings(req, p)

		if p.SpeedToken != c.wantToken {
			t.Fatalf("%s: token = %q，期望 %q", c.name, p.SpeedToken, c.wantToken)
		}
		// 地址与通道状态对所有角色都可见，它们不是凭据。
		if p.SpeedEndpoint != "http://hs.example.com:3001/api/speed" {
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
	req := httptest.NewRequest(http.MethodGet, "http://hs.example.com:3001/settings", nil)
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
	req := httptest.NewRequest(http.MethodGet, "https://hs.example.com:3001/settings", nil)
	if got := a.speedEndpoint(req); got != "https://hs.example.com:3001/api/speed" {
		t.Fatalf("endpoint = %q，期望 https", got)
	}
	req = httptest.NewRequest(http.MethodGet, "http://hs.example.com:3001/settings", nil)
	if got := a.speedEndpoint(req); got != "http://hs.example.com:3001/api/speed" {
		t.Fatalf("endpoint = %q，期望 http", got)
	}
}
