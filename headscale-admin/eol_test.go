package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 浏览器提交的 textarea 内容按 HTML 规范用 CRLF 换行，服务端原样落盘，
// 配置文件就变成了 CRLF。headscale 自己的 YAML 解析器能接受 CRLF，
// 但本项目的行解析器（blockBody 要求块头后面紧跟 \n）认不出来，
// 于是 dns / derp 块里的值全被读成空：页面上域名是空的、开关是未勾选的，
// 用户再点一次「保存 DNS」，空值就被写回配置。
// 实测：生产 /etc/headscale/config.yaml 已被写成 CRLF（311 个 CR 字节）。

func TestNormalizeEOL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"CRLF 全转 LF", "a\r\nb\r\n", "a\nb\n"},
		{"单个 CR 也转 LF", "a\rb\r", "a\nb\n"},
		{"本来就没有 CR", "a\nb\n", "a\nb\n"},
		{"CRLF 不重复处理", "a\r\n\r\nb", "a\n\nb"},
	}
	for _, c := range cases {
		if got := normalizeEOL(c.in); got != c.want {
			t.Errorf("%s: normalizeEOL(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}

// TestParsersTolerateCRLF 验证 CRLF 配置文件里的值仍能被正确读出来。
func TestParsersTolerateCRLF(t *testing.T) {
	lf := "server_url: http://127.0.0.1:8080\n" +
		"dns:\n" +
		"  magic_dns: true\n" +
		"  base_domain: hs.example.com\n" +
		"  override_local_dns: false\n" +
		"  nameservers:\n" +
		"    global:\n" +
		"      - 223.5.5.5\n" +
		"derp:\n" +
		"  region_id: 900\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")

	if got := topValue(crlf, "server_url"); got != "http://127.0.0.1:8080" {
		t.Errorf("CRLF 配置里 server_url = %q，期望 http://127.0.0.1:8080", got)
	}
	if got := nestedValue(crlf, "dns", "base_domain"); got != "hs.example.com" {
		t.Errorf("CRLF 配置里 dns.base_domain = %q，期望 hs.example.com", got)
	}
	if got := nestedValue(crlf, "dns", "magic_dns"); got != "true" {
		t.Errorf("CRLF 配置里 dns.magic_dns = %q，期望 true", got)
	}
	if got := deepValue(crlf, "derp", "region_id"); got != "900" {
		t.Errorf("CRLF 配置里 derp.region_id = %q，期望 900", got)
	}
	got := listValues(crlf, "dns", "nameservers", "global")
	if len(got) != 1 || got[0] != "223.5.5.5" {
		t.Errorf("CRLF 配置里 dns.nameservers.global = %v，期望 [223.5.5.5]", got)
	}
}

// TestSaveConfigNormalizesEOL 验证整份配置落盘前被归一化成 LF。
func TestSaveConfigNormalizesEOL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server_url: http://127.0.0.1:8080\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &HSClient{Config: path}
	crlf := "server_url: http://127.0.0.1:8080\r\ndns:\r\n  magic_dns: true\r\n"

	if err := c.SaveConfig(crlf); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(b), '\r') {
		t.Fatalf("落盘的配置里还有 CR 字节：%q", string(b))
	}
	if got := nestedValue(string(b), "dns", "magic_dns"); got != "true" {
		t.Fatalf("落盘后读不回 dns.magic_dns：%q", got)
	}
}

// TestSavePolicyNormalizesEOL 验证策略文件落盘前也被归一化。
func TestSavePolicyNormalizesEOL(t *testing.T) {
	dir := t.TempDir()
	acl := filepath.Join(dir, "acl.json")
	path := filepath.Join(dir, "config.yaml")
	cfg := "policy:\n  path: " + acl + "\n"
	if err := os.WriteFile(path, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	c := &HSClient{Config: path}

	if err := c.SavePolicy("{\r\n  \"groups\": {\"group:admin\": []}\r\n}\r\n"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(acl)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(b), '\r') {
		t.Fatalf("落盘的策略文件里还有 CR 字节：%q", string(b))
	}
}

// TestConfigBytesSelfHealsCRLF 验证读配置时就会把 CRLF 归一化，
// 于是历史上被写成 CRLF 的配置会被自动「治愈」，页面能读到正确值。
func TestConfigBytesSelfHealsCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	crlf := "dns:\r\n  magic_dns: true\r\n  base_domain: hs.example.com\r\n"
	if err := os.WriteFile(path, []byte(crlf), 0600); err != nil {
		t.Fatal(err)
	}
	c := &HSClient{Config: path}

	text, err := c.ReadConfigText()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(text, '\r') {
		t.Fatalf("读出来的配置里还有 CR 字节：%q", text)
	}
	if got := nestedValue(text, "dns", "base_domain"); got != "hs.example.com" {
		t.Fatalf("CRLF 配置读出的 dns.base_domain = %q，期望 hs.example.com", got)
	}
}
