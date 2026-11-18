package main

import (
	"testing"
	"time"
)

func TestJoinCommand(t *testing.T) {
	cases := []struct {
		url, key, want string
	}{
		{"https://hs.example.com", "k123", "tailscale up --login-server=https://hs.example.com --authkey=k123"},
		{"http://192.0.2.10:18080", "abc", "tailscale up --login-server=http://192.0.2.10:18080 --authkey=abc"},
		{"", "k123", ""},      // 缺地址不拼命令
		{"https://x", "", ""}, // 缺密钥不拼命令
		{"", "", ""},
	}
	for _, c := range cases {
		if got := joinCommand(c.url, c.key); got != c.want {
			t.Errorf("joinCommand(%q, %q) = %q, want %q", c.url, c.key, got, c.want)
		}
	}
}

func TestJoinKeyStoreTakeOnce(t *testing.T) {
	s := newJoinKeyStore()
	s.Set("tok1", "secret-key")
	if got := s.Take("tok1"); got != "secret-key" {
		t.Fatalf("首次取出 = %q, want secret-key", got)
	}
	if got := s.Take("tok1"); got != "" {
		t.Fatalf("密钥只能取一次，第二次取到 %q", got)
	}
}

func TestJoinKeyStoreScopesByToken(t *testing.T) {
	s := newJoinKeyStore()
	s.Set("tok1", "k1")
	if got := s.Take("tok2"); got != "" {
		t.Fatalf("其他会话不应取到密钥，取到 %q", got)
	}
	if got := s.Take("tok1"); got != "k1" {
		t.Fatalf("本会话取到 %q, want k1", got)
	}
}

func TestJoinKeyStoreOverwriteAndExpiry(t *testing.T) {
	s := newJoinKeyStore()
	s.Set("tok1", "old")
	s.Set("tok1", "new")
	if got := s.Take("tok1"); got != "new" {
		t.Fatalf("同一会话再生成应覆盖旧密钥，取到 %q", got)
	}

	s.items["tok2"] = joinKeyEntry{key: "expired", expires: time.Now().Add(-time.Minute)}
	if got := s.Take("tok2"); got != "" {
		t.Fatalf("过期密钥不应取出，取到 %q", got)
	}
}

func TestJoinKeyStoreIgnoresEmpty(t *testing.T) {
	s := newJoinKeyStore()
	s.Set("", "k")
	s.Set("tok", "")
	if got := s.Take(""); got != "" {
		t.Fatalf("空 token 不应存取，取到 %q", got)
	}
	if got := s.Take("tok"); got != "" {
		t.Fatalf("空密钥不应保存，取到 %q", got)
	}
}

func TestExtractKeyVariants(t *testing.T) {
	if got := extractKey(`{"key":"hskey-auth-abc"}`); got != "hskey-auth-abc" {
		t.Fatalf("preauth key = %q", got)
	}
	if got := extractKey(`{"apiKey":"hskey-api-xyz"}`); got != "hskey-api-xyz" {
		t.Fatalf("api key object = %q", got)
	}
	// apikeys create 实际输出是裸 JSON 字符串。
	if got := extractKey(`"hskey-api-bare"`); got != "hskey-api-bare" {
		t.Fatalf("bare json string = %q", got)
	}
	if got := extractKey(`plain-string-key`); got != "plain-string-key" {
		t.Fatalf("plain = %q", got)
	}
}
