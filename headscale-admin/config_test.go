package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
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

// TestBuildConfigIPv6Toggle 验证「启用 IPv6 网段」开关两个方向都真的落到配置里。
//
// 这条守着「这个选项无效」那个缺陷：开关关掉时如果只是「不写」，
// 配置里原来的 prefixes.v6 会原样留着，headscale 仍然按 IPv6 网段分配地址，
// 开关等于没开；页面重新加载时又变回勾选状态，看着就像选项没用。
func TestBuildConfigIPv6Toggle(t *testing.T) {
	base := "server_url: http://x:8080\n" +
		"listen_addr: 0.0.0.0:8080\n" +
		"metrics_listen_addr: 127.0.0.1:9090\n" +
		"grpc_listen_addr: 127.0.0.1:50443\n" +
		"prefixes:\n" +
		"  v4: 100.64.0.0/10\n" +
		"  v6: fd7a:115c:a1e0::/48\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	c := &HSClient{Config: path}
	p := &ConfigPage{
		ListenAddr:        "0.0.0.0:8080",
		MetricsListenAddr: "127.0.0.1:9090",
		GRPCListenAddr:    "127.0.0.1:50443",
		PrefixV4:          "100.64.0.0/10",
	}

	// 打开开关：把新的 IPv6 网段写进配置。
	p.PrefixV6 = "fd7a:115c:a1e0::/48"
	next, err := c.buildConfig(p, "network")
	if err != nil {
		t.Fatal(err)
	}
	if got := nestedValue(next, "prefixes", "v6"); got != "fd7a:115c:a1e0::/48" {
		t.Fatalf("打开开关后 prefixes.v6 = %q，期望写入新网段:\n%s", got, next)
	}

	// 关掉开关：配置里的 prefixes.v6 必须被删掉，而不是留着旧值。
	p.PrefixV6 = ""
	next, err = c.buildConfig(p, "network")
	if err != nil {
		t.Fatal(err)
	}
	if got := nestedValue(next, "prefixes", "v6"); got != "" {
		t.Fatalf("关掉开关后 prefixes.v6 仍是 %q，应该从配置里删掉:\n%s", got, next)
	}
	if got := nestedValue(next, "prefixes", "v4"); got != "100.64.0.0/10" {
		t.Fatalf("关掉 IPv6 时把 IPv4 网段也弄坏了: %q\n%s", got, next)
	}
	if strings.Contains(next, "v6:") {
		t.Fatalf("配置里还残留 v6 键:\n%s", next)
	}
}

// TestDeleteNestedKey 验证删键只动目标键，不伤到块里的其它内容。
func TestDeleteNestedKey(t *testing.T) {
	base := "prefixes:\n  v4: 100.64.0.0/10\n  v6: fd7a:115c:a1e0::/48\ndns:\n  magic_dns: true\n"
	got := deleteNestedKey(base, "prefixes", "v6")
	if strings.Contains(got, "v6:") {
		t.Fatalf("v6 键没有被删掉:\n%s", got)
	}
	if !strings.Contains(got, "v4: 100.64.0.0/10") || !strings.Contains(got, "magic_dns: true") {
		t.Fatalf("删键时伤到了别的内容:\n%s", got)
	}
	// 块或键不存在时原样返回。
	if got := deleteNestedKey(base, "nosuch", "v6"); got != base {
		t.Fatalf("块不存在时应原样返回:\n%s", got)
	}
	if got := deleteNestedKey(base, "prefixes", "v9"); got != base {
		t.Fatalf("键不存在时应原样返回:\n%s", got)
	}
}
