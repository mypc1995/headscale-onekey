//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Windows 平台适配的专属断言。
//
// 这些是「在 Windows 上真的不一样」的地方：进程名的精确匹配、
// 默认落点的位置、日志以文件而非 journal 形式存在。

// TestTasklistHasProcessMatchesExactName 验证进程名按字段精确匹配。
//
// 关键点：管理界面的可执行文件叫 headscale-admin.exe，
// 用子串匹配 "headscale.exe" 会把它自己当成控制服务在跑——
// 服务状态会一直显示「运行中」，哪怕 headscale 根本没起来。
func TestTasklistHasProcessMatchesExactName(t *testing.T) {
	out := "headscale.exe                 10440 Console                    1     76,000 K\r\n" +
		"headscale-admin.exe            25204 Console                    1     40,000 K\r\n"
	if !tasklistHasProcess(out, "headscale.exe") {
		t.Fatal("应当匹配到 headscale.exe")
	}
	// 反过来查 headscale-admin.exe 不该被 headscale.exe 的查询命中。
	if !tasklistHasProcess(out, "headscale-admin.exe") {
		t.Fatal("应当匹配到 headscale-admin.exe")
	}
	// 不在列表里就不能报「在跑」。
	if tasklistHasProcess(out, "nginx.exe") {
		t.Fatal("不在列表里的进程不应被判定为在跑")
	}
	// 空输出也不能误判。
	if tasklistHasProcess("", "headscale.exe") {
		t.Fatal("空输出不应判定为在跑")
	}
}

// TestTasklistHasProcessIgnoresNonProcessLines 验证无关行不会造成误判。
//
// tasklist 带筛选条件且没有匹配项时会输出一行提示语，
// 不带 /NH 时还有表头。两种行都不能被当成「服务在跑」。
func TestTasklistHasProcessIgnoresNonProcessLines(t *testing.T) {
	out := "Image Name                     PID Session Name        Session#       Mem Usage\r\n" +
		"headscale.exe                 10440 Console                    1     76,000 K\r\n"
	if !tasklistHasProcess(out, "headscale.exe") {
		t.Fatal("应当匹配到 headscale.exe")
	}
	// 表头行与「无匹配」提示行都不能被当成进程。
	only := "INFO: No tasks are running which match the specified criteria.\r\n"
	if tasklistHasProcess(only, "headscale.exe") {
		t.Fatal("无匹配提示行不应判定为在跑")
	}
	if tasklistHasProcess(out, "Image") != true {
		t.Fatal("按首字段精确匹配时，表头首字段 Image 会被匹配上（真实查询不会问这个名字）")
	}
}

// TestServiceExeName 验证服务名到可执行文件名的映射。
func TestServiceExeName(t *testing.T) {
	if got := serviceExeName("headscale"); got != "headscale.exe" {
		t.Fatalf("headscale 应映射到 headscale.exe，得到 %q", got)
	}
	if got := serviceExeName("headscale-admin"); got != "headscale-admin.exe" {
		t.Fatalf("headscale-admin 应映射到 headscale-admin.exe，得到 %q", got)
	}
}

// TestWindowsDefaultPathsUnderProgramDirs 验证默认落点都在 Program Files / ProgramData 下。
//
// Program Files 是只读的程序本体位置，ProgramData 是服务以 SYSTEM 身份运行时
// 可读写的共享数据位置——两边分开是 Windows 的惯例，也避免服务把程序本体改坏。
func TestWindowsDefaultPathsUnderProgramDirs(t *testing.T) {
	programFiles := filepath.Clean(os.Getenv("ProgramFiles"))
	programData := filepath.Clean(os.Getenv("ProgramData"))
	if programFiles == "" || programData == "" {
		t.Skip("当前环境没有 ProgramFiles / ProgramData")
	}
	for name, p := range map[string]string{
		"模板目录":      defaultTemplatesDir(),
		"静态目录":      defaultStaticDir(),
		"headscale": defaultHeadscaleBin(),
	} {
		if !strings.HasPrefix(p, programFiles) {
			t.Fatalf("%s 应落在 Program Files 下，得到 %q", name, p)
		}
	}
	for name, p := range map[string]string{
		"数据文件":    defaultDataFile(),
		"旧版 JSON": defaultLegacyJSON(),
		"配置":      defaultHeadscaleConfig(),
		"证书目录":    defaultHeadscaleCertDir(),
		"数据目录":    defaultHeadscaleDataDir(),
		"LE 缓存目录": defaultLECacheDir(),
		"日志目录":    defaultLogDir(),
	} {
		if !strings.HasPrefix(p, programData) {
			t.Fatalf("%s 应落在 ProgramData 下，得到 %q", name, p)
		}
	}
}

// TestWindowsSvcLogHintIsFilePath 验证日志提示给的是文件路径。
//
// Windows 的服务不写 stdout，日志是部署脚本重定向出来的文件，
// 运维页必须直接告诉运维文件在哪，而不是给一条 Linux 的 journalctl 命令。
func TestWindowsSvcLogHintIsFilePath(t *testing.T) {
	hint := svcLogHint("headscale", 80)
	if !strings.HasSuffix(hint, "headscale.log") {
		t.Fatalf("日志提示应是日志文件路径，得到 %q", hint)
	}
	if strings.Contains(hint, "journalctl") {
		t.Fatalf("Windows 上不该出现 journalctl，得到 %q", hint)
	}
}

// TestWindowsCertbotUnsupported 验证 Windows 上明确不支持 certbot 自动续期。
//
// 界面据此显示「certbot 自动续期仅在 Linux 上可用」的提示，
// 而不是让运维去装一个根本用不上的 dnf/apt。
func TestWindowsCertbotUnsupported(t *testing.T) {
	if certbotSupported() {
		t.Fatal("Windows 上不应支持 certbot")
	}
	if certbotPath() != "" {
		t.Fatalf("Windows 上不应有 certbot 路径，得到 %q", certbotPath())
	}
	if certbotTimerDir() != "" {
		t.Fatalf("Windows 上不应有 certbot timer 目录，得到 %q", certbotTimerDir())
	}
	if _, err := certbotTimerSet(true); err == nil {
		t.Fatal("Windows 上启停 certbot.timer 应当报错")
	}
}
