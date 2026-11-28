//go:build windows

package main

// Windows 的网卡计数器采集：走 iphlpapi 的 GetIfTable2Ex。
//
// 公共部分（netmon.go）只定义数据结构与速率计算，
// 「计数从哪来」这一层按平台拆开——Windows 没有 /proc/net/dev。

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// ifTypeSoftwareLoopback 是 MIB_IF_ROW2.Type 里的回环口类型值。
const ifTypeSoftwareLoopback = 24

// readNetCounters 读一次各网卡的累计收发字节数。
//
// GetIfTable2Ex 由 DLL 分配缓冲区并返回指针，行数在 NumEntries 里。
// Go 侧的 MibIfTable2 只声明了 1 行（x/sys 的类型就是这么写的），
// 所以这里用一个超长数组视图去读真正的行——内存是 DLL 分配的，
// 用完由 FreeMibTable 释放。
//
// 回环口不统计：它上面的流量都是本机内部转发，算进去会把总速率抬高。
func readNetCounters() (map[string]netCounter, error) {
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.AF_INET, &table); err != nil {
		return nil, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))

	n := int(table.NumEntries)
	if n <= 0 {
		return map[string]netCounter{}, nil
	}
	rows := (*[1 << 16]windows.MibIfRow2)(unsafe.Pointer(&table.Table[0]))[:n:n]

	out := make(map[string]netCounter, n)
	for i := range rows {
		if rows[i].Type == ifTypeSoftwareLoopback {
			continue
		}
		name := windows.UTF16ToString(rows[i].Alias[:])
		if name == "" {
			name = windows.UTF16ToString(rows[i].Description[:])
		}
		if name == "" {
			continue
		}
		out[name] = netCounter{rx: rows[i].InOctets, tx: rows[i].OutOctets}
	}
	return out, nil
}
