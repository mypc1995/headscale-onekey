//go:build !windows

package main

import (
	"strings"
	"testing"
)

// Linux 平台适配的专属断言。
//
// 与 Windows 侧对称：同样的能力，两边表现必须一致，
// 只是「落点」和「命令长相」不同。

// TestLinuxSvcLogHintIsJournalctl 验证日志提示给的是 journalctl 命令。
func TestLinuxSvcLogHintIsJournalctl(t *testing.T) {
	hint := svcLogHint("headscale", 80)
	if !strings.HasPrefix(hint, "journalctl -u headscale") {
		t.Fatalf("日志提示应是 journalctl 命令，得到 %q", hint)
	}
	if !strings.Contains(hint, "-n 80") {
		t.Fatalf("日志提示应带行数，得到 %q", hint)
	}
}

// TestLinuxCertbotSupported 验证 Linux 上 certbot 自动续期能力存在。
func TestLinuxCertbotSupported(t *testing.T) {
	if !certbotSupported() {
		t.Fatal("Linux 上应支持 certbot")
	}
	if certbotTimerDir() == "" {
		t.Fatal("Linux 上应有 certbot timer 目录")
	}
	if letsEncryptLiveGlob() == "" {
		t.Fatal("Linux 上应有 Let's Encrypt 目录通配符")
	}
}
