package main

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// 路由的三个集合关系（headscale v0.23+ 的语义，实测 v0.29.7 一致）：
//
//	available_routes —— 节点公告的全部路由，**包含已批准的**；
//	approved_routes  —— 已被管理员批准的那部分；
//	待批准           —— available − approved。
//
// 早期版本把 available 直接当成「还没批准的」，于是批准过的路由会一直挂在
// 审批列表里，页面上表现为「审批完还在待审批」。所有展示待审批的地方都必须
// 先做差集，提交审批时也必须按全量覆盖的语义处理。

// routeKey 归一化路由字符串，用于集合比较：能解析成 CIDR 的按掩码后的标准
// 写法比，解析不了的按原样比，避免 192.168.100.0/24 和 192.168.100.1/24
// 被当成两条不同的路由。
func routeKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked().String()
	}
	return s
}

// pendingRoutes 返回还没被批准的路由：available 减去 approved。
// approved 为空时直接返回 available 的副本，避免调用方改到原切片。
func pendingRoutes(available, approved []string) []string {
	out := []string{}
	if len(approved) == 0 {
		for _, r := range available {
			if strings.TrimSpace(r) != "" {
				out = append(out, r)
			}
		}
		return out
	}
	done := make(map[string]bool, len(approved))
	for _, r := range approved {
		done[routeKey(r)] = true
	}
	for _, r := range available {
		if strings.TrimSpace(r) == "" {
			continue
		}
		if !done[routeKey(r)] {
			out = append(out, r)
		}
	}
	return out
}

// mergeRouteLists 合并两个路由集合：保留原有顺序并去重。
//
// headscale nodes approve-routes 是全量覆盖语义（传空串表示清空已批准路由），
// 所以提交审批时必须带上节点原有的已批准集合，否则会把之前批准的抹掉。
func mergeRouteLists(base, extra []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, list := range [][]string{base, extra} {
		for _, r := range list {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			k := routeKey(r)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

// parseRouteList 解析表单里的路由列表：逗号、空格或分号分隔，每项必须是合法
// 的 CIDR。非法输入直接报错，不静默忽略——否则用户会以为审批已经生效。
func parseRouteList(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r', '，', '；':
			return true
		}
		return false
	})
	out := []string{}
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if _, err := netip.ParsePrefix(f); err != nil {
			return nil, fmt.Errorf("无效的路由: %s", f)
		}
		out = append(out, f)
	}
	return out, nil
}

// nodeIDOf 从 headscale 的节点 JSON 里取节点 ID。
// protojson 里 ID 通常是数字，但不同版本可能序列化成字符串，两种都要认。
func nodeIDOf(m map[string]any) uint64 {
	switch t := m["id"].(type) {
	case float64:
		return uint64(t)
	case int64:
		return uint64(t)
	case uint64:
		return t
	case string:
		id, _ := strconv.ParseUint(strings.TrimSpace(t), 10, 64)
		return id
	}
	id, _ := strconv.ParseUint(strings.Trim(str(m["id"]), `"`), 10, 64)
	return id
}
