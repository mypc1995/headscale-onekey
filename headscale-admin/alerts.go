package main

import (
	"os"
	"strconv"
	"strings"
)

// MonitorAlert 是一条展示给用户的告警。Level 取 critical / warning / info，
// Key 用于去重和后续接入通知渠道（邮件、Webhook 等）。
type MonitorAlert struct {
	Level string `json:"level"`
	Key   string `json:"key"`
	Text  string `json:"text"`
}

// AlertThresholds 是内置规则的阈值，可通过环境变量覆盖，
// 后续接入配置文件或页面表单时也可以复用这个结构。
type AlertThresholds struct {
	CPU       float64 `json:"cpu"`
	Mem       float64 `json:"mem"`
	LoadRatio float64 `json:"load_ratio"`
}

func defaultAlertThresholds() AlertThresholds {
	return AlertThresholds{
		CPU:       envFloat("HSA_ALERT_CPU", 85, 1, 100),
		Mem:       envFloat("HSA_ALERT_MEM", 90, 1, 100),
		LoadRatio: envFloat("HSA_ALERT_LOAD", 2, 0.1, 100),
	}
}

func envFloat(key string, def, min, max float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return v
}

type alertInput struct {
	Host  HostSnapshot
	Nodes NodeSnapshot
	Lang  string
}

// alertRule 是一条独立规则。新增指标或规则时，只需实现 Eval 并注册到
// alertRules，页面渲染、接口输出与去重逻辑都不需要改动。
type alertRule struct {
	Key  string
	Eval func(alertInput, AlertThresholds) []MonitorAlert
}

var alertRules = []alertRule{
	{Key: "service", Eval: ruleServiceDown},
	{Key: "host.cpu", Eval: ruleHostCPU},
	{Key: "host.mem", Eval: ruleHostMem},
	{Key: "host.load", Eval: ruleHostLoad},
	{Key: "nodes.offline", Eval: ruleNodesOffline},
	{Key: "nodes.expired", Eval: ruleNodesExpired},
	{Key: "nodes.error", Eval: ruleNodeSampleError},
}

func evaluateAlerts(in alertInput, th AlertThresholds) []MonitorAlert {
	out := []MonitorAlert{}
	for _, r := range alertRules {
		out = append(out, r.Eval(in, th)...)
	}
	return out
}

func ruleServiceDown(in alertInput, _ AlertThresholds) []MonitorAlert {
	s := in.Nodes.Service
	if s == "" || s == "unknown" || s == "active" {
		return nil
	}
	return []MonitorAlert{{
		Level: "critical",
		Key:   "service",
		Text:  tr(in.Lang, "alert.serviceDown", s),
	}}
}

func ruleHostCPU(in alertInput, th AlertThresholds) []MonitorAlert {
	if !in.Host.Available || in.Host.CPUPercent < th.CPU {
		return nil
	}
	return []MonitorAlert{{
		Level: "warning",
		Key:   "host.cpu",
		Text:  tr(in.Lang, "alert.cpu", in.Host.CPUPercent, th.CPU),
	}}
}

func ruleHostMem(in alertInput, th AlertThresholds) []MonitorAlert {
	if !in.Host.Available || in.Host.MemPercent < th.Mem {
		return nil
	}
	return []MonitorAlert{{
		Level: "warning",
		Key:   "host.mem",
		Text:  tr(in.Lang, "alert.mem", in.Host.MemPercent, th.Mem),
	}}
}

func ruleHostLoad(in alertInput, th AlertThresholds) []MonitorAlert {
	if !in.Host.Available || in.Host.CPUCount <= 0 {
		return nil
	}
	limit := float64(in.Host.CPUCount) * th.LoadRatio
	if in.Host.Load1 <= limit {
		return nil
	}
	return []MonitorAlert{{
		Level: "warning",
		Key:   "host.load",
		Text:  tr(in.Lang, "alert.load", in.Host.Load1, in.Host.CPUCount, th.LoadRatio),
	}}
}

func ruleNodesOffline(in alertInput, _ AlertThresholds) []MonitorAlert {
	names := nodeNames(in.Nodes.Nodes, func(n NodeStat) bool { return !n.Online })
	if len(names) == 0 {
		return nil
	}
	return []MonitorAlert{{
		Level: "warning",
		Key:   "nodes.offline",
		Text:  tr(in.Lang, "alert.nodesOffline", len(names), joinNames(names, in.Lang)),
	}}
}

func ruleNodesExpired(in alertInput, _ AlertThresholds) []MonitorAlert {
	names := nodeNames(in.Nodes.Nodes, func(n NodeStat) bool { return n.State == "expired" })
	if len(names) == 0 {
		return nil
	}
	return []MonitorAlert{{
		Level: "warning",
		Key:   "nodes.expired",
		Text:  tr(in.Lang, "alert.nodeExpired", len(names), joinNames(names, in.Lang)),
	}}
}

func ruleNodeSampleError(in alertInput, _ AlertThresholds) []MonitorAlert {
	if strings.TrimSpace(in.Nodes.Error) == "" {
		return nil
	}
	return []MonitorAlert{{
		Level: "info",
		Key:   "nodes.error",
		Text:  tr(in.Lang, "alert.sampleError", in.Nodes.Error),
	}}
}

func nodeNames(nodes []NodeStat, keep func(NodeStat) bool) []string {
	out := []string{}
	for _, n := range nodes {
		if keep(n) {
			out = append(out, n.Name)
		}
	}
	return out
}

// joinNames 只列前 3 个节点，避免离线节点多时把告警撑成一行名字。
func joinNames(names []string, lang string) string {
	if len(names) > 3 {
		sep := "、"
		if lang == "en" {
			sep = ", "
		}
		return strings.Join(names[:3], sep) + sep + "…"
	}
	sep := "、"
	if lang == "en" {
		sep = ", "
	}
	return strings.Join(names, sep)
}
