//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// setProcAttr 让子进程独立成组。
//
// 只杀直接子进程是不够的：`sh -c` 下面还有 openssl、find 这些孙进程，
// 它们会变成孤儿继续跑，文件也一直锁着。独立成组之后能整组一起收掉。
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup 杀掉子进程所在的整个进程组。
//
// 负的 pid 表示进程组。进程组已经退了（命令自己结束得很快）时，
// 再单独杀一次直接子进程兜底。
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
