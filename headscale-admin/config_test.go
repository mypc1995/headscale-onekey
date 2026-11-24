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

	// 开关来回切换后配置应能字节级还原：新键要追加到块末尾，
	// 否则 prefixes.v6 会被插到 v4 上面，配置顺序跟着开关来回抖。
	//
	// 注意：buildConfig 每次都重新读磁盘上的配置，所以这里必须先把「关掉」
	// 的结果落盘，下一次「打开」才是真的在「没有 v6 的配置」上新增键。
	if err := os.WriteFile(path, []byte(next), 0600); err != nil {
		t.Fatal(err)
	}
	p.PrefixV6 = "fd7a:115c:a1e0::/48"
	again, err := c.buildConfig(p, "network")
	if err != nil {
		t.Fatal(err)
	}
	if again != base {
		t.Fatalf("开关来回切换后配置没能还原:\n got: %q\nwant: %q", again, base)
	}
}

// TestNestedValueBlockScope 验证块内取值不会越界命中后面别的块里的同名键。
//
// 踩过的坑：nestedValue / deepValue 原来也带 (?s)，`.*` 贪婪吞到文件末尾。
// 把 prefixes.v6 删掉之后，读取路径又从后面的块里翻出一个同名键，
// 页面开关照样显示已勾选——删了却没生效，看着就像「开关是假的」。
func TestNestedValueBlockScope(t *testing.T) {
	content := "prefixes:\n" +
		"  v4: 100.64.0.0/10\n" +
		"\n" +
		"  # allocation strategy\n" +
		"  allocation: sequential\n" +
		"\n" +
		"dns:\n" +
		"  magic_dns: true\n" +
		"  v6: other-block-value\n" +
		"  base_domain: hs.example.com\n"

	if got := nestedValue(content, "prefixes", "v6"); got != "" {
		t.Fatalf("prefixes 块没有 v6，却读到了 %q（越界命中了别的块）", got)
	}
	if got := nestedValue(content, "prefixes", "v4"); got != "100.64.0.0/10" {
		t.Fatalf("prefixes.v4 = %q，期望 100.64.0.0/10", got)
	}
	if got := nestedValue(content, "dns", "v6"); got != "other-block-value" {
		t.Fatalf("dns.v6 = %q，期望 other-block-value", got)
	}
	if got := nestedValue(content, "dns", "base_domain"); got != "hs.example.com" {
		t.Fatalf("dns.base_domain = %q，期望 hs.example.com", got)
	}
	// 块内先有的 v6 仍要读对。
	withV6 := strings.Replace(content, "  # allocation strategy", "  v6: fd7a::/48\n  # allocation strategy", 1)
	if got := nestedValue(withV6, "prefixes", "v6"); got != "fd7a::/48" {
		t.Fatalf("prefixes.v6 = %q，期望 fd7a::/48", got)
	}
}

// TestSetNestedKeyInsertPos 验证块内新增的键插在正确位置，且不会粘到上一行。
func TestSetNestedKeyInsertPos(t *testing.T) {
	got, err := setNestedKey("prefixes:\n  v4: 100.64.0.0/10\n", "prefixes", "v6", "fd7a:115c:a1e0::/48")
	if err != nil {
		t.Fatal(err)
	}
	want := "prefixes:\n  v4: 100.64.0.0/10\n  v6: fd7a:115c:a1e0::/48\n"
	if got != want {
		t.Fatalf("新键没有插在 v4 之后:\n got: %q\nwant: %q", got, want)
	}
	// 块内最后一行没有换行时，新键也不能被粘到同一行。
	got2, err := setNestedKey("prefixes:\n  v4: 100.64.0.0/10", "prefixes", "v6", "fd7a::/48")
	if err != nil {
		t.Fatal(err)
	}
	want2 := "prefixes:\n  v4: 100.64.0.0/10\n  v6: fd7a::/48\n"
	if got2 != want2 {
		t.Fatalf("块末行无换行时新键被粘连:\n got: %q\nwant: %q", got2, want2)
	}
}

