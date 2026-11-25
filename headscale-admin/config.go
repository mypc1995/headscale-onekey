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
	CertPath      string // 写入 headscale 配置的 tls_cert_path
	KeyPath       string // 写入 headscale 配置的 tls_key_path

	// 手动证书：用户直接粘贴的 PEM 文本，仅在本次请求内使用，不回显、不落库
	CertPEM string
	KeyPEM  string

	Cert  CertInfo
	Renew RenewInfo

	// 管理界面自身 HTTPS（与 headscale 证书独立）
	AdminTLS       adminTLSConfig
	AdminTLSCert   string // 当前证书描述（CN + 到期时间）
	AdminHTTPSLive bool   // 当前进程是否正以 HTTPS 运行

	// 节点网速上报通道（POST /api/speed）：地址、令牌与启用状态
	SpeedEndpoint string
	SpeedToken    string // 只对 admin 角色下发（readonly 看不到）
	SpeedEnabled  bool
	SpeedChannel  string // 当前生效的数据来源：api（节点上报）或 file（计数器文件）

	Service    string
	ConfigPath string
	Warnings   []string
	Errors     map[string]string
}

// CertInfo 描述当前实际加载的证书状态。
type CertInfo struct {
	Source    string
	Path      string
	KeyPath   string
	Subject   string
	Issuer    string
	NotBefore string
	NotAfter  string
	DaysLeft  int
	SAN       string
	KeyMatch  bool
	Valid     bool
	Problem   string
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
	if p.Cert.Problem != "" {
		p.Cert.Problem = certProblemText(p.Cert.Problem, lang)
	}
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

// certProblemText 把内部证书异常码翻成用户能看懂的说明。
func certProblemText(code, lang string) string {
	switch code {
	case "cert-path-empty":
		return tr(lang, "cfg.problem.empty")
	case "unreadable":
		return tr(lang, "cfg.problem.unreadable")
	case "not-pem":
		return tr(lang, "cfg.problem.notPEM")
	case "parse":
		return tr(lang, "cfg.problem.parse")
	}
	return code
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

// validBaseDomain 比 FQDN 宽松：headscale 允许以数字段结尾的根域（如 hs.192.0.2.60）。
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
	}
	// 网段不写死 100.64.0.0/10：部分云厂商（如阿里云 ECS）自身占用该网段，
	// 合法性交给 headscale configtest 在保存流程中兜底校验。
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
		if p.CertPEM == "" && p.KeyPEM == "" {
			// 没有重新粘贴：沿用服务器上已有的证书文件，但必须真实可用。
			if p.CertPath == "" || p.KeyPath == "" {
				e["cert_pem"] = tr(lang, "cfg.err.certRequired")
			} else if !fileExists(p.CertPath) || !fileExists(p.KeyPath) {
				e["cert_pem"] = tr(lang, "cfg.err.certStale")
			} else if _, err := tls.LoadX509KeyPair(p.CertPath, p.KeyPath); err != nil {
				e["cert_pem"] = tr(lang, "cfg.err.pairMismatch")
			}
		} else if field, code := checkPastedPair(p.CertPEM, p.KeyPEM); code != "" {
			e[field] = tr(lang, code)
		}
	case "http":
	default:
		e["tls_mode"] = tr(lang, "cfg.err.tlsMode")
	}
	return e
}

// ---------- 手动粘贴证书 ----------

// manualCertDir 是手动粘贴证书的固定落盘位置，headscale 进程要能读到。
const manualCertDir = "/etc/headscale/certs"

// checkPastedPair 校验粘贴的证书与私钥文本，返回出错字段与 i18n 键；通过时两者都为空。
func checkPastedPair(certPEM, keyPEM string) (field, code string) {
	if certPEM == "" || keyPEM == "" {
		return "cert_pem", "cfg.err.pasteBoth"
	}
	cb, _ := pem.Decode([]byte(certPEM))
	if cb == nil || cb.Type != "CERTIFICATE" {
		return "cert_pem", "cfg.err.certPEM"
	}
	if _, err := x509.ParseCertificate(cb.Bytes); err != nil {
		return "cert_pem", "cfg.err.certParse"
	}
	kb, _ := pem.Decode([]byte(keyPEM))
	if kb == nil {
		return "key_pem", "cfg.err.keyPEM"
	}
	if !parsesPrivateKey(kb.Bytes) {
		return "key_pem", "cfg.err.keyParse"
	}
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		return "cert_pem", "cfg.err.pairMismatch"
	}
	if field, code := checkCertChain(certPEM); field != "" {
		return field, code
	}
	return "", ""
}

