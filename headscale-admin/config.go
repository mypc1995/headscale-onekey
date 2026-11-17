package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ConfigPage 是「Headscale 配置」页面的表单模型与状态快照。
type ConfigPage struct {
	// 域名与接入
	ServerURL  string
	BaseDomain string
	MagicDNS   bool

	// IP 与监听
	ListenAddr        string
	MetricsListenAddr string
	GRPCListenAddr    string
	PrefixV4          string
	PrefixV6          string

	// SSL 证书
	TLSMode       string // letsencrypt | manual | http
	LEHostname    string
	ACMEEmail     string
	ACMEURL       string
	ChallengeType string
	LEListen      string
	LECacheDir    string
	CertPath      string
	KeyPath       string

	Cert CertInfo
	Renew RenewInfo

	Service    string
	ConfigPath string
	Warnings   []string
	Errors     map[string]string
}

// CertInfo 描述当前实际加载的证书状态。
type CertInfo struct {
	Source   string
	Path     string
	KeyPath  string
	Subject  string
	Issuer   string
	NotBefore string
	NotAfter string
	DaysLeft int
	SAN      string
	KeyMatch bool
	Valid    bool
	Problem  string
}

// RenewInfo 描述证书自动续期通道状态。
type RenewInfo struct {
	Mode             string // acme | certbot | manual | none
	CertbotInstalled bool
	TimerEnabled     string
	TimerActive      string
	NextRun          string
	LastRun          string
	CertName         string
	Schedule         string
	Note             string
}

func (c *HSClient) loadConfigPage(lang string) *ConfigPage {
	p := &ConfigPage{Errors: map[string]string{}, Service: tr(lang, "state.unknown"), ConfigPath: c.Config, LECacheDir: "/var/lib/headscale/cache", ChallengeType: "HTTP-01", LEListen: ":http"}
	if out, err := systemctl("is-active", "headscale"); err == nil {
		p.Service = strings.TrimSpace(out)
	}
	content, err := c.ReadConfigText()
	if err != nil {
		p.Warnings = append(p.Warnings, tr(lang, "cfg.warn.readConfig", err.Error()))
		return p
	}
	p.ServerURL = topValue(content, "server_url")
	p.BaseDomain = nestedValue(content, "dns", "base_domain")
	p.MagicDNS = strings.EqualFold(nestedValue(content, "dns", "magic_dns"), "true")
	p.ListenAddr = topValue(content, "listen_addr")
	p.MetricsListenAddr = topValue(content, "metrics_listen_addr")
	p.GRPCListenAddr = topValue(content, "grpc_listen_addr")
	p.PrefixV4 = nestedValue(content, "prefixes", "v4")
	p.PrefixV6 = nestedValue(content, "prefixes", "v6")
	p.LEHostname = topValue(content, "tls_letsencrypt_hostname")
	p.ACMEEmail = topValue(content, "acme_email")
	p.ACMEURL = topValue(content, "acme_url")
	p.CertPath = topValue(content, "tls_cert_path")
	p.KeyPath = topValue(content, "tls_key_path")
	if v := topValue(content, "tls_letsencrypt_challenge_type"); v != "" {
		p.ChallengeType = v
	}
	if v := topValue(content, "tls_letsencrypt_listen"); v != "" {
		p.LEListen = v
	}
	if v := topValue(content, "tls_letsencrypt_cache_dir"); v != "" {
		p.LECacheDir = v
	}
	switch {
	case p.LEHostname != "":
		p.TLSMode = "letsencrypt"
	case p.CertPath != "" && p.KeyPath != "":
		p.TLSMode = "manual"
	default:
		p.TLSMode = "http"
	}
	p.Cert = inspectCert(p)
	p.Renew = inspectRenew(p)
	p.Warnings = append(p.Warnings, configWarnings(p, lang)...)
	return p
}

