package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAdminTLSFiles 覆盖管理界面 HTTPS 配置的落盘与读取往返。
func TestAdminTLSFiles(t *testing.T) {
	dir := t.TempDir()
	dataFile := filepath.Join(dir, "headscale-admin.db")

	// 未配置文件时读取应为零值，且视为未启用。
	cfg := loadAdminTLS(dataFile)
	if cfg.Enabled || cfg.CertPath != "" || cfg.KeyPath != "" {
		t.Fatalf("未配置时应有零值，实际: %+v", cfg)
	}
	// 未启用时 validPair 应报错。
	if err := cfg.validPair(); err == nil {
		t.Fatal("未启用时 validPair 应报错")
	}

	// 写入后读回应完全一致。
	want := adminTLSConfig{Enabled: true, CertPath: "/x/admin.crt", KeyPath: "/x/admin.key"}
	if err := saveAdminTLS(dataFile, want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got := loadAdminTLS(dataFile)
	if got != want {
		t.Fatalf("往返不一致: 得到 %+v，期望 %+v", got, want)
	}

	// 配置文件应落在数据目录，且权限收紧到 0600。
	p := adminTLSPath(dataFile)
	if filepath.Dir(p) != dir {
		t.Fatalf("配置路径不在数据目录: %s", p)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("配置文件不存在: %v", err)
	}
	// Windows 不实现 POSIX 权限位，权限断言只在类 Unix 平台生效。
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("配置文件权限应为 0600，实际 %o", info.Mode().Perm())
	}

	// 覆盖写不应留下临时文件。
	saveAdminTLS(dataFile, adminTLSConfig{Enabled: false})
	if _, err := os.Stat(p + ".tmp"); err == nil {
		t.Fatal("不应残留 .tmp 文件")
	}
}

// TestAdminTLSWriteCerts 覆盖粘贴 PEM 的落盘行为：配对成功才落盘，失败不污染目录。
func TestAdminTLSWriteCerts(t *testing.T) {
	dir := t.TempDir()
	dataFile := filepath.Join(dir, "headscale-admin.db")

	certPEM, keyPEM := makePair(t, "admin.example.com")

	// 配对正确：应写入证书目录，且权限为 0600。
	certPath, keyPath, err := writeAdminCerts(dataFile, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("写入证书失败: %v", err)
	}
	if filepath.Dir(certPath) != adminCertDir(dataFile) {
		t.Fatalf("证书应写入自管目录: %s", certPath)
	}
	for _, f := range []string{certPath, keyPath} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatalf("证书文件缺失 %s: %v", f, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("%s 权限应为 0600，实际 %o", f, info.Mode().Perm())
		}
	}
	// 落盘后应能通过 validPair 校验。
	cfg := adminTLSConfig{Enabled: true, CertPath: certPath, KeyPath: keyPath}
	if err := cfg.validPair(); err != nil {
		t.Fatalf("落盘后的证书应配对成功: %v", err)
	}

	// 证书与私钥不匹配：应报错，且不留下证书文件。
	_, otherKey := makePair(t, "admin.example.com")
	badDir := t.TempDir()
	badData := filepath.Join(badDir, "headscale-admin.db")
	if _, _, err := writeAdminCerts(badData, certPEM, otherKey); err == nil {
		t.Fatal("不匹配的证书与私钥应报错")
	}
	entries, err := os.ReadDir(adminCertDir(badData))
	if err == nil {
		for _, e := range entries {
			t.Fatalf("校验失败后不应落盘文件，实际存在 %s", e.Name())
		}
	}

	// 空 PEM：不落盘。
	emptyDir := t.TempDir()
	emptyData := filepath.Join(emptyDir, "headscale-admin.db")
	if _, _, err := writeAdminCerts(emptyData, "", keyPEM); err == nil {
		t.Fatal("空证书应报错")
	}
	if _, _, err := writeAdminCerts(emptyData, certPEM, ""); err == nil {
		t.Fatal("空私钥应报错")
	}
	if _, err := os.Stat(adminCertDir(emptyData)); err == nil {
		t.Fatal("空 PEM 不应创建证书目录")
	}
}

