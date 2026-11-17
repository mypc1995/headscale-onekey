package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
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
		name       string
		cert, key  string
		wantField  string
		wantCode   string
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
