package main

import (
	"testing"
	"time"
)

func TestKeyNotesStore(t *testing.T) {
	s, err := OpenStore(t.TempDir()+"/t.db", t.TempDir()+"/none.json")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()

	if got := s.KeyNotes("preauth"); len(got) != 0 {
		t.Fatalf("empty store returned %v", got)
	}
	if err := s.SetKeyNote("preauth", 3, "机房服务器 3 号机"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeyNote("preauth", 4, "办公笔记本"); err != nil {
		t.Fatal(err)
	}
	// 覆盖更新
	if err := s.SetKeyNote("preauth", 3, "机房 3 号机（重装）"); err != nil {
		t.Fatal(err)
	}
	got := s.KeyNotes("preauth")
	if got[3] != "机房 3 号机（重装）" || got[4] != "办公笔记本" {
		t.Fatalf("notes = %v", got)
	}
	// 空备注等价删除
	if err := s.SetKeyNote("preauth", 4, "  "); err != nil {
		t.Fatal(err)
	}
	if got := s.KeyNotes("preauth"); len(got) != 1 {
		t.Fatalf("after clear, notes = %v", got)
	}
	// 清理已删除密钥的备注
	s.PruneKeyNotes("preauth", map[int64]bool{})
	if got := s.KeyNotes("preauth"); len(got) != 0 {
		t.Fatalf("after prune, notes = %v", got)
	}
	// 不同 kind 互不影响
	_ = s.SetKeyNote("preauth", 1, "a")
	_ = s.SetKeyNote("api", 1, "b")
	if s.KeyNotes("preauth")[1] != "a" || s.KeyNotes("api")[1] != "b" {
		t.Fatal("kinds mixed up")
	}
}

func TestKeyIDParsing(t *testing.T) {
	if got := extractKeyID(`{"id": "5", "key": "hskey-x"}`); got != 5 {
		t.Fatalf("string id = %d", got)
	}
	if got := extractKeyID(`{"id": 7, "key": "hskey-x"}`); got != 7 {
		t.Fatalf("number id = %d", got)
	}
	if got := extractKeyID(`not json`); got != 0 {
		t.Fatalf("invalid = %d", got)
	}
	if got := keyID("12"); got != 12 {
		t.Fatalf("keyID string = %d", got)
	}
	if got := keyID(float64(9)); got != 9 {
		t.Fatalf("keyID float = %d", got)
	}
}

func TestKeyExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour).Unix()
	future := time.Now().Add(time.Hour).Unix()
	if !keyExpired(map[string]any{"expiration": map[string]any{"seconds": float64(past)}}) {
		t.Fatal("past expiration should be expired")
	}
	if keyExpired(map[string]any{"expiration": map[string]any{"seconds": float64(future)}}) {
		t.Fatal("future expiration should not be expired")
	}
	if !keyExpired(map[string]any{"expired": true}) {
		t.Fatal("explicit expired flag")
	}
	if keyExpired(map[string]any{}) {
		t.Fatal("missing expiration should not be expired")
	}
}

func TestIsTrue(t *testing.T) {
	if !isTrue(true) || !isTrue("true") {
		t.Fatal("true values not detected")
	}
	if isTrue(false) || isTrue("false") || isTrue(nil) || isTrue(float64(1)) {
		t.Fatal("false values reported true")
	}
}
