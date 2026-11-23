package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type HSUser struct {
	ID        uint64         `json:"id"`
	Name      string         `json:"name"`
	CreatedAt map[string]any `json:"created_at"`
}

type HSClient struct {
	mu     sync.Mutex
	Bin    string
	Config string
}

func NewHSClient() *HSClient {
	bin := os.Getenv("HSA_HEADSCALE_BIN")
	if bin == "" {
		bin = "/usr/bin/headscale"
	}
	conf := os.Getenv("HSA_CONFIG_FILE")
	if conf == "" {
		conf = "/etc/headscale/config.yaml"
	}
	return &HSClient{Bin: bin, Config: conf}
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// safeText 把子进程输出转成合法的 UTF-8。
//
// 子进程的 stderr / stdout 是原始字节：中文 locale 下 headscale 输出的
// GBK 字节序列不是合法 UTF-8，直接塞进网页会显示成「���」这样的乱码，
// 而且页面本身也不再是合法的 UTF-8 文档。
func safeText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}

func (c *HSClient) run(args ...string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	args = append(args, "-o", "json")
	cmd := exec.Command(c.Bin, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(safeText(ansiRE.ReplaceAllString(errBuf.String(), "")))
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(safeText(ansiRE.ReplaceAllString(out.String(), ""))), nil
}

func (c *HSClient) runJSON(args ...string) (any, error) {
	out, err := c.run(args...)
	if err != nil {
		return nil, err
	}
	if out == "" || out == "null" {
		return []any{}, nil
	}
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, fmt.Errorf("解析输出失败: %w", err)
	}
	return v, nil
}

func asSlice(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	if m, ok := v.(map[string]any); ok {
		return []any{m}
	}
	return []any{}
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func (c *HSClient) Users() ([]HSUser, error) {
	v, err := c.runJSON("users", "list")
	if err != nil {
		return nil, err
	}
	out := []HSUser{}
	for _, item := range asSlice(v) {
		m := asMap(item)
		u := HSUser{Name: str(m["name"]), CreatedAt: asMap(m["created_at"])}
		u.ID, _ = strconv.ParseUint(fmt.Sprint(m["id"]), 10, 64)
		out = append(out, u)
	}
	return out, nil
}

func (c *HSClient) Nodes() ([]map[string]any, error) {
	v, err := c.runJSON("nodes", "list")
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, item := range asSlice(v) {
		out = append(out, asMap(item))
	}
	return out, nil
}

func (c *HSClient) Routes() ([]map[string]any, error) {
	v, err := c.runJSON("nodes", "list-routes")
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, item := range asSlice(v) {
		out = append(out, asMap(item))
	}
	return out, nil
}

func (c *HSClient) PreauthKeys() ([]map[string]any, error) {
	v, err := c.runJSON("preauthkeys", "list")
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, item := range asSlice(v) {
		out = append(out, asMap(item))
	}
	return out, nil
}

func (c *HSClient) APIKeys() ([]map[string]any, error) {
	v, err := c.runJSON("apikeys", "list")
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, item := range asSlice(v) {
		out = append(out, asMap(item))
	}
	return out, nil
}

func (c *HSClient) Action(args ...string) (string, error) {
	return c.run(args...)
}

func (c *HSClient) CreatePreauthKey(userID string, expiration string, reusable bool, ephemeral bool, tags string) (string, error) {
	args := []string{"preauthkeys", "create", "--user", userID, "--expiration", expiration}
	if reusable {
		args = append(args, "--reusable")
	}
	if ephemeral {
		args = append(args, "--ephemeral")
	}
	if tags != "" {
		args = append(args, "--tags", tags)
	}
	return c.run(args...)
}

func (c *HSClient) CreateAPIKey(expiration string) (string, error) {
	return c.run("apikeys", "create", "--expiration", expiration)
}

func (c *HSClient) Version() string {
	cmd := exec.Command(c.Bin, "version")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "未知"
	}
	lines := strings.Fields(strings.TrimSpace(safeText(ansiRE.ReplaceAllString(string(b), ""))))
	if len(lines) >= 3 {
		return lines[2]
	}
	return strings.TrimSpace(string(b))
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}

// maskedKey 判断 headscale 返回的密钥是否被打码。
// preauthkeys list 出于安全考虑只返回形如 hskey-auth-xxxx-*** 的脱敏值，
// 这种值不能用于客户端接入，界面上不提供复制与填入命令。
func maskedKey(v any) bool {
	return strings.Contains(fmt.Sprint(v), "***")
}

// protoSeconds 从 protobuf JSON 时间对象（{"seconds": ..., "nanos": ...}）取秒数。
// JSON 数字解码后是 float64，直接 fmt.Sprint 会得到 1.794975942e+09，
// ParseInt 解析失败就退化成 0，所以按类型分别取值。
func protoSeconds(v any) int64 {
	m, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	switch t := m["seconds"].(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case string:
		sec, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return sec
	default:
		sec, _ := strconv.ParseInt(fmt.Sprint(t), 10, 64)
		return sec
	}
}

