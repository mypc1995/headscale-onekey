package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeHeadscaleBin 造一个假的 headscale 可执行文件：把收到的参数记进 logPath，
// stdout 返回 "{}"。Windows 下 Go 的 exec 不能直接跑没有扩展名的脚本，
// 所以分别准备 .cmd 与 .sh 两种。
func fakeHeadscaleBin(t *testing.T, logPath string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		p := filepath.Join(dir, "fake-hs.cmd")
		body := "@echo off\r\necho %* >> \"" + logPath + "\"\r\necho {}\r\n"
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatalf("写假 headscale 失败: %v", err)
		}
		return p
	}
	p := filepath.Join(dir, "fake-hs.sh")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + logPath + "\"\necho '{}'\n"
	if err := os.WriteFile(p, []byte(body), 0700); err != nil {
		t.Fatalf("写假 headscale 失败: %v", err)
	}
	return p
}

// TestValidNodeLabel 节点名必须符合 DNS label 规则。
//
// headscale 用 dnsname.ValidLabel 校验：1~63 字节、首尾字母或数字、中间可含 '-'。
// 中文名一定被拒，所以要在本地就挡下来并给出可读提示，
// 而不是把命令行的英文报错丢给用户。
func TestValidNodeLabel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"普通名字", "r86s", true},
		{"单个字母", "a", true},
		{"带连字符", "office-router", true},
		{"数字开头", "86s", true},
		{"结尾是数字", "r86s2", true},
		{"63 个字符", strings.Repeat("a", 63), true},
		{"空", "", false},
		{"中文", "书房路由", false},
		{"带空格", "office router", false},
		{"带下划线", "office_router", false},
		{"以连字符开头", "-router", false},
		{"以连字符结尾", "router-", false},
		{"带点", "office.router", false},
		{"64 个字符", strings.Repeat("a", 64), false},
	}
	for _, c := range cases {
		if got := validNodeLabel(c.in); got != c.want {
			t.Errorf("%s: validNodeLabel(%q) = %v，期望 %v", c.name, c.in, got, c.want)
		}
	}
}

// TestNodeDisplayNamePrefersGivenName 节点列表显示的必须是受管名 given_name。
//
// headscale v0.29 的 `nodes rename` 改的是 given_name；JSON 里的 name 是客户端上报的
// 主机名。页面拿 name 当节点名显示，改完名就看不出变化——这就是「改了没生效」的根因。
func TestNodeDisplayNamePrefersGivenName(t *testing.T) {
	m := map[string]any{"given_name": "r86s", "name": "R86S"}
	if got := nodeDisplayName(m); got != "r86s" {
		t.Fatalf("节点名 = %q，期望受管名 r86s", got)
	}
	// 老版本 headscale 没有 given_name 时回落到客户端上报名。
	old := map[string]any{"name": "R86S"}
	if got := nodeDisplayName(old); got != "R86S" {
		t.Fatalf("缺少 given_name 时 = %q，期望回落到 R86S", got)
	}
}

// TestNodeRenameRejectsInvalidName 非法名必须在调用 headscale 之前就被挡下。
func TestNodeRenameRejectsInvalidName(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	store, cleanup := newTestStore(t)
	defer cleanup()
	m := NewSessionManager(store.DB, false)
	s := m.Create("admin", "admin")
	a := &App{store: store, sessions: m, hs: &HSClient{Bin: fakeHeadscaleBin(t, logPath)}}

	body := "csrf=" + url.QueryEscape(s.CSRF) + "&id=21&name=" + url.QueryEscape("书房路由")
	req := httptest.NewRequest(http.MethodPost, "/nodes/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), sessionKey, s))
	w := httptest.NewRecorder()
	a.nodeRename(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("状态码 = %d，期望 %d", w.Code, http.StatusSeeOther)
	}
	if loc := w.Header().Get("Location"); loc != "/nodes" {
		t.Fatalf("跳转目标 = %q，期望 /nodes", loc)
	}
	if flash := m.TakeFlash(s.Token); !strings.Contains(flash, "节点名不合法") {
		t.Fatalf("提示 = %q，期望包含「节点名不合法」", flash)
	}
	if b, err := os.ReadFile(logPath); err == nil && len(b) > 0 {
		t.Fatalf("非法名字仍然调用了 headscale：%s", b)
	}
}

// TestNodeRenameCallsHeadscale 合法名要带着新名字与节点 ID 调用命令行。
func TestNodeRenameCallsHeadscale(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	store, cleanup := newTestStore(t)
	defer cleanup()
	m := NewSessionManager(store.DB, false)
	s := m.Create("admin", "admin")
	a := &App{store: store, sessions: m, hs: &HSClient{Bin: fakeHeadscaleBin(t, logPath)}}

	body := "csrf=" + url.QueryEscape(s.CSRF) + "&id=21&name=office-router"
	req := httptest.NewRequest(http.MethodPost, "/nodes/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), sessionKey, s))
	w := httptest.NewRecorder()
	a.nodeRename(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("状态码 = %d，期望 %d", w.Code, http.StatusSeeOther)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读命令调用记录失败: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "nodes rename office-router -i 21 --force") {
		t.Fatalf("命令行参数 = %q，期望包含 \"nodes rename office-router -i 21 --force\"", got)
	}
	if flash := m.TakeFlash(s.Token); !strings.Contains(flash, "office-router") {
		t.Fatalf("提示 = %q，期望包含新名字", flash)
	}
}
