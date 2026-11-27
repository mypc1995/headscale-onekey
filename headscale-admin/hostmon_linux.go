//go:build !windows

package main

// Linux 的主机资源采集：全部走 /proc。
//
// 公共部分（hostmon.go）只定义数据结构与采样循环，
// 「数据从哪来」这一层按平台拆开——Windows 没有 /proc。

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// readCPU 读一次 CPU 累计时间，返回 (总时间, 空闲时间)，单位是 tick。
func readCPU() (uint64, uint64, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	total, idle, ok := parseProcStat(string(b))
	if !ok {
		return 0, 0, errors.New("/proc/stat has no cpu line")
	}
	return total, idle, nil
}

// readMemory 读一次内存，返回 (总量, 可用量)，单位是字节。
func readMemory() (uint64, uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	total, avail, ok := parseMeminfo(string(b))
	if !ok || total == 0 {
		return 0, 0, errors.New("/proc/meminfo is incomplete")
	}
	return total, avail, nil
}

// readLoadAvg 读 1/5/15 分钟负载。读不到就返回 0。
func readLoadAvg() (float64, float64, float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	a, _ := strconv.ParseFloat(fields[0], 64)
	c, _ := strconv.ParseFloat(fields[1], 64)
	d, _ := strconv.ParseFloat(fields[2], 64)
	return a, c, d
}

// readUptime 读系统运行时长，单位是秒。
func readUptime() uint64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || f < 0 {
		return 0
	}
	return uint64(f)
}
