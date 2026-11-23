package main

import (
	"reflect"
	"testing"
)

// headscale 的 available_routes 是节点公告的**全部**路由（含已批准的），
// approved_routes 才是已批准的那部分。待批准必须做差集，否则批准过的路由
// 会一直挂在审批列表里——这正是「审批完还在待审批」的根因。

func TestPendingRoutesExcludesApproved(t *testing.T) {
	got := pendingRoutes(
		[]string{"10.0.0.0/8", "192.168.1.0/24"},
		[]string{"10.0.0.0/8"},
	)
	if want := []string{"192.168.1.0/24"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pendingRoutes = %v，期望 %v", got, want)
	}
}

func TestPendingRoutesAllWhenNoneApproved(t *testing.T) {
	avail := []string{"10.0.0.0/8", "192.168.1.0/24"}
	got := pendingRoutes(avail, nil)
	if want := []string{"10.0.0.0/8", "192.168.1.0/24"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pendingRoutes = %v，期望 %v", got, want)
	}
	// 不能把调用方的切片直接返回出去，否则改返回值会改到原数据。
	got[0] = "9.9.9.9/32"
	if avail[0] != "10.0.0.0/8" {
		t.Fatalf("pendingRoutes 改动了入参切片：%v", avail)
	}
}

func TestPendingRoutesEmptyWhenAllApproved(t *testing.T) {
	got := pendingRoutes([]string{"192.168.1.0/24"}, []string{"192.168.1.0/24"})
	if len(got) != 0 {
		t.Fatalf("全部已批准时 pendingRoutes = %v，期望空", got)
	}
}

func TestPendingRoutesNormalizesCIDR(t *testing.T) {
	// 同一个网段写成不同形式时不能被当成两条路由。
	got := pendingRoutes([]string{"192.168.1.7/24"}, []string{"192.168.1.0/24"})
	if len(got) != 0 {
		t.Fatalf("同一网段的不同写法没有归一化：%v", got)
	}
}

func TestMergeRouteListsKeepsBase(t *testing.T) {
	// headscale 的 approve-routes 是全量覆盖语义：新批的路由必须跟原有
	// 已批准的取并集，否则会把之前批准的抹掉。
	got := mergeRouteLists(
		[]string{"10.0.0.0/8"},
		[]string{"192.168.1.0/24"},
	)
	if want := []string{"10.0.0.0/8", "192.168.1.0/24"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeRouteLists = %v，期望 %v", got, want)
	}
}

func TestMergeRouteListsDedupes(t *testing.T) {
	got := mergeRouteLists(
		[]string{"10.0.0.0/8"},
		[]string{"10.0.0.0/8", "192.168.1.0/24", "192.168.1.0/24"},
	)
	if want := []string{"10.0.0.0/8", "192.168.1.0/24"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeRouteLists = %v，期望 %v", got, want)
	}
}

func TestParseRouteList(t *testing.T) {
	got, err := parseRouteList("10.0.0.0/8, 192.168.1.0/24")
	if err != nil {
		t.Fatalf("parseRouteList 报错: %v", err)
	}
	if want := []string{"10.0.0.0/8", "192.168.1.0/24"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRouteList = %v，期望 %v", got, want)
	}
}

func TestParseRouteListEmpty(t *testing.T) {
	got, err := parseRouteList("   ")
	if err != nil {
		t.Fatalf("parseRouteList 报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("空输入应得到空列表，实际 %v", got)
	}
}

func TestParseRouteListRejectsInvalid(t *testing.T) {
	// 非法输入必须报错，不能静默忽略——否则用户以为审批成功了。
	if _, err := parseRouteList("not-a-cidr"); err == nil {
		t.Fatal("非法路由没有被拒绝")
	}
}

func TestApprovedRoutesOfNode(t *testing.T) {
	nodes := []map[string]any{
		{"id": float64(1), "approved_routes": []any{"10.0.0.0/8"}},
		{"id": float64(2), "approved_routes": []any{}},
	}
	got := approvedRoutesOfNode(nodes, 2)
	if len(got) != 0 {
		t.Fatalf("节点 2 没有已批准路由，实际 %v", got)
	}
	got = approvedRoutesOfNode(nodes, 1)
	if want := []string{"10.0.0.0/8"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("approvedRoutesOfNode = %v，期望 %v", got, want)
	}
	if got := approvedRoutesOfNode(nodes, 99); len(got) != 0 {
		t.Fatalf("找不到的节点应返回空，实际 %v", got)
	}
}

func TestNodeIDOf(t *testing.T) {
	cases := []struct {
		in   any
		want uint64
	}{
		{float64(7), 7},
		{int64(8), 8},
		{"9", 9},
		{nil, 0},
	}
	for _, c := range cases {
		got := nodeIDOf(map[string]any{"id": c.in})
		if got != c.want {
			t.Fatalf("nodeIDOf(%v) = %d，期望 %d", c.in, got, c.want)
		}
	}
}
