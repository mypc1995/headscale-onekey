package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestFlashRoundTrip 验证一次性提示消息（flash）能写入、取回，且只取一次。
//
// 这条用例守着 TakeFlash 的实现：SQLite 的 UPDATE ... RETURNING 返回的是
// 更新之后的值，若用它来「取出并清空」flash，取回来的必然是空串，
// 页面上所有提示消息都不会显示。
func TestFlashRoundTrip(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "flash.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()
	m := NewSessionManager(db, false)

	s := m.Create("admin", "admin")
	m.SetFlash(s.Token, "这是一条 flash 消息")

	got := m.TakeFlash(s.Token)
	if got != "这是一条 flash 消息" {
		t.Fatalf("取回 flash 失败: 得到 %q，期望 %q", got, "这是一条 flash 消息")
	}
	// 取过一次后应为空，避免同一条提示重复显示。
	if again := m.TakeFlash(s.Token); again != "" {
		t.Fatalf("flash 应只取一次，第二次得到 %q", again)
	}
}

// TestFlashOverwrite 验证后写入的提示会覆盖前一条，且不会残留旧值。
func TestFlashOverwrite(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "flash.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()
	m := NewSessionManager(db, false)

	s := m.Create("admin", "admin")
	m.SetFlash(s.Token, "第一条")
	m.SetFlash(s.Token, "第二条")
	if got := m.TakeFlash(s.Token); got != "第二条" {
		t.Fatalf("取回 flash 失败: 得到 %q，期望 %q", got, "第二条")
	}
}

// TestFlashEmptyMessage 验证没有提示时返回空串，且不会误伤会话本身。
func TestFlashEmptyMessage(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "flash.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()
	m := NewSessionManager(db, false)

	s := m.Create("admin", "admin")
	if got := m.TakeFlash(s.Token); got != "" {
		t.Fatalf("无提示时应返回空串，得到 %q", got)
	}
	// 会话仍应可用。
	if _, ok := m.Get(s.Token); !ok {
		t.Fatal("取回 flash 后会话失效")
	}
}

// TestFlashUnknownToken 验证未知 token 不会panic，只返回空串。
func TestFlashUnknownToken(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "flash.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()
	m := NewSessionManager(db, false)

	if got := m.TakeFlash("不存在的token"); got != "" {
		t.Fatalf("未知 token 应返回空串，得到 %q", got)
	}
}

// TestActionDetail 验证提示详情不会把 CLI 的原始输出当成人看的文案。
//
// headscale CLI 调用一律带 -o json，变更类命令（建用户、删节点、改标签…）的
// stdout 就是 JSON，空结果是一个 "{}"。直接拼进提示会变成「操作成功：{}」
// 这种没人看得懂的文案——JSON 是给程序看的，不是给人看的。
func TestActionDetail(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空输出", "", ""},
		{"JSON 对象", "{}", ""},
		{"JSON 数组", `[{"id":1}]`, ""},
		{"带空白的 JSON", "  {}  ", ""},
		{"纯文本", "node renamed", "node renamed"},
		{"纯文本带首尾空白", "  done\n", "done"},
	}
	for _, c := range cases {
		if got := actionDetail(c.in); got != c.want {
			t.Errorf("%s: actionDetail(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}

	// 过长的详情要截断，避免把整段 CLI 输出塞进提示条。
	long := strings.Repeat("a", 300)
	got := actionDetail(long)
	if len([]rune(got)) != 161 || !strings.HasSuffix(got, "…") {
		t.Errorf("过长详情应截断为 160 字符加省略号，实际长度 %d：%q", len([]rune(got)), got)
	}

	// 中文详情按字节切会把汉字劈成两半（乱码），必须按 rune 截断。
	cn := strings.Repeat("节", 300)
	got = actionDetail(cn)
	if len([]rune(got)) != 161 || !strings.HasSuffix(got, "…") {
		t.Errorf("中文详情应按 rune 截断为 160 字符加省略号，实际长度 %d：%q", len([]rune(got)), got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("截断后的中文详情不是合法 UTF-8：%q", got)
	}
}
