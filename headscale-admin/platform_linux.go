//go:build !windows

package main

// Linux 平台的适配实现。
//
// 服务控制走 systemd，属主用 chown，日志用 journalctl，
// 证书续期交给 certbot —— 这些在 Windows 上都没有对应物，
// 那边的实现见 platform_windows.go。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Linux 上的默认落点。
const (
	linuxAdminDataDir   = "/var/lib/headscale-admin"
	linuxAdminAppDir    = "/opt/headscale-admin"
	linuxHeadscaleConf  = "/etc/headscale"
	linuxHeadscaleData  = "/var/lib/headscale"
	linuxCertbotTimer   = "/etc/systemd/system/certbot.timer.d"
	linuxLetsEncryptDir = "/etc/letsencrypt/live"
)

func defaultDataFile() string {
	return envPath("HSA_DATA_FILE", filepath.Join(linuxAdminDataDir, "headscale-admin.db"))
}

func defaultLegacyJSON() string {
	return envPath("HSA_LEGACY_JSON", filepath.Join(linuxAdminDataDir, "admins.json"))
}

func defaultTemplatesDir() string {
	return envPath("HSA_TEMPLATES", filepath.Join(linuxAdminAppDir, "templates"))
}

func defaultStaticDir() string {
	return envPath("HSA_STATIC", filepath.Join(linuxAdminAppDir, "static"))
}

func defaultHeadscaleBin() string {
	return envPath("HSA_HEADSCALE_BIN", "/usr/bin/headscale")
}

func defaultHeadscaleConfig() string {
	return envPath("HSA_CONFIG_FILE", filepath.Join(linuxHeadscaleConf, "config.yaml"))
}

func defaultHeadscaleCertDir() string {
	return filepath.Join(linuxHeadscaleConf, "certs")
}

func defaultHeadscaleDataDir() string {
	return linuxHeadscaleData
}

func defaultLECacheDir() string {
	return filepath.Join(linuxHeadscaleData, "cache")
}

func defaultLogDir() string {
	return "/var/log/headscale"
}

// svcIsActive 返回服务的 systemd 状态（active / inactive / failed ...）。
func svcIsActive(name string) (string, error) {
	return systemctlExec("is-active", name)
}

// svcRestart 重启服务。systemd 的 restart 会等旧进程收尾再起新的。
func svcRestart(name string) (string, error) {
	return systemctlExec("restart", name)
}

// svcStatus 返回服务的详细状态，lines 指定保留多少行日志。
func svcStatus(name string, lines int) (string, error) {
	return systemctlExec("status", name, "--no-pager", "-n", strconv.Itoa(lines))
}

// svcDaemonReload 让 systemd 重新读一遍单元文件。
func svcDaemonReload() error {
	_, err := systemctlExec("daemon-reload")
	return err
}

// svcLogs 取服务最近若干行日志。
func svcLogs(name string, lines int) (string, error) {
	b, err := execCombined(context.Background(), hsLocalTimeout, "journalctl",
		"-u", name, "-n", strconv.Itoa(lines), "--no-pager")
	return safeText(b), err
}

// systemctlExec 执行一条 systemctl 命令。
func systemctlExec(args ...string) (string, error) {
	b, err := execCombined(context.Background(), systemctlTimeout(args...), "systemctl", args...)
	return strings.TrimSpace(b), err
}

// chownRecursive 把目录树属主改成 headscale。
//
// 管理界面以 root 写入，headscale 以 headscale 用户运行——
// 0600 的 root 文件它读不了。失败时保留 root 属主，只影响排查提示。
func chownRecursive(path string) {
	_, _ = execCombined(context.Background(), hsWriteTimeout, "chown",
		"-R", headscaleUser+":"+headscaleUser, path)
}

// chownFile 把单个文件的属主改成 headscale。
func chownFile(path string) {
	_, _ = execCombined(context.Background(), hsWriteTimeout, "chown",
		headscaleUser+":"+headscaleUser, path)
}

// certbotPath 找 certbot 可执行文件，找不到返回空串。
func certbotPath() string {
	for _, p := range []string{"/usr/bin/certbot", "/usr/local/bin/certbot", "/snap/bin/certbot"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// certbotTimerDir 是 certbot.timer 覆盖配置的目录。
func certbotTimerDir() string { return linuxCertbotTimer }

// letsEncryptLiveGlob 是 Let's Encrypt 证书的存放通配符。
func letsEncryptLiveGlob() string { return filepath.Join(linuxLetsEncryptDir, "*") }

// certbotSupported 报告本平台是否支持 certbot 自动续期。
func certbotSupported() bool { return true }

// certbotTimerSet 启停 certbot.timer。
func certbotTimerSet(enable bool) (string, error) {
	action := "disable"
	if enable {
		action = "enable"
	}
	return systemctlExec(action, "--now", "certbot.timer")
}

// systemdShow 读 systemd 单元的属性，返回 属性名 → 属性值。
func systemdShow(unit string, props ...string) map[string]string {
	out := map[string]string{}
	args := append([]string{"show", unit, "-p", strings.Join(props, ",")}, "--no-pager")
	b, err := execCombined(context.Background(), hsLocalTimeout, "systemctl", args...)
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

// svcSelfRestart 重启管理界面自身。
//
// systemd 有 Restart=always 兜底：这里只要 systemctl restart 就行。
// 不在 systemd 下运行（手工跑、容器里）就没有守护进程拉起来，
// 直接返回错误，由界面提示「请手工重启」。
func svcSelfRestart(unit string) error {
	if os.Getenv("INVOCATION_ID") == "" {
		return errors.New("not running under systemd")
	}
	go func() {
		time.Sleep(time.Second)
		// 用独立上下文：重启是改系统状态的动作，不能因为发起它的那个
		// HTTP 请求断开就被取消——那样服务会停在新旧之间。
		_, _ = svcRestart(unit)
	}()
	return nil
}

// svcLogHint 返回「日志在哪」的提示文案。
//
// Linux 的日志在 journal 里，运维直接敲 journalctl 看。
func svcLogHint(name string, lines int) string {
	return fmt.Sprintf("journalctl -u %s -n %d", name, lines)
}
