package main

import (
	"database/sql"
	"path/filepath"
	"testing"
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
