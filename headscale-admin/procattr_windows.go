//go:build windows

package main

import "os/exec"

// setProcAttr 在 Windows 上不需要进程组处理。
//
// 管理界面只跑在 Linux 上，Windows 这份只是为了本地编译与单测能过。
func setProcAttr(cmd *exec.Cmd) {}

// killGroup 在 Windows 上只杀直接子进程。
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
