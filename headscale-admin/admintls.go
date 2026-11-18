package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// restartSelf 延迟重启管理界面进程（systemd 会自动拉起，加载新 HTTPS 配置）。
// 延迟 1 秒让当前响应先发出；非 systemd 环境直接返回错误由界面提示手动重启。
func restartSelf() error {
	if os.Getenv("INVOCATION_ID") == "" {
		return errors.New("not running under systemd")
	}
	go func() {
		time.Sleep(time.Second)
		_ = exec.Command("systemctl", "restart", "headscale-admin").Run()
	}()
	return nil
}