// parseCertChain 按出现顺序解析 PEM 里的全部证书。
func parseCertChain(certPEM string) []*x509.Certificate {
	out := []*x509.Certificate{}
	rest := []byte(certPEM)
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	return out
}

// checkCertChain 校验粘贴的是不是完整证书链。
//
// 只粘叶子证书（漏掉中间 CA）时，服务端会只下发叶子：浏览器能自己补中间证书，
// 但 Tailscale 客户端（Go 写的 tailscaled 不做 AIA 自动补链）会直接报
// x509: certificate signed by unknown authority，连控制服务都上不去，
// 表现为「连不上内置 DERP 中继」这类完全看不出原因的健康告警。
//
// 链上**每一对相邻证书**都要验签名，不能只查前两张：多张链里第 3 张及以后
// 贴错（顺序反了、贴了不相关的证书、少贴一张中间 CA）同样会让一部分客户端
// 建不出链，而症状依旧表现为「只有某些设备连不上」，很难往证书上想。
//
// 自签证书（mkcert 之类）本来就只有一张，不算缺链，放行。
func checkCertChain(certPEM string) (field, code string) {
	certs := parseCertChain(certPEM)
	if len(certs) == 0 {
		return "", "" // 解析失败由前面的校验报错
	}
	leaf := certs[0]
	if len(certs) == 1 && leaf.Issuer.String() != leaf.Subject.String() {
		return "cert_pem", "cfg.err.certChainIncomplete"
	}
	for i := 0; i+1 < len(certs); i++ {
		// 不只看名字对不对，还要验签名：同名但不同签发者的中间证书
		// 名字是能对上的，签名却验不过。
		if certs[i].Issuer.String() != certs[i+1].Subject.String() || certs[i].CheckSignatureFrom(certs[i+1]) != nil {
			return "cert_pem", "cfg.err.certChainBroken"
		}
	}
	return "", ""
}

func parsesPrivateKey(der []byte) bool {
	if _, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return true
	}
	if _, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return true
	}
	if _, err := x509.ParseECPrivateKey(der); err == nil {
		return true
	}
	return false
}

// saveManualCert 把粘贴的证书文本写入 manualCertDir，返回写入后的路径与回滚函数。
// 配置最终没能应用时，调用方用回滚函数还原旧文件。
func saveManualCert(certPEM, keyPEM string) (certPath, keyPath string, rollback func(), err error) {
	if err := os.MkdirAll(manualCertDir, 0755); err != nil {
		return "", "", func() {}, err
	}
	certPath = filepath.Join(manualCertDir, "manual.crt")
	keyPath = filepath.Join(manualCertDir, "manual.key")
	rollback, err = writeCertFiles(map[string]string{certPath: certPEM, keyPath: keyPEM})
	if err != nil {
		return "", "", func() {}, err
	}
	return certPath, keyPath, rollback, nil
}

// writeCertFiles 以 0600 写入 PEM 文件并 chown 给 headscale，返回可还原旧文件的回滚函数。
func writeCertFiles(files map[string]string) (func(), error) {
	type prevFile struct {
		path string
		data []byte
		mode os.FileMode
		ok   bool
	}
	var prev []prevFile
	restore := func() {
		for _, f := range prev {
			if f.ok {
				_ = os.WriteFile(f.path, f.data, f.mode)
			} else {
				_ = os.Remove(f.path)
			}
		}
	}
	for path, body := range files {
		f := prevFile{path: path}
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			if b, err := os.ReadFile(path); err == nil {
				f.data, f.mode, f.ok = b, st.Mode().Perm(), true
			}
		}
		prev = append(prev, f)
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			restore()
			return func() {}, err
		}
	}
	// headscale 以 headscale 用户运行，证书必须让它可读（失败时保留 root 属主，仅影响排查提示）。
	_ = exec.Command("chown", "-R", "headscale:headscale", manualCertDir).Run()
	return restore, nil
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
			w = append(w, tr(lang, "cfg.warn.schemeMismatch", "https", tr(lang, "cfg.mode.http")))
		}
		if p.TLSMode != "http" && u.Scheme == "http" {
			modeLabel := tr(lang, "cfg.mode.manual")
			if p.TLSMode == "letsencrypt" {
				modeLabel = tr(lang, "cfg.mode.le")
			}
			w = append(w, tr(lang, "cfg.warn.schemeMismatch", "http", modeLabel))
		}
	}
	return w
}