// TestNestedKeyBlockScope 验证块内增删键只影响目标块。
//
// 这是踩过的坑：块定位正则原来带 (?s)，.* 贪婪吞到文件末尾，
// 于是「新增键」被追加到**文件末尾**（写出一个顶层键，headscale 根本不认，
// 而 configtest 还会因为容忍未知键而放行），「删除键」也会误伤后面别的块里的同名键。
func TestNestedKeyBlockScope(t *testing.T) {
	// 形状照抄线上 config.yaml：prefixes 块后面有空行、注释段和另一个键，
	// 再往后还有别的顶层块。
	base := "server_url: https://h:18080\n" +
		"prefixes:\n" +
		"  v4: 100.64.0.0/10\n" +
		"\n" +
		"  # Strategy used for allocation of IPs to nodes.\n" +
		"  allocation: sequential\n" +
		"\n" +
		"dns:\n" +
		"  magic_dns: false\n" +
		"  v6: other-block-value\n"

	got, err := setNestedKey(base, "prefixes", "v6", "fd7a:115c:a1e0::/48")
	if err != nil {
		t.Fatal(err)
	}
	want := "server_url: https://h:18080\n" +
		"prefixes:\n" +
		"  v4: 100.64.0.0/10\n" +
		"  v6: fd7a:115c:a1e0::/48\n" +
		"\n" +
		"  # Strategy used for allocation of IPs to nodes.\n" +
		"  allocation: sequential\n" +
		"\n" +
		"dns:\n" +
		"  magic_dns: false\n" +
		"  v6: other-block-value\n"
	if got != want {
		t.Fatalf("新键没有落到 prefixes 块内（或位置不对）:\n got: %q\nwant: %q", got, want)
	}

	// 删除时不能误伤后面其它块里的同名键。
	got2 := deleteNestedKey(got, "prefixes", "v6")
	if !strings.Contains(got2, "  v6: other-block-value") {
		t.Fatalf("删键误伤了别的块里的同名键:\n%s", got2)
	}
	if strings.Contains(got2, "fd7a:115c:a1e0::/48") {
		t.Fatalf("prefixes.v6 没有被删掉:\n%s", got2)
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

// makeCAChain 生成「CA → 叶子」两级证书，用于验证证书链完整性校验。
func makeCAChain(t *testing.T, cn string) (caPEM, leafPEM, keyPEM string) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 CA 密钥失败: %v", err)
	}
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(10),
		Subject:               pkix.Name{CommonName: "Test Intermediate CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("签发 CA 证书失败: %v", err)
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成叶子密钥失败: %v", err)
	}
	leafTpl := &x509.Certificate{
		SerialNumber: big.NewInt(11),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTpl, caTpl, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("签发叶子证书失败: %v", err)
	}
	caPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	leafPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)}))
	return caPEM, leafPEM, keyPEM
}

// TestCheckCertChain 验证「只粘叶子证书」会被拦下。
//
// 这正是线上事故的成因：服务端只下发叶子证书，浏览器能自动补中间证书，
// 但 Tailscale 客户端（Go 写的 tailscaled 不做 AIA 自动补链）会直接报
// x509: certificate signed by unknown authority，连控制服务都上不去。
func TestCheckCertChain(t *testing.T) {
	caPEM, leafPEM, keyPEM := makeCAChain(t, "ts.example.com")
	otherCA, _, _ := makeCAChain(t, "other.example.com")

	cases := []struct {
		name      string
		certPEM   string
		wantField string
		wantCode  string
	}{
		{"完整链（叶子 + 中间）", leafPEM + caPEM, "", ""},
		{"只粘叶子证书", leafPEM, "cert_pem", "cfg.err.certChainIncomplete"},
		{"链接不上（中间证书不对）", leafPEM + otherCA, "cert_pem", "cfg.err.certChainBroken"},
	}
	for _, tc := range cases {
		field, code := checkPastedPair(tc.certPEM, keyPEM)
		if field != tc.wantField || code != tc.wantCode {
			t.Errorf("%s: 得到 (%q, %q)，期望 (%q, %q)", tc.name, field, code, tc.wantField, tc.wantCode)
		}
	}
}

// TestCheckCertChainAllowsSelfSigned 自签证书只有一张，不算缺链（mkcert 场景）。
func TestCheckCertChainAllowsSelfSigned(t *testing.T) {
	certPEM, keyPEM := makePair(t, "self.example.com")
	if field, code := checkPastedPair(certPEM, keyPEM); field != "" || code != "" {
		t.Fatalf("自签证书应放行，得到 (%q, %q)", field, code)
	}
}
