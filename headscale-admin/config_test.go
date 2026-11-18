package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// makePair 生成一对自签证书，用于验证粘贴内容的校验逻辑。
func makePair(t *testing.T, cn string) (certPEM, keyPEM string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("签发证书失败: %v", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return certPEM, keyPEM
}

func TestCheckPastedPair(t *testing.T) {
	certA, keyA := makePair(t, "hs.example.com")
	_, keyB := makePair(t, "hs.example.com")

	cases := []struct {
		name      string
		cert, key string
		wantField string
		wantCode  string
	}{
		{"匹配的证书与私钥", certA, keyA, "", ""},
		{"只粘贴了证书", certA, "", "cert_pem", "cfg.err.pasteBoth"},
		{"只粘贴了私钥", "", keyA, "cert_pem", "cfg.err.pasteBoth"},
		{"证书位置填了私钥", keyA, keyA, "cert_pem", "cfg.err.certPEM"},
		{"证书是乱码", "not a pem", keyA, "cert_pem", "cfg.err.certPEM"},
		{"私钥是乱码", certA, "not a pem", "key_pem", "cfg.err.keyPEM"},
		{"私钥与证书不匹配", certA, keyB, "cert_pem", "cfg.err.pairMismatch"},
	}
	for _, tc := range cases {
		field, code := checkPastedPair(tc.cert, tc.key)
		if field != tc.wantField || code != tc.wantCode {
			t.Errorf("%s: 得到 (%q, %q)，期望 (%q, %q)", tc.name, field, code, tc.wantField, tc.wantCode)
		}
	}
}

func TestValidateTLSManualWithoutPaste(t *testing.T) {
	// 没有粘贴内容、也没有现存证书时，应提示重新粘贴，而不是要求填写路径。
	p := &ConfigPage{TLSMode: "manual"}
	errs := validateTLS(p, "zh")
	if _, ok := errs["cert_pem"]; !ok {
		t.Fatalf("期望 cert_pem 报错，实际: %v", errs)
	}
	if _, ok := errs["tls_cert_path"]; ok {
		t.Fatalf("不应再出现 tls_cert_path 字段错误: %v", errs)
	}
}

func TestNestedListReadWrite(t *testing.T) {
	base := "server_url: http://x:8080\n" +
		"dns:\n" +
		"  magic_dns: true\n" +
		"  base_domain: hs.example.com\n" +
		"  nameservers:\n" +
		"    global:\n" +
		"      - 223.5.5.5\n" +
		"      - 119.29.29.29\n" +
		"    split: {}\n" +
		"  search_domains: []\n" +
		"derp:\n" +
		"  server:\n" +
		"    enabled: false\n"
	got := listValues(base, "dns", "nameservers", "global")
	if len(got) != 2 || got[0] != "223.5.5.5" || got[1] != "119.29.29.29" {
		t.Fatalf("global = %v", got)
	}
	if got := listValues(base, "dns", "search_domains"); len(got) != 0 {
		t.Fatalf("search = %v", got)
	}

	// 替换列表，兄弟键不受影响
	next, err := setNestedList(base, []string{"1.1.1.1", "8.8.8.8"}, "dns", "nameservers", "global")
	if err != nil {
		t.Fatal(err)
	}
	if got := listValues(next, "dns", "nameservers", "global"); len(got) != 2 || got[0] != "1.1.1.1" {
		t.Fatalf("after set, global = %v\n%s", got, next)
	}
	if !strings.Contains(next, "split: {}") {
		t.Fatalf("sibling lost:\n%s", next)
	}

	// 清空列表写成 []
	next, err = setNestedList(next, nil, "dns", "nameservers", "global")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "global: []") {
		t.Fatalf("empty list:\n%s", next)
	}

	// 中间层缺失时补齐
	missing := "dns:\n  magic_dns: true\n  base_domain: x\n"
	next, err = setNestedList(missing, []string{"9.9.9.9"}, "dns", "nameservers", "global")
	if err != nil {
		t.Fatal(err)
	}
	if got := listValues(next, "dns", "nameservers", "global"); len(got) != 1 || got[0] != "9.9.9.9" {
		t.Fatalf("auto-created:\n%s", next)
	}
	if !strings.Contains(next, "magic_dns: true") {
		t.Fatalf("dns block corrupted:\n%s", next)
	}

	// 顶层块缺失时追加
	next, err = setNestedList("server_url: http://x\n", []string{"1.0.0.1"}, "dns", "nameservers", "global")
	if err != nil {
		t.Fatal(err)
	}
	if got := listValues(next, "dns", "nameservers", "global"); len(got) != 1 || got[0] != "1.0.0.1" {
		t.Fatalf("top-level append:\n%s", next)
	}
}

func TestSetDeepKey(t *testing.T) {
	base := `derp:
  server:
    enabled: false
    region_id: 999
`
	next, err := setDeepKey(base, "derp", "server", "enabled", "true")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "enabled: true") || !strings.Contains(next, "region_id: 999") {
		t.Fatalf("set existing:\n%s", next)
	}
	next, err = setDeepKey(next, "derp", "server", "region_name", "test-relay")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "region_name: test-relay") {
		t.Fatalf("add key:\n%s", next)
	}
	// 中间块缺失时自动补齐
	next, err = setDeepKey("derp:\n  urls: []\n", "derp", "server", "enabled", "true")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "server:") || !strings.Contains(next, "enabled: true") || !strings.Contains(next, "urls: []") {
		t.Fatalf("add mid block:\n%s", next)
	}
}