// ---------- 写配置与应用 ----------

func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	// 含换行或控制字符的值必须整体加引号。否则它会跳出当前所在的 YAML 块，
	// 后面的行会被当成新的顶层键——这就是 YAML 结构注入。
	if hasControlChar(s) {
		return strconv.Quote(s)
	}
	if strings.ContainsAny(s, "#&*!|>'\"%@`") || strings.Contains(s, ": ") || strings.HasSuffix(s, ":") ||
		strings.HasPrefix(s, "-") || strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") {
		return strconv.Quote(s)
	}
	return s
}

// hasControlChar 判断字符串里有没有换行或其它控制字符。
func hasControlChar(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func setTopKey(content, key, value string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:.*$`)
	if re.MatchString(content) {
		// 必须用字面量替换：ReplaceAllString 会把替换串当模板展开，
		// 用户值里的 "$1"、"$name" 会被静默删掉，配置内容就这么丢了。
		return re.ReplaceAllLiteralString(content, key+": "+value)
	}
	return key + ": " + value + "\n" + content
}

// blockPattern 返回定位 YAML 嵌套块的正则。
//
// 块的范围 = 块头之后连续的空行与缩进行，直到第一个既非空行、也不缩进的行。
// 这正是 YAML 里块映射的边界规则：块内的空行和注释不会结束块。
//
// 这里绝对不能加 (?s)：那会让 . 匹配换行，.* 一路贪婪吞到文件末尾。后果是
// 「块内新增键」被追加到**文件末尾**（变成顶层键，根本没写进块里），
// 「块内删键」也可能误伤文件后面别的块里的同名键。
func blockPattern(block string) string {
	return `(?m)(^` + regexp.QuoteMeta(block) + `:[ \t]*\n)((?:(?:[ \t][^\n]*|[ \t]*)(?:\n|\z))*)`
}

// blockKeyRe 匹配块内位于首层缩进（两格）的键，注释行不算。
var blockKeyRe = regexp.MustCompile(`^  [^ #\s][^:]*:`)

// insertPosInBlock 返回块内容里插入新键的位置（相对块内容的字节偏移）。
//
// 取「块头后第一段连续键行」的最后一行：块内若有空行，后面的注释属于另一段
// 说明文字，把新键插到那段后面，看起来就像新键属于那段注释。
// 第一段里一个键都没有时，插到块内容最前面（即紧跟块头）。
func insertPosInBlock(inner string) int {
	pos, off := 0, 0
	rest := inner
	for len(rest) > 0 {
		var line string
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i+1], rest[i+1:]
		} else {
			line, rest = rest, ""
		}
		if strings.TrimSpace(line) == "" {
			break // 空行结束第一段，后面的键不再作为插入位置
		}
		off += len(line)
		if blockKeyRe.MatchString(line) {
			pos = off
		}
	}
	return pos
}

func setNestedKey(content, block, key, value string) (string, error) {
	re := regexp.MustCompile(blockPattern(block))
	loc := re.FindStringSubmatchIndex(content)
	if loc == nil {
		return content, fmt.Errorf("配置中缺少 %s 块", block)
	}
	inner := content[loc[4]:loc[5]]
	line := "  " + key + ": " + value
	kr := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(key) + `:.*$`)
	if kr.MatchString(inner) {
		// 同 setTopKey：字面量替换，别让 "$" 被当成模板展开。
		inner = kr.ReplaceAllLiteralString(inner, line)
	} else {
		// 块里还没有这个键：插到块内第一段键行的末尾。
		// 必须落在块里：插到文件末尾会写出一个顶层键，headscale 根本不认，
		// 而 configtest 还会因为容忍未知键而放行，看着像成功了其实没生效。
		at := insertPosInBlock(inner)
		sep := ""
		if at > 0 && inner[at-1] != '\n' {
			sep = "\n" // 块的最后一行没有换行符时，先补一个，别把两行粘一起
		}
		inner = inner[:at] + sep + line + "\n" + inner[at:]
	}
	return content[:loc[4]] + inner + content[loc[5]:], nil
}

