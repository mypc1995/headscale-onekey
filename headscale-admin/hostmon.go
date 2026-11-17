package main

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HostPoint 是本机资源曲线上的一个采样点，CPU 与内存都是百分比。
type HostPoint struct {
	CPU float64 `json:"cpu"`
	Mem float64 `json:"mem"`
}

// HostSnapshot 是监控服务所在主机的资源快照。字段保持原始数值，
// 展示文案由 newHostView 按语言生成，便于前端复用同一份 JSON。
type HostSnapshot struct {
	Available  bool        `json:"available"`
	Error      string      `json:"error,omitempty"`
	CPUPercent float64     `json:"cpu_percent"`
	MemPercent float64     `json:"mem_percent"`
	MemTotal   uint64      `json:"mem_total"`
	MemUsed    uint64      `json:"mem_used"`
	CPUCount   int         `json:"cpu_count"`
	Load1      float64     `json:"load1"`
	Load5      float64     `json:"load5"`
	Load15     float64     `json:"load15"`
	UptimeSec  uint64      `json:"uptime_sec"`
	History    []HostPoint `json:"history"`
	Updated    string      `json:"updated"`
}

// HostMonitor 每 3 秒读取一次 /proc/stat、/proc/meminfo、/proc/loadavg，
// 用两次采样的差值算出 CPU 使用率，并保留最近 3 分钟的曲线。
type HostMonitor struct {
	mu        sync.RWMutex
	prevTotal uint64
	prevIdle  uint64
	history   []HostPoint
	snap      HostSnapshot
}

const hostHistoryLen = 60

func NewHostMonitor() *HostMonitor {
	m := &HostMonitor{}
	m.sample()
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for range t.C {
			m.sample()
		}
	}()
	return m
}

func (m *HostMonitor) sample() {
	now := time.Now()
	snap := HostSnapshot{CPUCount: runtime.NumCPU(), Updated: now.Format("15:04:05")}

	statRaw, err := os.ReadFile("/proc/stat")
	if err != nil {
		m.storeUnavailable(snap, err)
		return
	}
	total, idle, ok := parseProcStat(string(statRaw))
	if !ok {
		m.storeUnavailable(snap, fmt.Errorf("/proc/stat has no cpu line"))
		return
	}

	memRaw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		m.storeUnavailable(snap, err)
		return
	}
	memTotal, memAvail, ok := parseMeminfo(string(memRaw))
	if !ok || memTotal == 0 {
		m.storeUnavailable(snap, fmt.Errorf("/proc/meminfo is incomplete"))
		return
	}

	cpu := cpuPercent(m.prevTotal, m.prevIdle, total, idle)

	used := uint64(0)
	if memTotal > memAvail {
		used = memTotal - memAvail
	}
	memPercent := clampPercent(float64(used) * 100 / float64(memTotal))

	snap.Available = true
	snap.CPUPercent = round1(cpu)
	snap.MemPercent = round1(memPercent)
	snap.MemTotal = memTotal
	snap.MemUsed = used
	snap.Load1, snap.Load5, snap.Load15 = readLoadAvg()
	snap.UptimeSec = readUptime()

	m.mu.Lock()
	if m.prevTotal > 0 && total > m.prevTotal {
		m.history = append(m.history, HostPoint{CPU: snap.CPUPercent, Mem: snap.MemPercent})
		if len(m.history) > hostHistoryLen {
			m.history = m.history[len(m.history)-hostHistoryLen:]
		}
	}
	snap.History = append([]HostPoint{}, m.history...)
	m.prevTotal = total
	m.prevIdle = idle
	m.snap = snap
	m.mu.Unlock()
}

func (m *HostMonitor) storeUnavailable(snap HostSnapshot, err error) {
	snap.Available = false
	if err != nil {
		snap.Error = err.Error()
	}
	m.mu.Lock()
	snap.History = append([]HostPoint{}, m.history...)
	m.snap = snap
	// 下一次成功采样重新建立基线，避免跨越故障区间计算平均值。
	m.prevTotal, m.prevIdle = 0, 0
	m.mu.Unlock()
}

func (m *HostMonitor) Snapshot() HostSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.snap
	s.History = append([]HostPoint{}, s.History...)
	return s
}

// hostView 在原始快照上补充按语言格式化好的展示字段。
type hostView struct {
	HostSnapshot
	MemUsedText   string `json:"mem_used_text"`
	MemTotalText  string `json:"mem_total_text"`
	MemDetailText string `json:"mem_detail_text"`
	UptimeText    string `json:"uptime_text"`
	LoadText      string `json:"load_text"`
}

func newHostView(s HostSnapshot, lang string) hostView {
	return hostView{
		HostSnapshot:  s,
		MemUsedText:   fmtBytes(s.MemUsed),
		MemTotalText:  fmtBytes(s.MemTotal),
		MemDetailText: tr(lang, "host.memDetail", fmtBytes(s.MemUsed), fmtBytes(s.MemTotal)),
		UptimeText:    humanUptime(s.UptimeSec, lang),
		LoadText:      fmt.Sprintf("%.2f / %.2f / %.2f", s.Load1, s.Load5, s.Load15),
	}
}

// parseProcStat 从 /proc/stat 的 cpu 汇总行取总时间和空闲时间。
// 只累加前 8 个字段，guest 已包含在 user 内，避免重复计数。
func parseProcStat(s string) (total, idle uint64, ok bool) {
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		if len(fields) < 5 {
			return 0, 0, false
		}
		if len(fields) > 8 {
			fields = fields[:8]
		}
		for _, f := range fields {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			total += v
		}
		idle, _ = strconv.ParseUint(fields[3], 10, 64)
		iowait, _ := strconv.ParseUint(fields[4], 10, 64)
		return total, idle + iowait, true
	}
	return 0, 0, false
}

// parseMeminfo 返回总内存与可用内存（字节）。老内核没有 MemAvailable 时
// 退化到 MemFree + Buffers + Cached。
func parseMeminfo(s string) (total, available uint64, ok bool) {
	vals := map[string]uint64{}
	for _, line := range strings.Split(s, "\n") {
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		fields := strings.Fields(line[i+1:])
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		vals[key] = v * 1024
	}
	total = vals["MemTotal"]
	if total == 0 {
		return 0, 0, false
	}
	if v, has := vals["MemAvailable"]; has && v > 0 {
		return total, v, true
	}
	available = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	return total, available, true
}

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

// cpuPercent 用两次采样的累计值算使用率；首次采样没有基线时返回 0。
func cpuPercent(prevTotal, prevIdle, total, idle uint64) float64 {
	if prevTotal == 0 || total <= prevTotal {
		return 0
	}
	deltaTotal := total - prevTotal
	var deltaIdle uint64
	if idle > prevIdle {
		deltaIdle = idle - prevIdle
	}
	return clampPercent(float64(deltaTotal-deltaIdle) * 100 / float64(deltaTotal))
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// humanUptime 把秒数转成「3 天 4 小时」这类易读文本。
func humanUptime(sec uint64, lang string) string {
	if sec == 0 {
		return "—"
	}
	days := int(sec / 86400)
	hours := int(sec%86400) / 3600
	mins := int(sec%3600) / 60
	if lang == "en" {
		switch {
		case days > 0:
			return fmt.Sprintf("%dd %dh", days, hours)
		case hours > 0:
			return fmt.Sprintf("%dh %dm", hours, mins)
		default:
			return fmt.Sprintf("%dm", mins)
		}
	}
	switch {
	case days > 0:
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, mins)
	default:
		return fmt.Sprintf("%d 分钟", mins)
	}
}