// TestResolveAdminTLSFallback 覆盖「配置启用 HTTPS 但证书不可用」的回退行为，
// 以及随之而来的 Cookie Secure 标记。
//
// 这条曾经是个真缺陷：Secure 标记按配置里的 Enabled 算，而证书可用性在
// 启动流程的更后面才检查。于是回退成 HTTP 后 Cookie 仍带 Secure，浏览器
// 明文下不发送该 Cookie → 登录态失效 → 运维入口被锁死，
// 而「回退 HTTP 以免锁死」这个兜底正是要防这件事。
func TestResolveAdminTLSFallback(t *testing.T) {
	dir := t.TempDir()
	dataFile := filepath.Join(dir, "headscale-admin.db")

	// 未配置文件：既不启用 HTTPS，也不该给 Cookie 加 Secure。
	_, tlsOn := resolveAdminTLS(dataFile)
	if tlsOn {
		t.Fatal("未配置时不应判定为 HTTPS 生效")
	}
	if sessionCookieSecure(false, tlsOn) {
		t.Fatal("未配置时 Cookie 不应带 Secure")
	}

	// 配置启用、但证书文件不存在：必须回退为 HTTP。
	broken := adminTLSConfig{
		Enabled:  true,
		CertPath: filepath.Join(dir, "missing.crt"),
		KeyPath:  filepath.Join(dir, "missing.key"),
	}
	if err := saveAdminTLS(dataFile, broken); err != nil {
		t.Fatal(err)
	}
	cfg, tlsOn := resolveAdminTLS(dataFile)
	if tlsOn {
		t.Fatal("证书不可用时不应判定为 HTTPS 生效")
	}
	if cfg.Enabled {
		t.Fatal("证书不可用时生效状态应为关闭")
	}
	// 回退后的关键不变量：Cookie 不能带 Secure，否则明文下登不进去。
	if sessionCookieSecure(false, tlsOn) {
		t.Fatal("回退 HTTP 后 Cookie 不应带 Secure")
	}

	// 换成真实可用的证书：应判定为 HTTPS 生效，Cookie 随之带 Secure。
	certPEM, keyPEM := makePair(t, "admin.example.com")
	certPath, keyPath, err := writeAdminCerts(dataFile, certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveAdminTLS(dataFile, adminTLSConfig{Enabled: true, CertPath: certPath, KeyPath: keyPath}); err != nil {
		t.Fatal(err)
	}
	_, tlsOn = resolveAdminTLS(dataFile)
	if !tlsOn {
		t.Fatal("证书可用时应判定为 HTTPS 生效")
	}
	if !sessionCookieSecure(false, tlsOn) {
		t.Fatal("HTTPS 生效时 Cookie 应带 Secure")
	}

	// 强制 Secure（反向代理终止 TLS 的场景）仍然生效。
	if !sessionCookieSecure(true, false) {
		t.Fatal("强制 Secure 时应带 Secure")
	}
}

// TestAdminTLSReuseExisting 覆盖「两项留空 = 沿用已有证书」时仍需重新校验配对。
func TestAdminTLSReuseExisting(t *testing.T) {
	dir := t.TempDir()
	dataFile := filepath.Join(dir, "headscale-admin.db")

	certPEM, keyPEM := makePair(t, "admin.example.com")
	certPath, keyPath, err := writeAdminCerts(dataFile, certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	cfg := adminTLSConfig{Enabled: true, CertPath: certPath, KeyPath: keyPath}
	if err := cfg.validPair(); err != nil {
		t.Fatalf("已有证书应可用: %v", err)
	}

	// 私钥被换掉后，沿用路径的校验必须失败，避免带病重启锁死界面。
	otherCert, _ := makePair(t, "admin.example.com")
	if err := os.WriteFile(certPath, []byte(strings.TrimSpace(otherCert)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.validPair(); err == nil {
		t.Fatal("证书与私钥不配对应报错")
	}
}