// deleteNestedKey 从嵌套块里删掉某个键。块或键不存在时原样返回。
//
// 「IPv6 开关」要用它：关掉开关时如果只是不写，配置里原来的 prefixes.v6
// 会原样留着，headscale 仍然按 IPv6 网段分配地址，这个开关等于没开。
func deleteNestedKey(content, block, key string) string {
	re := regexp.MustCompile(blockPattern(block))
	loc := re.FindStringSubmatchIndex(content)
	if loc == nil {
		return content
	}
	inner := content[loc[4]:loc[5]]
	kr := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(key) + `:[^\n]*\n?`)
	if !kr.MatchString(inner) {
		return content
	}
	return content[:loc[4]] + kr.ReplaceAllLiteralString(inner, "") + content[loc[5]:]
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
		} else {
			// 开关关掉要把 prefixes.v6 从配置里删掉，而不是留着旧值：
			// 留着的话 headscale 仍然按 IPv6 网段分配地址，这个开关等于没开。
			next = deleteNestedKey(next, "prefixes", "v6")
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
	// 行尾统一：调用方可能直接把浏览器 textarea 的内容递进来（CRLF），
	// 也可能已经是 LF。落盘前统一成 LF，免得候选文件里混着 CRLF。
	next = normalizeEOL(next)
	dir := filepath.Dir(c.Config)
	candidate := filepath.Join(dir, ".config-candidate.yaml")
	if err := os.WriteFile(candidate, []byte(next), 0600); err != nil {
		return "", errors.New(tr(lang, "cfg.err.writeCandidate", err.Error()))
	}
	defer os.Remove(candidate)

	out, err := exec.Command(c.Bin, "configtest", "-c", candidate).CombinedOutput()
	ctOut := strings.TrimSpace(safeText(ansiRE.ReplaceAllString(string(out), "")))
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

// ---- 缩进式 YAML 列表读写（用于 dns.nameservers.global / dns.search_domains 等）----
// 只按缩进定位，不做完整 YAML 解析；保存后由 headscale configtest 兜底校验。

// findBlockLine 在 [from,to) 行范围内找缩进为 indent 的 `key:` 行，返回行号与块结束行（半开）。
func blockLine(lines []string, from, to, indent int, key string) (int, int) {
	prefix := strings.Repeat(" ", indent)
	keyRe := regexp.MustCompile(`^` + prefix + regexp.QuoteMeta(key) + `:\s*(?:#.*)?$`)
	for i := from; i < to; i++ {
		if keyRe.MatchString(lines[i]) {
			end := to
			for j := i + 1; j < to; j++ {
				ln := lines[j]
				if strings.TrimSpace(ln) == "" || strings.HasPrefix(strings.TrimSpace(ln), "#") {
					continue
				}
				if !strings.HasPrefix(ln, prefix+" ") && !strings.HasPrefix(ln, prefix+"-") {
					end = j
					break
				}
			}
			return i, end
		}
	}
	return -1, -1
}

// listValues 读取 path 指向的列表项（path 每层缩进依次 +2）。
func listValues(content string, path ...string) []string {
	content = normalizeEOL(content)
	lines := strings.Split(content, "\n")
	from, to, indent := 0, len(lines), 0
	for _, key := range path {
		i, end := blockLine(lines, from, to, indent, key)
		if i < 0 {
			return nil
		}
		from, to, indent = i+1, end, indent+2
	}
	var out []string
	itemPrefix := strings.Repeat(" ", indent) + "- "
	for i := from; i < to; i++ {
		if strings.HasPrefix(lines[i], itemPrefix) {
			v := strings.TrimSpace(strings.TrimPrefix(lines[i], itemPrefix))
			v = strings.Trim(v, `"'`)
			out = append(out, v)
		}
	}
	return out
}

// setNestedList 用 items 替换 path 指向的列表；中间层缺失时自动补齐，顶层缺失时追加到文件末尾。
// 传入空 items 时写成 `key: []` 清空列表。
func setNestedList(content string, items []string, path ...string) (string, error) {
	if len(path) == 0 {
		return content, errors.New("empty path")
	}
	lines := strings.Split(content, "\n")
	lines, err := setListRec(lines, 0, len(lines), 0, path, items)
	if err != nil {
		return content, err
	}
	return strings.Join(lines, "\n"), nil
}

// setListRec 返回 [from,to) 区间的替换内容（契约：只返回该区间的新行，不含区间外内容）。
func setListRec(lines []string, from, to, indent int, path []string, items []string) ([]string, error) {
	key := path[0]
	i, end := blockLine(lines, from, to, indent, key)
	if i < 0 {
		// 块不存在：在区间开头插入整条路径。
		add := []string{strings.Repeat(" ", indent) + key + ":"}
		if len(path) == 1 {
			add = append(add, listLines(indent+2, items)...)
		} else {
			sub, err := setListRec(nil, 0, 0, indent+2, path[1:], items)
			if err != nil {
				return nil, err
			}
			add = append(add, sub...)
		}
		out := append([]string{}, add...)
		out = append(out, lines[from:to]...)
		return out, nil
	}
	if len(path) == 1 {
		// 目标列表：删除旧条目行，插入新条目；空列表写成内联 []。
		var out []string
		out = append(out, lines[from:i]...)
		if len(items) == 0 {
			out = append(out, strings.Repeat(" ", indent)+key+": []")
		} else {
			out = append(out, lines[i])
			out = append(out, listLines(indent+2, items)...)
		}
		itemPrefix := strings.Repeat(" ", indent+2) + "- "
		for j := i + 1; j < end; j++ {
			if strings.HasPrefix(lines[j], itemPrefix) {
				continue
			}
			out = append(out, lines[j])
		}
		out = append(out, lines[end:to]...)
		return out, nil
	}
	// 中间层：子块内容由递归结果替换。
	sub, err := setListRec(lines, i+1, end, indent+2, path[1:], items)
	if err != nil {
		return nil, err
	}
	var out []string
	out = append(out, lines[from:i+1]...)
	out = append(out, sub...)
	out = append(out, lines[end:to]...)
	return out, nil
}

func listLines(indent int, items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, strings.Repeat(" ", indent)+"- "+yamlScalar(it))
	}
	return out
}

