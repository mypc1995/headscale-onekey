//go:build windows

package main

// Windows 的主机资源采集：走 Win32 API。
//
// 公共部分（hostmon.go）只定义数据结构与采样循环，
// 「数据从哪来」这一层按平台拆开——Windows 没有 /proc。
//
// 口径说明：
//   - CPU 用 GetSystemTimes，它的 kernel 时间**已经包含 idle**，
//     所以总时间取 kernel+user，空闲时间单独取 idle；
//   - 内存用 GlobalMemoryStatusEx，可用内存就是「还能用的」那部分；
//   - 运行时长用 GetTickCount64（开机以来的毫秒数）；
//   - Windows 没有 load average 这个概念，返回 0 ——
//     Linux 上读不到 /proc/loadavg 时也是 0，页面本来就按 0 显示。

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32DLL            = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes     = kernel32DLL.NewProc("GetSystemTimes")
	procGlobalMemoryStatus = kernel32DLL.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64     = kernel32DLL.NewProc("GetTickCount64")
)

// memoryStatusEx 对应 Win32 的 MEMORYSTATUSEX。
//
// 前两个字段是 DWORD（32 位），其余是 DWORD64；
// 结构体的对齐由 Go 编译器按 8 字节处理，和 Win32 的布局一致。
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// readCPU 读一次 CPU 累计时间，返回 (总时间, 空闲时间)，单位是 100 纳秒。
func readCPU() (uint64, uint64, error) {
	var idle, kernel, user int64
	r, _, err := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		if err == nil {
			err = errors.New("GetSystemTimes 失败")
		}
		return 0, 0, err
	}
	// kernel 时间里已经含 idle，不能再把 idle 加回去。
	return uint64(kernel) + uint64(user), uint64(idle), nil
}

// readMemory 读一次物理内存，返回 (总量, 可用量)，单位是字节。
func readMemory() (uint64, uint64, error) {
	ms := memoryStatusEx{}
	ms.Length = uint32(unsafe.Sizeof(ms))
	r, _, err := procGlobalMemoryStatus.Call(uintptr(unsafe.Pointer(&ms)))
	if r == 0 {
		if err == nil {
			err = errors.New("GlobalMemoryStatusEx 失败")
		}
		return 0, 0, err
	}
	return ms.TotalPhys, ms.AvailPhys, nil
}

// readLoadAvg 在 Windows 上没有 load average，返回 0。
func readLoadAvg() (float64, float64, float64) { return 0, 0, 0 }

// readUptime 读开机时长，单位是秒。
func readUptime() uint64 {
	r, _, _ := procGetTickCount64.Call()
	return uint64(r) / 1000
}