// ---------- 证书与续期状态 ----------

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func globFirst(pattern string) string {
	m, _ := filepath.Glob(pattern)
	for _, p := range m {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func certCandidatePaths(p *ConfigPage) (cert, key, source string) {
	if p.CertPath != "" {
		return p.CertPath, p.KeyPath, "manual"
	}
	if p.LEHostname != "" {
		dir := p.LECacheDir
		if dir == "" {
			dir = "/var/lib/headscale/cache"
		}
		c := firstExisting(filepath.Join(dir, p.LEHostname+".crt"), filepath.Join(dir, p.LEHostname+".pem"))
		if c == "" {
			c = globFirst(filepath.Join(dir, "*.crt"))
		}
		if c == "" {
			c = globFirst(filepath.Join(dir, "*", p.LEHostname+".crt"))
		}
		k := strings.TrimSuffix(strings.TrimSuffix(c, ".crt"), ".pem") + ".key"
		return c, k, "acme"
	}
	return "", "", "none"
}

func inspectCert(p *ConfigPage) CertInfo {
	certPath, keyPath, source := certCandidatePaths(p)
	info := CertInfo{Source: source, Path: certPath, KeyPath: keyPath}
	// 手工证书模式下，即使文件有问题也要显示路径，便于排查。
	if certPath == "" {
		if source == "manual" {
			info.Problem = "cert-path-empty"
		}
		return info
	}
	raw, err := os.ReadFile(certPath)
	if err != nil {
		info.Problem = "unreadable"
		return info
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		info.Problem = "not-pem"
		return info
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		info.Problem = "parse"
		return info
	}
	info.Valid = true
	info.Subject = cert.Subject.CommonName
	info.NotBefore = cert.NotBefore.Format("2006-01-02 15:04")
	info.NotAfter = cert.NotAfter.Format("2006-01-02 15:04")
	info.DaysLeft = int(time.Until(cert.NotAfter).Hours() / 24)
	var names []string
	for _, d := range cert.DNSNames {
		names = append(names, d)
	}
	for _, ip := range cert.IPAddresses {
		names = append(names, ip.String())
	}
	info.SAN = strings.Join(names, ", ")
	issuer := cert.Issuer.CommonName
	if issuer == "" {
		issuer = cert.Issuer.Organization[0]
	}
	info.Issuer = issuer
	if keyPath != "" {
		if _, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
			info.KeyMatch = true
		}
	}
	return info
}

func certbotBinary() string {
	for _, p := range []string{"/usr/bin/certbot", "/usr/local/bin/certbot", "/snap/bin/certbot"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func systemdShow(unit string, props ...string) map[string]string {
	out := map[string]string{}
	args := append([]string{"show", unit, "-p", strings.Join(props, ",")}, "--no-pager")
	b, err := exec.Command("systemctl", args...).Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func inspectRenew(p *ConfigPage) RenewInfo {
	r := RenewInfo{Mode: "none", CertbotInstalled: certbotBinary() != "", Schedule: "03:17"}
	if p.TLSMode == "letsencrypt" {
		r.Mode = "acme"
		r.Note = "acme"
	}
	if p.TLSMode == "manual" {
		r.Mode = "manual"
		r.Note = "manual"
	}
	if !r.CertbotInstalled {
		if r.Mode == "acme" {
			r.Note = "acme"
		}
		return r
	}
	// 有 certbot 时，续期由 certbot.timer 负责；ACME 内置签发也能被 certbot 接管不存在的情况忽略。
	if r.Mode == "letsencrypt" || r.Mode == "" {
		r.Mode = "certbot"
	}
	props := systemdShow("certbot.timer", "LoadState", "ActiveState", "UnitFileState", "NextElapseUSecRealtime", "LastTriggerUSec")
	r.TimerEnabled = props["UnitFileState"]
	r.TimerActive = props["ActiveState"]
	r.NextRun = humanSystemdTime(props["NextElapseUSecRealtime"])
	r.LastRun = humanSystemdTime(props["LastTriggerUSec"])
	if p.CertPath != "" {
		r.CertName = filepath.Base(filepath.Dir(filepath.Dir(p.CertPath)))
	}
	if r.CertName == "" || r.CertName == "." || r.CertName == "/" {
		if p.LEHostname != "" {
			r.CertName = p.LEHostname
		}
	}
	if r.CertName == "" {
		if m, _ := filepath.Glob("/etc/letsencrypt/live/*"); len(m) > 0 {
			r.CertName = filepath.Base(m[0])
		}
	}
	if ov := "/etc/systemd/system/certbot.timer.d/override.conf"; fileExists(ov) {
		if b, err := os.ReadFile(ov); err == nil {
			if m := regexp.MustCompile(`OnCalendar=\*-\*-\*\s+(\d{2}:\d{2})`).FindStringSubmatch(string(b)); len(m) > 1 {
				r.Schedule = m[1]
			}
		}
	}
	return r
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func humanSystemdTime(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "n/a" || strings.HasPrefix(v, "0") {
		return "—"
	}
	for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 MST"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Format("2006-01-02 15:04")
		}
	}
	return v
}

// ---------- 校验 ----------

var fqdnRE = regexp.MustCompile(`^(?i)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func validFQDN(s string) bool {
	if len(s) == 0 || len(s) > 253 || strings.HasSuffix(s, ".") {
		return false
	}
	return fqdnRE.MatchString(s)
}

var hostLabelRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// validBaseDomain 比 FQDN 宽松：headscale 允许以数字段结尾的根域（如 hs.192.168.100.60）。
func validBaseDomain(s string) bool {
	if s == "" || len(s) > 253 || strings.HasSuffix(s, ".") || strings.HasPrefix(s, ".") {
		return false
	}
	if net.ParseIP(s) != nil {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if !hostLabelRE.MatchString(part) {
			return false
		}
	}
	return true
}

func validHostPort(s string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, errors.New("format")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("port")
	}
	if host != "" && net.ParseIP(host) == nil {
		return "", 0, errors.New("host")
	}
	return host, port, nil
}

func urlPort(u *url.URL, defaultPort int) int {
	if p := u.Port(); p != "" {
		n, _ := strconv.Atoi(p)
		return n
	}
	return defaultPort
}

func validateURL(raw string) (*url.URL, string) {
	if raw == "" {
		return nil, "cfg.err.urlRequired"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, "cfg.err.urlFormat"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, "cfg.err.urlScheme"
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, "cfg.err.urlPath"
	}
	if strings.HasSuffix(raw, "/") {
		return nil, "cfg.err.urlTrailing"
	}
	return u, ""
}

// validateDomain 校验域名与接入段。
func validateDomain(p *ConfigPage, lang string) map[string]string {
	e := map[string]string{}
	u, code := validateURL(p.ServerURL)
	if code != "" {
		e["server_url"] = tr(lang, code)
	}
	if p.BaseDomain == "" {
		e["base_domain"] = tr(lang, "cfg.err.baseRequired")
	} else if !validBaseDomain(p.BaseDomain) {
		e["base_domain"] = tr(lang, "cfg.err.fqdn")
	} else if u != nil && strings.EqualFold(p.BaseDomain, u.Hostname()) {
		e["base_domain"] = tr(lang, "cfg.err.baseSame")
	}
	if u != nil {
		scheme := u.Scheme
		if p.TLSMode == "http" && scheme == "https" {
			e["server_url"] = tr(lang, "cfg.err.httpsNoCert")
		}
		if p.TLSMode != "http" && scheme == "http" {
			e["server_url"] = tr(lang, "cfg.err.httpWithCert")
		}
	}
	return e
}

func validateNetwork(p *ConfigPage, lang string) map[string]string {
	e := map[string]string{}
	for _, f := range []struct {
		key   string
		value string
		opt   bool
	}{{"listen_addr", p.ListenAddr, false}, {"grpc_listen_addr", p.GRPCListenAddr, false}, {"metrics_listen_addr", p.MetricsListenAddr, true}} {
		if f.value == "" {
			if !f.opt {
				e[f.key] = tr(lang, "cfg.err.addrRequired")
			}
			continue
		}
		if _, _, err := validHostPort(f.value); err != nil {
			switch err.Error() {
			case "port":
				e[f.key] = tr(lang, "cfg.err.addrPort")
			default:
				e[f.key] = tr(lang, "cfg.err.addrFormat")
			}
		}
	}
	// 端口冲突
	ports := map[int]string{}
	for _, f := range []struct{ key, value string }{{"listen_addr", p.ListenAddr}, {"grpc_listen_addr", p.GRPCListenAddr}, {"metrics_listen_addr", p.MetricsListenAddr}} {
		if _, port, err := validHostPort(f.value); err == nil {
			if other, ok := ports[port]; ok {
				e[f.key] = tr(lang, "cfg.err.portConflict", other)
			} else {
				ports[port] = f.key
			}
		}
	}
	if p.PrefixV4 == "" {
		e["prefix_v4"] = tr(lang, "cfg.err.prefixRequired")
	} else if _, n, err := net.ParseCIDR(p.PrefixV4); err != nil || n.IP.To4() == nil {
		e["prefix_v4"] = tr(lang, "cfg.err.prefixV4")
	} else {
		_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
		if !cgnat.Contains(n.IP) {
			e["prefix_v4"] = tr(lang, "cfg.err.prefixRange")
		}
	}
	if p.PrefixV6 != "" {
		_, n, err := net.ParseCIDR(p.PrefixV6)
		if err != nil || n.IP.To4() != nil {
			e["prefix_v6"] = tr(lang, "cfg.err.prefixV6")
		} else if ones, _ := n.Mask.Size(); ones > 64 {
			e["prefix_v6"] = tr(lang, "cfg.err.prefixV6Len")
		}
	}
	return e
}

func validateTLS(p *ConfigPage, lang string) map[string]string {
	e := map[string]string{}
	switch p.TLSMode {
	case "letsencrypt":
		if p.LEHostname == "" {
			e["tls_letsencrypt_hostname"] = tr(lang, "cfg.err.leHostRequired")
		} else if !validFQDN(p.LEHostname) {
			e["tls_letsencrypt_hostname"] = tr(lang, "cfg.err.fqdn")
		} else if ip := net.ParseIP(p.LEHostname); ip != nil {
			e["tls_letsencrypt_hostname"] = tr(lang, "cfg.err.fqdn")
		}
		if p.ACMEEmail != "" {
			if _, err := mail.ParseAddress(p.ACMEEmail); err != nil || !strings.Contains(p.ACMEEmail, ".") {
				e["acme_email"] = tr(lang, "cfg.err.email")
			}
		}
		if p.ChallengeType != "HTTP-01" && p.ChallengeType != "TLS-ALPN-01" {
			e["tls_letsencrypt_challenge_type"] = tr(lang, "cfg.err.challenge")
		}
		if p.LEListen == "" {
			e["tls_letsencrypt_listen"] = tr(lang, "cfg.err.addrRequired")
		}
	case "manual":
		if p.CertPath == "" {
			e["tls_cert_path"] = tr(lang, "cfg.err.certRequired")
		} else if !filepath.IsAbs(p.CertPath) {
			e["tls_cert_path"] = tr(lang, "cfg.err.pathAbs")
		} else if !fileExists(p.CertPath) {
			e["tls_cert_path"] = tr(lang, "cfg.err.certMissing")
		}
		if p.KeyPath == "" {
			e["tls_key_path"] = tr(lang, "cfg.err.keyRequired")
		} else if !filepath.IsAbs(p.KeyPath) {
			e["tls_key_path"] = tr(lang, "cfg.err.pathAbs")
		} else if !fileExists(p.KeyPath) {
			e["tls_key_path"] = tr(lang, "cfg.err.keyMissing")
		}
		if _, ok := e["tls_cert_path"]; !ok {
			if _, ok := e["tls_key_path"]; !ok {
				if _, err := tls.LoadX509KeyPair(p.CertPath, p.KeyPath); err != nil {
					e["tls_cert_path"] = tr(lang, "cfg.err.pairMismatch")
				}
			}
		}
	case "http":
	default:
		e["tls_mode"] = tr(lang, "cfg.err.tlsMode")
	}
	return e
}

func configWarnings(p *ConfigPage, lang string) []string {
	var w []string
	if p.Cert.Valid && p.Cert.DaysLeft <= 30 {
		w = append(w, tr(lang, "cfg.warn.certExpiring", p.Cert.DaysLeft))
	}
	if p.Cert.Valid && !p.Cert.KeyMatch {
		w = append(w, tr(lang, "cfg.warn.keyMismatch"))
	}
	if p.TLSMode == "letsencrypt" && p.LEHostname != "" {
		if u, code := validateURL(p.ServerURL); code == "" && !strings.EqualFold(u.Hostname(), p.LEHostname) {
			w = append(w, tr(lang, "cfg.warn.leHostDiffers", u.Hostname(), p.LEHostname))
		}
	}
	if p.Renew.Mode == "acme" {
		w = append(w, tr(lang, "cfg.warn.acmeBuiltin"))
	}
	if u, code := validateURL(p.ServerURL); code == "" {
		if p.ListenAddr != "" {
			if _, port, err := validHostPort(p.ListenAddr); err == nil {
				if up := urlPort(u, map[string]int{"http": 80, "https": 443}[u.Scheme]); up != port {
					w = append(w, tr(lang, "cfg.warn.portMismatch", up, port))
				}
			}
		}
		if p.TLSMode == "http" && u.Scheme == "https" {
			w = append(w, tr(lang, "cfg.warn.schemeMismatch", "https", "HTTP"))
		}
		if p.TLSMode != "http" && u.Scheme == "http" {
			w = append(w, tr(lang, "cfg.warn.schemeMismatch", "http", p.TLSMode))
		}
	}
	return w
}

// ---------- 写配置与应用 ----------

func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, "#&*!|>'\"%@`") || strings.Contains(s, ": ") || strings.HasSuffix(s, ":") ||
		strings.HasPrefix(s, "-") || strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") {
		return strconv.Quote(s)
	}
	return s
}

func setTopKey(content, key, value string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:.*$`)
	if re.MatchString(content) {
		return re.ReplaceAllString(content, key+": "+value)
	}
	return key + ": " + value + "\n" + content
}

func setNestedKey(content, block, key, value string) (string, error) {
	re := regexp.MustCompile(`(?ms)(^` + regexp.QuoteMeta(block) + `:[ \t]*\n)((?:[ \t]+.*\n?)*)`)
	loc := re.FindStringSubmatchIndex(content)
	if loc == nil {
		return content, fmt.Errorf("配置中缺少 %s 块", block)
	}
	inner := content[loc[4]:loc[5]]
	line := "  " + key + ": " + value
	kr := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(key) + `:.*$`)
	if kr.MatchString(inner) {
		inner = kr.ReplaceAllString(inner, line)
	} else {
		inner = line + "\n" + inner
	}
	return content[:loc[4]] + inner + content[loc[5]:], nil
}

// buildConfig 基于当前文件内容生成本次提交的完整配置文本。
func (c *HSClient) buildConfig(p *ConfigPage, section string) (string, error) {
	b, err := c.ReadConfigText()
	if err != nil {
		return "", err
	}
	next := b
	switch section {
	case "domain":
		next = setTopKey(next, "server_url", yamlScalar(p.ServerURL))
		magic := "false"
		if p.MagicDNS {
			magic = "true"
		}
		if next, err = setNestedKey(next, "dns", "base_domain", yamlScalar(p.BaseDomain)); err != nil {
			return "", err
		}
		if next, err = setNestedKey(next, "dns", "magic_dns", magic); err != nil {
			return "", err
		}
	case "network":
		next = setTopKey(next, "listen_addr", yamlScalar(p.ListenAddr))
		next = setTopKey(next, "metrics_listen_addr", yamlScalar(p.MetricsListenAddr))
		next = setTopKey(next, "grpc_listen_addr", yamlScalar(p.GRPCListenAddr))
		if next, err = setNestedKey(next, "prefixes", "v4", yamlScalar(p.PrefixV4)); err != nil {
			return "", err
		}
		if p.PrefixV6 != "" {
			if next, err = setNestedKey(next, "prefixes", "v6", yamlScalar(p.PrefixV6)); err != nil {
				return "", err
			}
		}
	case "tls":
		next = setTopKey(next, "acme_email", yamlScalar(p.ACMEEmail))
		if p.ACMEURL != "" {
			next = setTopKey(next, "acme_url", yamlScalar(p.ACMEURL))
		}
		next = setTopKey(next, "tls_letsencrypt_hostname", yamlScalar(p.LEHostname))
		next = setTopKey(next, "tls_letsencrypt_challenge_type", yamlScalar(p.ChallengeType))
		next = setTopKey(next, "tls_letsencrypt_listen", yamlScalar(p.LEListen))
		next = setTopKey(next, "tls_letsencrypt_cache_dir", yamlScalar(p.LECacheDir))
		next = setTopKey(next, "tls_cert_path", yamlScalar(p.CertPath))
		next = setTopKey(next, "tls_key_path", yamlScalar(p.KeyPath))
	}
	return next, nil
}

// ApplyConfig 采用「临时文件校验 → 备份 → 原子替换 → 重启 → 失败回滚」的流程。
func (c *HSClient) ApplyConfig(next string, restart bool, lang string) (string, error) {
	dir := filepath.Dir(c.Config)
	candidate := filepath.Join(dir, ".config-candidate.yaml")
	if err := os.WriteFile(candidate, []byte(next), 0600); err != nil {
		return "", errors.New(tr(lang, "cfg.err.writeCandidate", err.Error()))
	}
	defer os.Remove(candidate)

	out, err := exec.Command(c.Bin, "configtest", "-c", candidate).CombinedOutput()
	ctOut := strings.TrimSpace(ansiRE.ReplaceAllString(string(out), ""))
	if err != nil {
		// configtest 会真实初始化，可能留下 root 属主的运行文件。
		_ = exec.Command("chown", "-R", "headscale:headscale", "/var/lib/headscale").Run()
		return "", errors.New(tr(lang, "cfg.err.configtest", ctOut))
	}
	_ = exec.Command("chown", "-R", "headscale:headscale", "/var/lib/headscale").Run()

	old, err := os.ReadFile(c.Config)
	if err != nil {
		return "", err
	}
	bak := c.Config + ".bak." + time.Now().Format("20060102-150405")
	if err := os.WriteFile(bak, old, 0600); err != nil {
		return "", errors.New(tr(lang, "cfg.err.backup", err.Error()))
	}
	if err := os.Rename(candidate, c.Config); err != nil {
		return "", errors.New(tr(lang, "cfg.err.writeConfig", err.Error()))
	}
	_ = c.chownConfig()
	if !restart {
		return bak, nil
	}
	if msg, ok := restartHeadscale(); !ok {
		// 回滚
		_ = os.WriteFile(c.Config, old, 0600)
		_ = c.chownConfig()
		_, _ = restartHeadscale()
		return bak, errors.New(tr(lang, "cfg.err.restartRolledBack", msg))
	}
	return bak, nil
}

// probeTarget 从配置的 listen_addr 推导本机可探测的 TCP 地址。
func probeTarget() string {
	conf := os.Getenv("HSA_CONFIG_FILE")
	if conf == "" {
		conf = "/etc/headscale/config.yaml"
	}
	b, err := os.ReadFile(conf)
	if err != nil {
		return ""
	}
	addr := topValue(string(b), "listen_addr")
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// waitListening 等到进程 active 且端口真正可连接，避免"已重启"但尚未就绪的误报。
func waitListening(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		active := false
		if out, err := systemctl("is-active", "headscale"); err == nil && strings.TrimSpace(out) == "active" {
			active = true
		}
		if active {
			if addr == "" {
				return true
			}
			conn, err := net.DialTimeout("tcp", addr, 800*time.Millisecond)
			if err == nil {
				conn.Close()
				return true
			}
		}
		time.Sleep(700 * time.Millisecond)
	}
	return false
}

func restartHeadscale() (string, bool) {
	if out, err := systemctl("restart", "headscale"); err != nil {
		return strings.TrimSpace(out + " " + err.Error()), false
	}
	if waitListening(probeTarget(), 30*time.Second) {
		return "", true
	}
	out, _ := systemctl("status", "headscale", "--no-pager", "-n", "12")
	return strings.TrimSpace(out), false
}

// certRenew 触发 certbot 续期，成功后续期并重启 headscale 让新证书生效。
func certRenew(lang string) (string, error) {
	bin := certbotBinary()
	if bin == "" {
		return "", errors.New(tr(lang, "cfg.err.noCertbot"))
	}
	out, err := exec.Command(bin, "renew", "--non-interactive").CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, errors.New(tr(lang, "cfg.err.renewFail", text))
	}
	if msg, ok := restartHeadscale(); !ok {
		return text, errors.New(tr(lang, "cfg.err.renewRestart", msg))
	}
	return text, nil
}

// setRenewTimer 启停 certbot.timer，并可选写入每日检查时间。
func setRenewTimer(enable bool, schedule string, lang string) error {
	if certbotBinary() == "" {
		return errors.New(tr(lang, "cfg.err.noCertbot"))
	}
	if enable && schedule != "" {
		if !regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`).MatchString(schedule) {
			return errors.New(tr(lang, "cfg.err.schedule"))
		}
		dir := "/etc/systemd/system/certbot.timer.d"
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		conf := "[Timer]\nOnCalendar=\nOnCalendar=*-*-* " + schedule + ":00\nRandomizedDelaySec=1800\nPersistent=true\n"
		if err := os.WriteFile(filepath.Join(dir, "override.conf"), []byte(conf), 0644); err != nil {
			return err
		}
		if _, err := systemctl("daemon-reload"); err != nil {
			return err
		}
	}
	action := "disable"
	if enable {
		action = "enable"
	}
	if out, err := systemctl(action, "--now", "certbot.timer"); err != nil {
		return errors.New(strings.TrimSpace(out + " " + err.Error()))
	}
	return nil
}
