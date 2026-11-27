package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 平台适配层的公共行为测试。
//
// 这些断言在 Linux 与 Windows 上都要成立：平台差异只应该体现在
// 「落点在哪、命令长什么样」，不该体现在「行为对不对」。

// TestEnvPathOverride 验证环境变量优先、没设时用平台默认值。
func TestEnvPathOverride(t *testing.T) {
	t.Setenv("HSA_TEST_PATH", "D:\\custom\\thing")
	if got := envPath("HSA_TEST_PATH", "/default/thing"); got != "D:\\custom\\thing" {
		t.Fatalf("环境变量应当优先，得到 %q", got)
	}
	// 没设环境变量：给的是平台默认值。
	if got := envPath("HSA_TEST_PATH_NOT_SET", "/default/thing"); got != "/default/thing" {
		t.Fatalf("未设置时应回落到默认值，得到 %q", got)
	}
	// 设成空白也当没设：空白路径会让后面的拼路径产出诡异的路径。
	t.Setenv("HSA_TEST_PATH", "   ")
	if got := envPath("HSA_TEST_PATH", "/default/thing"); got != "/default/thing" {
		t.Fatalf("空白环境变量应回落到默认值，得到 %q", got)
	}
}

// TestTrimSep 验证去掉路径末尾分隔符。
func TestTrimSep(t *testing.T) {
	cases := map[string]string{
		`C:\ProgramData\headscale-admin\`: `C:\ProgramData\headscale-admin`,
		`C:\ProgramData\headscale-admin`:  `C:\ProgramData\headscale-admin`,
		"/var/lib/headscale-admin/":       "/var/lib/headscale-admin",
		"/var/lib/headscale-admin":        "/var/lib/headscale-admin",
	}
	for in, want := range cases {
		if got := trimSep(in); got != want {
			t.Fatalf("trimSep(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestJoinPath 验证拼路径不会留下多余分隔符。
func TestJoinPath(t *testing.T) {
	got := joinPath(trimSep(`C:\Program Files\headscale-admin\`), "templates")
	if !strings.HasSuffix(got, "templates") {
		t.Fatalf("拼路径结果不对: %q", got)
	}
	if strings.Contains(got, `\\`) {
		t.Fatalf("拼路径出现了重复分隔符: %q", got)
	}
}

// TestDefaultPathsAreAbsolute 验证所有默认落点都是绝对路径。
//
// 相对路径在不同工作目录下含义不同，服务由计划任务/ systemd 拉起时
// 工作目录未必是预期那个，配置与数据就会落到奇怪的地方。
func TestDefaultPathsAreAbsolute(t *testing.T) {
	paths := map[string]string{
		"数据文件":      defaultDataFile(),
		"旧版 JSON":   defaultLegacyJSON(),
		"模板目录":      defaultTemplatesDir(),
		"静态目录":      defaultStaticDir(),
		"headscale": defaultHeadscaleBin(),
		"配置":        defaultHeadscaleConfig(),
		"证书目录":      defaultHeadscaleCertDir(),
		"数据目录":      defaultHeadscaleDataDir(),
		"LE 缓存目录":   defaultLECacheDir(),
		"日志目录":      defaultLogDir(),
	}
	for name, p := range paths {
		if p == "" {
			t.Fatalf("%s 的默认路径不应为空", name)
		}
		if !filepath.IsAbs(p) {
			t.Fatalf("%s 的默认路径应是绝对路径，得到 %q", name, p)
		}
	}
}

// TestHSRunPassesConfigPath 验证每次调用 headscale CLI 都带上配置路径。
//
// 不带 -c 时，headscale 会按 /etc/headscale → ~/.headscale → 当前目录
// 去找配置：那套路径在 Windows 上不存在，CLI 直接读不到配置报错；
// 就算存在，也可能读到跟界面上编辑的不是同一份文件。
func TestHSRunPassesConfigPath(t *testing.T) {
	c := &HSClient{Bin: "/usr/bin/headscale", Config: "/etc/headscale/config.yaml"}
	var got []string
	c.exec = func(ctx context.Context, name string, args ...string) (string, string, error) {
		got = args
		return `[]`, "", nil
	}
	if _, err := c.run("nodes", "list"); err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[len(got)-2] != "-c" || got[len(got)-1] != "/etc/headscale/config.yaml" {
		t.Fatalf("命令行末尾应是 -c <配置路径>，实际: %v", got)
	}
	// JSON 输出标志必须保留。
	if !strings.Contains(strings.Join(got, " "), "-o json") {
		t.Fatalf("命令行应保留 -o json，实际: %v", got)
	}
}

// TestHSRunWithoutConfigOmitsFlag 验证没配路径时不硬塞 -c。
func TestHSRunWithoutConfigOmitsFlag(t *testing.T) {
	c := &HSClient{Bin: "/usr/bin/headscale"}
	var got []string
	c.exec = func(ctx context.Context, name string, args ...string) (string, string, error) {
		got = args
		return `[]`, "", nil
	}
	if _, err := c.run("nodes", "list"); err != nil {
		t.Fatal(err)
	}
	for _, a := range got {
		if a == "-c" {
			t.Fatalf("没有配置路径时不应带 -c，实际: %v", got)
		}
	}
}

// TestPolicyPathResolvesRelativeToConfigDir 验证相对策略路径按配置目录解析。
//
// headscale 自己就是按「相对配置文件所在目录」解析 policy.path 的
// （util.AbsolutePathFromConfigPath）。管理界面若按当前工作目录解析，
// 同一个相对路径会落到两个地方：headscale 读 A，管理界面写 B。
func TestPolicyPathResolvesRelativeToConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("policy:\n  path: acl.hujson\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &HSClient{Config: cfg}
	got := c.PolicyPath()
	want := filepath.Join(dir, "acl.hujson")
	if got != want {
		t.Fatalf("相对策略路径应解析到配置目录，得到 %q，期望 %q", got, want)
	}
}

// TestPolicyPathKeepsAbsolute 验证绝对策略路径原样保留。
func TestPolicyPathKeepsAbsolute(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "acl.hujson")
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("policy:\n  path: "+abs+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &HSClient{Config: cfg}
	if got := c.PolicyPath(); got != abs {
		t.Fatalf("绝对策略路径应原样保留，得到 %q，期望 %q", got, abs)
	}
}
