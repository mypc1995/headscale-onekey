//go:build windows

package main

// Windows 平台的适配实现。
//
// 服务控制走 **计划任务**（Task Scheduler）而不是 sc.exe 注册的 Windows 服务：
// headscale 是上游二进制，不会注册 SCM 控制处理器，硬套 sc.exe 必然报
// 1053「服务没有响应启动或控制函数」，而且上游没法改。
// 计划任务零依赖、国内机器能离线用，是这台机器上唯一稳的托管方式。
//
// systemd 那一套（属主、journal、certbot）在 Windows 上没有对应物：
// 属主由部署脚本用 icacls 设一次，Windows 服务本来也不把 stdout 写进日志，
// 所以日志改读部署脚本重定向出来的 .log 文件。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Windows 上的默认落点。
//
// 数据与配置放 ProgramData（服务以 SYSTEM 身份运行时可读写），
// 程序本体放 Program Files（只读，避免被随手改坏）。
const (
	winProgramData  = `C:\ProgramData`
	winProgramFiles = `C:\Program Files`
)

func defaultDataFile() string {
	return envPath("HSA_DATA_FILE", filepath.Join(winProgramData, "headscale-admin", "headscale-admin.db"))
}

func defaultLegacyJSON() string {
	return envPath("HSA_LEGACY_JSON", filepath.Join(winProgramData, "headscale-admin", "admins.json"))
}

func defaultTemplatesDir() string {
	return envPath("HSA_TEMPLATES", filepath.Join(winProgramFiles, "headscale-admin", "templates"))
}

func defaultStaticDir() string {
	return envPath("HSA_STATIC", filepath.Join(winProgramFiles, "headscale-admin", "static"))
}

func defaultHeadscaleBin() string {
	return envPath("HSA_HEADSCALE_BIN", filepath.Join(winProgramFiles, "headscale", "headscale.exe"))
}

func defaultHeadscaleConfig() string {
	return envPath("HSA_CONFIG_FILE", filepath.Join(winProgramData, "headscale", "config.yaml"))
}

func defaultHeadscaleCertDir() string {
	return filepath.Join(winProgramData, "headscale", "certs")
}

func defaultHeadscaleDataDir() string {
	return filepath.Join(winProgramData, "headscale")
}

func defaultLECacheDir() string {
	return filepath.Join(winProgramData, "headscale", "cache")
}

// defaultLogDir 是部署脚本把服务输出重定向到的目录。
func defaultLogDir() string {
	return filepath.Join(defaultHeadscaleDataDir(), "logs")
}

// serviceExeName 返回服务对应的可执行文件名。
func serviceExeName(name string) string {
	if strings.EqualFold(name, "headscale") {
		return "headscale.exe"
	}
	return name + ".exe"
}

// svcIsActive 查进程是否在跑。
//
// 刻意用进程列表而不是计划任务状态：无论服务是计划任务拉起的、
// 控制台手工跑的，还是 NSSM 包出来的，这个判断都准。
// 进程名按字段精确匹配——"headscale-admin.exe" 里也含 "headscale.exe"，
// 用子串匹配会把管理界面自己当成控制服务在跑。
func svcIsActive(name string) (string, error) {
	exe := serviceExeName(name)
	out, err := execCombined(context.Background(), hsLocalTimeout, "tasklist.exe",
		"/FI", "IMAGENAME eq "+exe, "/NH")
	if err != nil {
		return "", err
	}
	if tasklistHasProcess(out, exe) {
		return "active", nil
	}
	return "inactive", nil
}

// tasklistHasProcess 在 tasklist /NH 的输出里找同名进程。
func tasklistHasProcess(out, exe string) bool {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if strings.EqualFold(fields[0], exe) {
			return true
		}
	}
	return false
}

// svcRestart 重启服务。
//
// 计划任务方式部署时：结束旧任务 → 收掉可能残留的孤儿进程 → 重新拉起。
// 单独跑在控制台里（没有计划任务）时只能结束进程，由外部守护或运维重新拉起。
func svcRestart(name string) (string, error) {
	if taskExists(name) {
		_, _ = execCombined(context.Background(), hsWriteTimeout, "schtasks.exe", "/End", "/TN", name)
		// 任务是 cmd 包装器的话，结束任务不一定会带走 headscale.exe，
		// 残留的孤儿进程会和新起的抢端口。
		_, _ = execCombined(context.Background(), hsWriteTimeout, "taskkill.exe",
			"/IM", serviceExeName(name), "/F")
		time.Sleep(2 * time.Second)
		out, err := execCombined(context.Background(), hsWriteTimeout, "schtasks.exe", "/Run", "/TN", name)
		if err != nil {
			return strings.TrimSpace(out + " " + err.Error()), err
		}
		return "", nil
	}
	out, err := execCombined(context.Background(), hsWriteTimeout, "taskkill.exe",
		"/IM", serviceExeName(name), "/F")
	if err != nil {
		return strings.TrimSpace(out + " " + err.Error()), err
	}
	return "该服务未注册为计划任务，已结束进程，请手动重新启动", nil
}

