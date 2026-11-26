package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// adminTLSConfig 是管理界面自身的 HTTPS 配置，存在数据目录下的 admin-tls.json。
// headscale 的证书只管控制面服务，网页服务的 HTTPS 由这里独立控制。
type adminTLSConfig struct {
	Enabled  bool   `json:"enabled"`
	CertPath string `json:"cert_path"`
	KeyPath  string `json:"key_path"`
}

func adminTLSPath(dataFile string) string {
	return filepath.Join(filepath.Dir(dataFile), "admin-tls.json")
}

// loadAdminTLS 读取管理界面 HTTPS 配置；文件不存在视为未启用。
func loadAdminTLS(dataFile string) adminTLSConfig {
	var cfg adminTLSConfig
	b, err := os.ReadFile(adminTLSPath(dataFile))
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(b, &cfg)
	return cfg
}

func saveAdminTLS(dataFile string, cfg adminTLSConfig) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	p := adminTLSPath(dataFile)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// validPair 返回证书文件是否可用（存在且配对成功）。
func (c adminTLSConfig) validPair() error {
	if !c.Enabled {
		return errors.New("https disabled")
	}
	if _, err := tls.LoadX509KeyPair(c.CertPath, c.KeyPath); err != nil {
		return err
	}
	return nil
}

// adminCertDir 是管理界面自管证书的存放目录。
func adminCertDir(dataFile string) string {
	return filepath.Join(filepath.Dir(dataFile), "certs")
}

// writeAdminCerts 把粘贴的 PEM 证书写入管理界面证书目录（0600）。
func writeAdminCerts(dataFile, certPEM, keyPEM string) (certPath, keyPath string, err error) {
	certPEM = strings.TrimSpace(certPEM)
	keyPEM = strings.TrimSpace(keyPEM)
	if certPEM == "" || keyPEM == "" {
		return "", "", errors.New("empty pem")
	}
	dir := adminCertDir(dataFile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", err
	}
	certPath = filepath.Join(dir, "admin.crt")
	keyPath = filepath.Join(dir, "admin.key")
	// 先写临时文件并校验配对，失败不落盘。
	tmpCert := certPath + ".tmp"
	tmpKey := keyPath + ".tmp"
	if err := os.WriteFile(tmpCert, []byte(certPEM+"\n"), 0600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(tmpKey, []byte(keyPEM+"\n"), 0600); err != nil {
		os.Remove(tmpCert)
		return "", "", err
	}
	if _, err := tls.LoadX509KeyPair(tmpCert, tmpKey); err != nil {
		os.Remove(tmpCert)
		os.Remove(tmpKey)
		return "", "", fmt.Errorf("certificate and key do not match: %w", err)
	}
	if err := os.Rename(tmpCert, certPath); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmpKey, keyPath); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

// resolveAdminTLS 解析管理界面 HTTPS 的**实际生效**状态。
//
// 配置里写着启用、但证书不可用（文件被删、损坏、路径写错）时，记日志并回退为
// HTTP。第二个返回值是「真的走 HTTPS」——监听协议和 Cookie 的 Secure 标记
// 都必须以它为准，不能各看各的：回退成明文后 Cookie 若仍带 Secure，浏览器
// 在 HTTP 下不会回传该 Cookie，登录态立刻失效，运维入口被锁死，
// 恰好是这个兜底本来要防的事。
func resolveAdminTLS(dataFile string) (adminTLSConfig, bool) {
	cfg := loadAdminTLS(dataFile)
	if !cfg.Enabled {
		return cfg, false
	}
	if err := cfg.validPair(); err != nil {
		log.Printf("管理界面 HTTPS 证书不可用，回退为 HTTP：%v", err)
		cfg.Enabled = false
		return cfg, false
	}
	return cfg, true
}

// sessionCookieSecure 计算会话 Cookie 是否带 Secure 标记。
//
// forceSecure 来自 -cookie-secure / HSA_COOKIE_SECURE（反向代理终止 TLS 时用），
// tlsOn 是实际生效的协议。两者都为 false 时绝不能带 Secure。
func sessionCookieSecure(forceSecure, tlsOn bool) bool {
	return forceSecure || tlsOn
}

// restartSelf 延迟重启管理界面进程（systemd 会自动拉起，加载新 HTTPS 配置）。
// 延迟 1 秒让当前响应先发出；非 systemd 环境直接返回错误由界面提示手动重启。
//
// 重启的单元名可用 HSA_SYSTEMD_UNIT 覆盖，默认 headscale-admin。
// 隔离实例用另一个单元名，才能在不碰生产服务的前提下测这条自重启链路。
func restartSelf() error {
	if os.Getenv("INVOCATION_ID") == "" {
		return errors.New("not running under systemd")
	}
	unit := os.Getenv("HSA_SYSTEMD_UNIT")
	if unit == "" {
		unit = "headscale-admin"
	}
	go func() {
		time.Sleep(time.Second)
		// 用独立上下文：重启是改系统状态的动作，不能因为发起它的那个
		// HTTP 请求断开就被取消——那样服务会停在新旧之间。
		_, _ = execCombined(context.Background(), hsWriteTimeout, "systemctl", "restart", unit)
	}()
	return nil
}
