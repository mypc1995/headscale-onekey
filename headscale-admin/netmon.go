package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type NetPoint struct {
	Rx float64 `json:"rx"` // KB/s
	Tx float64 `json:"tx"`
}

type NetIface struct {
	Name     string  `json:"name"`
	RxRate   float64 `json:"rx_rate"` // KB/s
	TxRate   float64 `json:"tx_rate"`
	RxTotal  uint64  `json:"rx_total"` // bytes
	TxTotal  uint64  `json:"tx_total"`
}

type NetSnapshot struct {
	Available bool       `json:"available"`
	Ifaces    []NetIface `json:"ifaces"`
	History   []NetPoint `json:"history"`
	TotalRx   float64    `json:"total_rx"`
	TotalTx   float64    `json:"total_tx"`
	TotalRxBytes uint64  `json:"total_rx_bytes"`
	TotalTxBytes uint64  `json:"total_tx_bytes"`
	Updated   string     `json:"updated"`
}

type netCounter struct{ rx, tx uint64 }

// NetMonitor 每 2 秒采样一次 /proc/net/dev，计算实时速率并保留最近 60 个点。
type NetMonitor struct {
	mu       sync.RWMutex
	last     map[string]netCounter
	lastTime time.Time
	history  []NetPoint
	snap     NetSnapshot
}

func NewNetMonitor() *NetMonitor {
	m := &NetMonitor{last: map[string]netCounter{}}
	m.sample()
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for range t.C {
			m.sample()
		}
	}()
	return m
}

func (m *NetMonitor) sample() {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		m.mu.Lock()
		m.snap = NetSnapshot{Available: false, Ifaces: []NetIface{}, History: []NetPoint{}}
		m.mu.Unlock()
		return
	}
	defer f.Close()

	now := time.Now()
	cur := map[string]netCounter{}
	snap := NetSnapshot{Available: true, Ifaces: []NetIface{}, Updated: now.Format("15:04:05")}
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
		cur[name] = netCounter{rx, tx}
		var rateRx, rateTx float64
		if prev, ok := m.last[name]; ok && !m.lastTime.IsZero() {
			if sec := now.Sub(m.lastTime).Seconds(); sec > 0 {
				if rx >= prev.rx {
					rateRx = float64(rx-prev.rx) / sec / 1024
				}
				if tx >= prev.tx {
					rateTx = float64(tx-prev.tx) / sec / 1024
				}
			}
		}
		snap.Ifaces = append(snap.Ifaces, NetIface{Name: name, RxRate: rateRx, TxRate: rateTx, RxTotal: rx, TxTotal: tx})
		snap.TotalRx += rateRx
		snap.TotalTx += rateTx
		snap.TotalRxBytes += rx
		snap.TotalTxBytes += tx
	}

	m.mu.Lock()
	if !m.lastTime.IsZero() && len(m.last) > 0 {
		m.history = append(m.history, NetPoint{Rx: snap.TotalRx, Tx: snap.TotalTx})
		if len(m.history) > 60 {
			m.history = m.history[len(m.history)-60:]
		}
	}
	snap.History = append([]NetPoint(nil), m.history...)
	m.last = cur
	m.lastTime = now
	m.snap = snap
	m.mu.Unlock()
}

func (m *NetMonitor) Snapshot() NetSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.snap
	s.History = append([]NetPoint(nil), s.History...)
	s.Ifaces = append([]NetIface(nil), s.Ifaces...)
	return s
}