// setDeepKey 设置三级嵌套的标量键（如 derp.server.enabled）。
func setDeepKey(content, top, mid, key, value string) (string, error) {
	lines := strings.Split(content, "\n")
	ti, tend := blockLine(lines, 0, len(lines), 0, top)
	if ti < 0 {
		return content, fmt.Errorf("配置中缺少 %s 块", top)
	}
	mi, mend := blockLine(lines, ti+1, tend, 2, mid)
	if mi < 0 {
		// 中间块缺失：插入 mid 块与目标键。
		add := []string{"  " + mid + ":", "    " + key + ": " + value}
		out := append([]string{}, lines[:ti+1]...)
		out = append(out, add...)
		out = append(out, lines[ti+1:]...)
		return strings.Join(out, "\n"), nil
	}
	keyRe := regexp.MustCompile(`^    ` + regexp.QuoteMeta(key) + `:.*$`)
	for j := mi + 1; j < mend; j++ {
		if keyRe.MatchString(lines[j]) {
			lines[j] = "    " + key + ": " + value
			return strings.Join(lines, "\n"), nil
		}
	}
	// 键缺失：插入到 mid 块开头。
	out := append([]string{}, lines[:mi+1]...)
	out = append(out, "    "+key+": "+value)
	out = append(out, lines[mi+1:]...)
	return strings.Join(out, "\n"), nil
}