func protoTime(v any) string {
	if m, ok := v.(map[string]any); !ok {
		return str(v)
	} else {
		if sec := protoSeconds(m); sec > 0 {
			return time.Unix(sec, 0).Format("2006-01-02 15:04")
		}
	}
	return "—"
}

// keyExpired 判断预授权密钥是否已过期（expiration 早于当前时间）。
func keyExpired(m map[string]any) bool {
	if b, ok := m["expired"].(bool); ok && b {
		return true
	}
	sec := protoSeconds(m["expiration"])
	return sec > 0 && sec < time.Now().Unix()
}

// isTrue 兼容 JSON 里的布尔与字符串形式的 true。
func isTrue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
}

func (c *HSClient) configBytes() ([]byte, error) {
	b, err := os.ReadFile(c.Config)
	if err != nil {
		return nil, err
	}
	// 读进来就归一化行尾：后续所有派生内容（页面读取、写回、备份对比）
	// 都基于它，历史上被写成 CRLF 的配置也会在这里自愈回 LF。
	return []byte(normalizeEOL(string(b))), nil
}

// normalizeEOL 把 CRLF / CR 统一成 LF。
//
// 浏览器的 textarea 提交时按 HTML 规范把换行规范化成 CRLF，服务端原样落盘，
// 配置文件就成了 CRLF。headscale 自己的 YAML 解析器能接受 CRLF，但本项目的
// 行解析器认不出来：blockBody 要求块头后面紧跟 \n，"dns:\r\n" 匹配不上，
// 于是 dns / derp 块里的值全被读成空——页面上域名是空的、开关是未勾选的，
// 用户再点一次保存，空值就被写回配置。
//
// 所以读进来先归一化（顺带把历史上被写成 CRLF 的配置自愈回 LF），
// 写出去之前也归一化（浏览器提交的内容不带这层保护）。
func normalizeEOL(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func topValue(content string, key string) string {
	content = normalizeEOL(content)
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*(.*)$`)
	m := re.FindStringSubmatch(content)
	if len(m) < 2 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(m[1]), `"'`)
}

func nestedValue(content string, block string, key string) string {
	content = normalizeEOL(content)
	inner, ok := blockBody(content, block)
	if !ok {
		return ""
	}
	m := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(key) + `:\s*(.*)$`).FindStringSubmatch(inner)
	if len(m) < 2 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(m[1]), `"'`)
}

// blockBody 取出顶层块（block:）的内容，范围严格限制在该块之内。
//
// 不能用 (?ms) 让 "." 匹配换行：那样 `.*` 会贪婪吞到文件末尾，块内查找就会
// 越界命中后面别的块里的同名键。踩过：删掉 prefixes.v6 之后，读取路径又从
// 后面的 dns 块里翻出一个同名键，页面开关照样显示已勾选，看着像「删除没生效」。
func blockBody(content string, block string) (string, bool) {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(block) + `:[ \t]*\n((?:(?:[ \t][^\n]*|[ \t]*)(?:\n|\z))*)`)
	m := re.FindStringSubmatch(content)
	if len(m) < 2 {
		return "", false
	}
	return m[1], true
}

func deepValue(content string, block string, key string) string {
	content = normalizeEOL(content)
	inner, ok := blockBody(content, block)
	if !ok {
		return ""
	}
	m := regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(key) + `:\s*(.*)$`).FindStringSubmatch(inner)
	if len(m) < 2 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(m[1]), `"'`)
}

func replaceLine(content string, pattern string, replacement string, lang ...string) (string, error) {
	L := reqLang(lang)
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	if !re.MatchString(content) {
		return content, errors.New(tr(L, "hs.noConfigItem"))
	}
	return re.ReplaceAllLiteralString(content, replacement), nil
}

func (c *HSClient) ReadConfigText() (string, error) {
	b, err := c.configBytes()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (c *HSClient) UpdateTop(key string, value string) error {
	b, err := c.configBytes()
	if err != nil {
		return err
	}
	next, err := replaceLine(string(b), `(?m)^`+regexp.QuoteMeta(key)+`:.*$`, key+": "+value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.Config, []byte(next), 0600); err != nil {
		return err
	}
	return c.chownConfig()
}

func (c *HSClient) chownConfig() error {
	_ = exec.Command("chown", "headscale:headscale", c.Config).Run()
	return nil
}

func (c *HSClient) SaveConfig(content string) error {
	old, err := c.configBytes()
	if err != nil {
		return err
	}
	// 浏览器 textarea 提交上来的换行是 CRLF，原样落盘会把配置写成 CRLF，
	// 本项目的行解析器随后就读不出 dns / derp 块了。落盘前统一成 LF。
	content = normalizeEOL(content)
	_ = os.WriteFile(c.Config+".bak."+time.Now().Format("20060102-150405"), old, 0600)
	if err := os.WriteFile(c.Config, []byte(content), 0600); err != nil {
		return err
	}
	return c.chownConfig()
}

func (c *HSClient) UpdateDERP(key string, value string, lang ...string) error {
	L := reqLang(lang)
	b, err := c.configBytes()
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`(?ms)(^derp:\s*\n)((?:[ \t]+.*\n?)*)`)
	loc := re.FindStringSubmatchIndex(string(b))
	if loc == nil {
		return errors.New(tr(L, "hs.noDerpBlock"))
	}
	inner := b[loc[2]:loc[3]]
	innerNext, err := replaceLine(string(inner), `(?m)^    `+regexp.QuoteMeta(key)+`:.*$`, "    "+key+": "+value)
	if err != nil {
		return err
	}
	next := string(b[:loc[2]]) + innerNext + string(b[loc[3]:])
	if err := os.WriteFile(c.Config, []byte(next), 0600); err != nil {
		return err
	}
	return c.chownConfig()
}