// taskExists 判断计划任务是否存在。
func taskExists(name string) bool {
	_, err := execCombined(context.Background(), hsLocalTimeout, "schtasks.exe", "/Query", "/TN", name)
	return err == nil
}

// svcStatus 返回服务状态与最近若干行日志。
func svcStatus(name string, lines int) (string, error) {
	out, err := execCombined(context.Background(), hsLocalTimeout, "schtasks.exe",
		"/Query", "/TN", name, "/V", "/FO", "LIST")
	if err != nil {
		// 没有这个计划任务时退回进程查询，至少能看出服务在不在跑。
		if state, e2 := svcIsActive(name); e2 == nil {
			return "服务状态: " + state, nil
		}
		return safeText(out), err
	}
	if logs, e3 := svcLogs(name, lines); e3 == nil && logs != "" {
		return out + "\n" + logs, nil
	}
	return out, nil
}

// svcDaemonReload 在 Windows 上没有对应概念（没有单元文件要重读），直接返回成功。
func svcDaemonReload() error { return nil }

// svcLogs 读服务日志文件的末尾若干行。
//
// Windows 服务不把 stdout 写进日志，部署脚本用 cmd 重定向到
// <默认数据目录>\logs\<服务名>.log，这里读的就是它。
func svcLogs(name string, lines int) (string, error) {
	path := filepath.Join(defaultLogDir(), name+".log")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	all := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if lines > 0 && len(all) > lines {
		all = all[len(all)-lines:]
	}
	return safeText(strings.Join(all, "\n")), nil
}

// chownRecursive 在 Windows 上是空操作。
//
// NTFS 的属主/权限模型跟 POSIX 完全不同：服务以 SYSTEM 身份运行时本来就能读
// ProgramData 下的文件，属主由部署脚本用 icacls 设一次即可，运行期不需要再改。
func chownRecursive(path string) {}

// chownFile 在 Windows 上是空操作，理由同 chownRecursive。
func chownFile(path string) {}

// certbotPath 在 Windows 上没有 certbot，返回空串。
func certbotPath() string { return "" }

// certbotTimerDir 在 Windows 上没有 systemd timer，返回空串。
func certbotTimerDir() string { return "" }

// letsEncryptLiveGlob 在 Windows 上没有 Let's Encrypt 目录，返回空串。
func letsEncryptLiveGlob() string { return "" }

// certbotSupported 报告本平台是否支持 certbot 自动续期。
func certbotSupported() bool { return false }

// certbotTimerSet 在 Windows 上没有 systemd timer，返回不支持。
func certbotTimerSet(enable bool) (string, error) {
	return "certbot.timer 仅在 Linux 上可用", errors.New("certbot.timer 仅在 Linux 上可用")
}

// systemdShow 在 Windows 上没有 systemd，返回空属性集。
func systemdShow(unit string, props ...string) map[string]string {
	return map[string]string{}
}

// svcSelfRestart 重启管理界面自身。
//
// Windows 没有 systemd 的 Restart=always：部署脚本用一个 cmd 包装器当看门狗，
// 包装器循环拉起本进程，只有收到约定的退出码时才立刻重启。
// 本进程要重启时按约定退出即可——包装器会把它重新拉起来。
//
// 不在包装器里运行（手工在控制台跑）就没有守护进程，直接返回错误，
// 由界面提示「请手工重启」。
func svcSelfRestart(unit string) error {
	code := strings.TrimSpace(os.Getenv("HSA_RESTART_EXIT_CODE"))
	if code == "" {
		return errors.New("not running under the task wrapper")
	}
	n, err := strconv.Atoi(code)
	if err != nil || n <= 0 || n > 255 {
		return errors.New("invalid restart exit code")
	}
	go func() {
		time.Sleep(time.Second)
		os.Exit(n)
	}()
	return nil
}

// svcLogHint 返回「日志在哪」的提示文案。
//
// Windows 的服务不写 stdout，部署脚本用 cmd 重定向到日志文件，
// 这里直接把文件路径告诉运维。
func svcLogHint(name string, lines int) string {
	return filepath.Join(defaultLogDir(), name+".log")
}
