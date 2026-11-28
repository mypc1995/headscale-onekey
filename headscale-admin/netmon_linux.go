//go:build !windows

package main

// Linux 的网卡计数器采集：读 /proc/net/dev。
//
// 公共部分（netmon.go）只定义数据结构与速率计算，
// 「计数从哪来」这一层按平台拆开——Windows 没有 /proc。

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// readNetCounters 读一次各网卡的累计收发字节数。
//
// 回环口不统计：它上面的流量都是本机内部转发，算进去会把总速率抬高。
func readNetCounters() (map[string]netCounter, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]netCounter{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		if name == "" || name == "lo" {
			continue
		}
		fields := strings.Fields(line[i+1:])
		if len(fields) < 9 {
			continue
		}
		rx, e1 := strconv.ParseUint(fields[0], 10, 64)
		tx, e2 := strconv.ParseUint(fields[8], 10, 64)
		if e1 != nil || e2 != nil {
			continue
		}
		out[name] = netCounter{rx, tx}
	}
	return out, nil
}
