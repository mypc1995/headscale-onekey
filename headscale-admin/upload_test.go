package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uploadFile 是构造上传请求时的一个文件项。
type uploadFile struct {
	field   string
	content string
}

// newUploadRequest 构造一个 multipart 上传请求（模拟浏览器选文件后提交表单）。
func newUploadRequest(t *testing.T, files []uploadFile, fields map[string]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("写字段失败: %v", err)
		}
	}
	for _, f := range files {
		fw, err := w.CreateFormFile(f.field, f.field)
		if err != nil {
			t.Fatalf("创建文件段失败: %v", err)
		}
		if _, err := io.WriteString(fw, f.content); err != nil {
			t.Fatalf("写文件段失败: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}
	req := httptest.NewRequest("POST", "/settings/tls", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if err := req.ParseMultipartForm(maxUploadBytes); err != nil {
		t.Fatalf("解析 multipart 失败: %v", err)
	}
	return req
}

// TestReadUploadedPair 上传公钥、中间证书、私钥三个文件，能拼出完整链。
func TestReadUploadedPair(t *testing.T) {
	rootPEM, interPEM, leafPEM, keyPEM := makeThreeLevelChain(t, "upload.example.com")
	req := newUploadRequest(t, []uploadFile{
		{"tls_cert_file", leafPEM},
		{"tls_chain_file", interPEM + rootPEM},
		{"tls_key_file", keyPEM},
	}, map[string]string{"tls_mode": "manual"})

	up, field, code := readUploadedPair(req)
	if field != "" || code != "" {
		t.Fatalf("上传应被接受，得到 (%q, %q)", field, code)
	}
	if field, code := checkPastedPair(up.cert, up.key); field != "" || code != "" {
		t.Fatalf("上传得到的证书链应通过配对与链校验，得到 (%q, %q)", field, code)
	}
	certs := parseCertChain(up.cert)
	if len(certs) != 3 {
		t.Fatalf("上传后应有 3 张证书，得到 %d 张", len(certs))
	}
	if certs[0].Subject.CommonName != "upload.example.com" {
		t.Fatalf("链首应是叶子证书，得到 %q", certs[0].Subject.CommonName)
	}
}

// TestReadUploadedPairSortsChain 中间证书文件里顺序颠倒时，服务端按签名关系重排。
//
// CA 交付的 bundle 顺序经常不是「叶子 → 中间 → 根」，
// 用户照着文件原样上传很容易排错，重排后就不用手工调顺序。
func TestReadUploadedPairSortsChain(t *testing.T) {
	rootPEM, interPEM, leafPEM, keyPEM := makeThreeLevelChain(t, "upload2.example.com")
	req := newUploadRequest(t, []uploadFile{
		{"tls_cert_file", leafPEM},
		{"tls_chain_file", rootPEM + interPEM}, // 故意倒序
		{"tls_key_file", keyPEM},
	}, nil)

	up, field, code := readUploadedPair(req)
	if field != "" || code != "" {
		t.Fatalf("上传应被接受，得到 (%q, %q)", field, code)
	}
	certs := parseCertChain(up.cert)
	if len(certs) != 3 {
		t.Fatalf("应有 3 张证书，得到 %d 张", len(certs))
	}
	if certs[1].Subject.CommonName != "Test Sub CA" {
		t.Fatalf("第 2 张应是中间 CA，得到 %q", certs[1].Subject.CommonName)
	}
	if certs[2].Subject.CommonName != "Test Root CA" {
		t.Fatalf("第 3 张应是根 CA，得到 %q", certs[2].Subject.CommonName)
	}
	if field, code := checkPastedPair(up.cert, up.key); field != "" || code != "" {
		t.Fatalf("重排后应通过链校验，得到 (%q, %q)", field, code)
	}
}

// TestReadUploadedPairSelfSignedOnly 只传公钥与私钥（自签证书场景）也要能走通。
func TestReadUploadedPairSelfSignedOnly(t *testing.T) {
	certPEM, keyPEM := makePair(t, "self-upload.example.com")
	req := newUploadRequest(t, []uploadFile{
		{"tls_cert_file", certPEM},
		{"tls_key_file", keyPEM},
	}, nil)

	up, field, code := readUploadedPair(req)
	if field != "" || code != "" {
		t.Fatalf("上传应被接受，得到 (%q, %q)", field, code)
	}
	if field, code := checkPastedPair(up.cert, up.key); field != "" || code != "" {
		t.Fatalf("自签证书应通过校验，得到 (%q, %q)", field, code)
	}
}

// TestReadUploadedPairErrors 各种缺失/错误情况都要报出对应字段。
func TestReadUploadedPairErrors(t *testing.T) {
	rootPEM, interPEM, leafPEM, keyPEM := makeThreeLevelChain(t, "err.example.com")

	cases := []struct {
		name      string
		files     []uploadFile
		wantField string
		wantCode  string
	}{
		{
			name:      "只选了公钥没选私钥",
			files:     []uploadFile{{"tls_cert_file", leafPEM}},
			wantField: "key_pem",
			wantCode:  "cfg.err.keyFileEmpty",
		},
		{
			name:      "只选了私钥没选公钥",
			files:     []uploadFile{{"tls_key_file", keyPEM}},
			wantField: "cert_pem",
			wantCode:  "cfg.err.certFileEmpty",
		},
		{
			name: "公钥文件里不是证书",
			files: []uploadFile{
				{"tls_cert_file", "这不是证书，只是一段说明文字\n"},
				{"tls_key_file", keyPEM},
			},
			wantField: "cert_pem",
			wantCode:  "cfg.err.certFileInvalid",
		},
	}
	for _, tc := range cases {
		req := newUploadRequest(t, tc.files, nil)
		_, field, code := readUploadedPair(req)
		if field != tc.wantField || code != tc.wantCode {
			t.Errorf("%s: 得到 (%q, %q)，期望 (%q, %q)", tc.name, field, code, tc.wantField, tc.wantCode)
		}
	}
	_ = rootPEM
	_ = interPEM
}

// TestReadUploadedPairNoUpload 没选文件时不算上传，应回落到粘贴的文本。
func TestReadUploadedPairNoUpload(t *testing.T) {
	req := newUploadRequest(t, nil, map[string]string{"tls_mode": "manual", "tls_cert_pem": "abc"})
	up, field, code := readUploadedPair(req)
	if field != "" || code != "" {
		t.Fatalf("没有上传文件时不应报错，得到 (%q, %q)", field, code)
	}
	if up.cert != "" || up.key != "" {
		t.Fatalf("没有上传文件时不应产生内容，得到 %q/%q", up.cert, up.key)
	}
}

// TestReadUploadedPairMismatchedKey 私钥与证书不配对时，上传本身解析成功，
// 但配对校验必须拦下来（不能让不匹配的证书写进服务器）。
func TestReadUploadedPairMismatchedKey(t *testing.T) {
	_, _, leafPEM, _ := makeThreeLevelChain(t, "a.example.com")
	_, _, _, otherKey := makeThreeLevelChain(t, "b.example.com")
	req := newUploadRequest(t, []uploadFile{
		{"tls_cert_file", leafPEM},
		{"tls_key_file", otherKey},
	}, nil)

	up, field, code := readUploadedPair(req)
	if field != "" || code != "" {
		t.Fatalf("上传解析本身应成功，得到 (%q, %q)", field, code)
	}
	if field, code := checkPastedPair(up.cert, up.key); field != "cert_pem" || code != "cfg.err.pairMismatch" {
		t.Fatalf("配对不匹配应被拦下，得到 (%q, %q)", field, code)
	}
}

// TestNormalizePEMText 上传的文件可能带 BOM 与 CRLF，要统一成干净的 PEM 文本。
func TestNormalizePEMText(t *testing.T) {
	in := "\ufeff-----BEGIN CERTIFICATE-----\r\nMIIF\r\n-----END CERTIFICATE-----\r\n"
	got := normalizePEMText(in)
	if strings.Contains(got, "\r") {
		t.Fatalf("应已去掉 CR，得到 %q", got)
	}
	if strings.HasPrefix(got, "\ufeff") {
		t.Fatalf("应已去掉 BOM，得到 %q", got)
	}
	if got != "-----BEGIN CERTIFICATE-----\nMIIF\n-----END CERTIFICATE-----" {
		t.Fatalf("归一化结果不符：%q", got)
	}
}

// TestSaveManualCertWritesAndRollsBack 落盘到临时目录，并能在失败时还原旧文件。
func TestSaveManualCertWritesAndRollsBack(t *testing.T) {
	old := manualCertDir
	manualCertDir = t.TempDir()
	defer func() { manualCertDir = old }()

	certPEM, keyPEM := makePair(t, "write.example.com")
	certPath, keyPath, rollback, err := saveManualCert(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	if filepath.Base(certPath) != "manual.crt" || filepath.Base(keyPath) != "manual.key" {
		t.Fatalf("落盘路径不符：%s / %s", certPath, keyPath)
	}
	body, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}
	if !strings.Contains(string(body), "BEGIN CERTIFICATE") {
		t.Fatalf("落盘内容不是 PEM：%q", string(body))
	}

	// 模拟保存后配置没应用成功：回滚应把旧文件恢复回来。
	secondCert, secondKey := makePair(t, "write2.example.com")
	_, _, undo, err := saveManualCert(secondCert, secondKey)
	if err != nil {
		t.Fatalf("二次落盘失败: %v", err)
	}
	undo()
	restored, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("回滚后读取失败: %v", err)
	}
	if string(restored) != string(body) {
		t.Fatalf("回滚后内容与原先不一致")
	}
	rollback()
}