func (c *HSClient) PolicyPath(lang ...string) string {
	b, err := c.configBytes()
	if err != nil {
		return ""
	}
	return nestedValue(string(b), "policy", "path")
}

func (c *HSClient) Policy(lang ...string) (string, error) {
	L := reqLang(lang)
	p := c.PolicyPath()
	if p == "" {
		return L, errors.New(tr(L, "hs.noPolicyPath"))
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (c *HSClient) SavePolicy(content string, lang ...string) error {
	L := reqLang(lang)
	p := c.PolicyPath()
	if p == "" {
		return errors.New(tr(L, "hs.noPolicyPath"))
	}
	// 同 SaveConfig：浏览器 textarea 提交的是 CRLF，落盘前统一成 LF。
	content = normalizeEOL(content)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	if old, err := os.ReadFile(p); err == nil {
		_ = os.WriteFile(p+".bak."+time.Now().Format("20060102-150405"), old, 0600)
	}
	return os.WriteFile(p, []byte(content), 0600)
}

// SavePolicyChecked 校验并保存 ACL 策略文件。
//
// 旧实现是「先落盘，再拿 headscale configtest 校验」：configtest 校验的是
// config.yaml，根本不看策略文件本身，策略写坏了也照样留在盘上——而策略
// 文件坏掉时 headscale 启动会直接失败，等于留了一颗地雷。
//
// 现在改成：先写候选文件 → 用 headscale policy check 校验 → 通过才备份并原子替换。
// 校验用 headscale 自带的 policy check：它解析 HuJSON，还会核对策略里引用的
// 用户/组是否真实存在，比自己判 JSON 语法可靠得多。
func (c *HSClient) SavePolicyChecked(content string, lang ...string) error {
	L := reqLang(lang)
	p := c.PolicyPath()
	if p == "" {
		return errors.New(tr(L, "hs.noPolicyPath"))
	}
	content = normalizeEOL(content)
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	candidate := p + ".candidate"
	if err := os.WriteFile(candidate, []byte(content), 0600); err != nil {
		return err
	}
	defer os.Remove(candidate)

	out, err := exec.Command(c.Bin, "policy", "check", "-c", c.Config, "-f", candidate).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(safeText(ansiRE.ReplaceAllString(string(out), "")))
		return errors.New(tr(L, "hs.policyInvalid", msg))
	}
	if old, err := os.ReadFile(p); err == nil {
		_ = os.WriteFile(p+".bak."+time.Now().Format("20060102-150405"), old, 0600)
	}
	if err := os.Rename(candidate, p); err != nil {
		return err
	}
	// 策略文件得让 headscale 服务读得到：它以 headscale 用户运行，
	// 管理界面以 root 写入，0600 的 root 文件它读不了。
	_ = exec.Command("chown", "headscale:headscale", p).Run()
	return nil
}

func systemctl(args ...string) (string, error) {
	cmd := exec.Command("systemctl", args...)
	b, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(b)), err
}

func journal(n int) string {
	cmd := exec.Command("journalctl", "-u", "headscale", "-n", strconv.Itoa(n), "--no-pager")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return safeText(string(b))
	}
	return safeText(ansiRE.ReplaceAllString(string(b), ""))
}

func certExpiry() string {
	cmd := exec.Command("sh", "-c", `set -e
conf=/etc/headscale/config.yaml
p=$(grep -E '^tls_cert_path:' "$conf" 2>/dev/null | head -1 | sed -E 's/^tls_cert_path:[[:space:]]*//; s/^"|"$//g')
if [ -z "$p" ] || [ ! -f "$p" ]; then
  p=$(find /var/lib/headscale/cache -type f \( -name '*.crt' -o -name '*.pem' \) 2>/dev/null | head -1)
fi
[ -n "$p" ] && [ -f "$p" ] || exit 0
openssl x509 -in "$p" -noout -enddate | sed 's/^notAfter=//'
`)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "—"
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "—"
	}
	return s
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
