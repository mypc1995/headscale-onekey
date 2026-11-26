package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHSRunTimesOut 验证 headscale 卡住时调用会超时返回，而不是永远挂着。
//
// 这是本次改造的核心：旧实现没有超时，headscale 一卡住，
// 请求挂到 HTTP 超时，而闸门一直被攥着，后面所有页面全堵死。
func TestHSRunTimesOut(t *testing.T) {
	c := &HSClient{Bin: "/usr/bin/headscale", timeout: 300 * time.Millisecond}
	c.exec = func(ctx context.Context, name string, args ...string) (string, string, error) {
		<-ctx.Done() // 模拟卡死的 headscale
		return "", "", ctx.Err()
	}
	start := time.Now()
	_, err := c.run("nodes", "list")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("卡死的调用必须返回错误")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("错误信息应说明是超时，得到 %q", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("超时没生效，耗时 %v", elapsed)
	}
}

// TestHSGateReleasedAfterTimeout 验证超时之后闸门被还回去了。
//
// 闸门漏还一次，后面所有调用 headscale 的请求就全被堵死——
// 这正是旧实现最要命的那个后果。
func TestHSGateReleasedAfterTimeout(t *testing.T) {
	c := &HSClient{Bin: "/usr/bin/headscale", timeout: 300 * time.Millisecond}
	hang := true
	c.exec = func(ctx context.Context, name string, args ...string) (string, string, error) {
		if hang {
			<-ctx.Done()
			return "", "", ctx.Err()
		}
		return `[]`, "", nil
	}
	if _, err := c.run("nodes", "list"); err == nil {
		t.Fatal("卡死的调用应当返回错误")
	}
	// 卡死过一次之后，闸门必须已经归还：下一次调用要能正常完成。
	hang = false
	done := make(chan error, 1)
	go func() {
		_, err := c.run("users", "list")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("超时之后的调用不该失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时之后闸门没有归还，后续调用被堵死了")
	}
}

// TestHSGateWaitIsBounded 验证闸门被占住时，后来的调用会及时报「忙」。
//
// 不能让后来的调用无限等：一个卡死的调用已经够糟了，
// 不能再把后面所有人都拖下水。
func TestHSGateWaitIsBounded(t *testing.T) {
	c := &HSClient{Bin: "/usr/bin/headscale", timeout: 5 * time.Second}
	release := make(chan struct{})
	c.exec = func(ctx context.Context, name string, args ...string) (string, string, error) {
		<-release
		return `[]`, "", nil
	}
	// 第一个调用占住闸门。
	go func() { _, _ = c.run("nodes", "list") }()
	time.Sleep(150 * time.Millisecond)
	// 第二个调用带自己的短截止时间：必须在它到期前报「忙」。
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.runCtx(ctx, "users", "list")
	elapsed := time.Since(start)
	close(release)
	if err == nil {
		t.Fatal("闸门被占住时，第二个调用应当返回错误")
	}
	if !strings.Contains(err.Error(), "忙") {
		t.Fatalf("错误信息应说明是 headscale 忙，得到 %q", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("等待闸门没有上界，耗时 %v", elapsed)
	}
}

// TestHSRunSurfacesCLIError 验证 headscale 报错时把 stderr 的内容带出来。
func TestHSRunSurfacesCLIError(t *testing.T) {
	c := &HSClient{Bin: "/usr/bin/headscale"}
	c.exec = func(ctx context.Context, name string, args ...string) (string, string, error) {
		return "", "database is locked", errors.New("exit status 1")
	}
	_, err := c.run("nodes", "list")
	if err == nil {
		t.Fatal("CLI 失败应当返回错误")
	}
	if !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("错误信息应包含 stderr 内容，得到 %q", err)
	}
}

// TestHSRunKeepsStdoutClean 验证 stdout 与 stderr 分开：提示不能混进 JSON。
//
// headscale 的 JSON 走 stdout、提示走 stderr。合并两者的话，
// 页面会因为 JSON 解析失败而显示不出数据。
func TestHSRunKeepsStdoutClean(t *testing.T) {
	c := &HSClient{Bin: "/usr/bin/headscale"}
	c.exec = func(ctx context.Context, name string, args ...string) (string, string, error) {
		return `[{"id":1,"name":"default"}]`, "warning: something", nil
	}
	out, err := c.run("users", "list")
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if strings.Contains(out, "warning") {
		t.Fatalf("stderr 的提示混进了 stdout: %q", out)
	}
	v, err := c.runJSON("users", "list")
	if err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if len(asSlice(v)) != 1 {
		t.Fatalf("解析结果不对: %v", v)
	}
}

// TestHSRunConcurrent 验证并发调用会被闸门串行化，不会互相踩。
func TestHSRunConcurrent(t *testing.T) {
	c := &HSClient{Bin: "/usr/bin/headscale"}
	var mu sync.Mutex
	inside := 0
	maxInside := 0
	c.exec = func(ctx context.Context, name string, args ...string) (string, string, error) {
		mu.Lock()
		inside++
		if inside > maxInside {
			maxInside = inside
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		inside--
		mu.Unlock()
		return `[]`, "", nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.run("nodes", "list"); err != nil {
				t.Errorf("并发调用不该失败: %v", err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if maxInside > 1 {
		t.Fatalf("闸门没起作用，最多同时有 %d 个调用在里面", maxInside)
	}
}

// TestHSCallTimeoutEnv 验证超时可以用环境变量调短。
func TestHSCallTimeoutEnv(t *testing.T) {
	t.Setenv("HSA_HS_CALL_TIMEOUT", "7")
	if got := hsCallTimeoutEnv(); got != 7*time.Second {
		t.Fatalf("环境变量没生效: %v", got)
	}
	// 非法值要回落到默认值，不能变成 0（0 会被当成「没有超时」）。
	t.Setenv("HSA_HS_CALL_TIMEOUT", "abc")
	if got := hsCallTimeoutEnv(); got != hsCallTimeout {
		t.Fatalf("非法值应回落到默认值: %v", got)
	}
	t.Setenv("HSA_HS_CALL_TIMEOUT", "0")
	if got := hsCallTimeoutEnv(); got != hsCallTimeout {
		t.Fatalf("0 应回落到默认值: %v", got)
	}
}

// TestExecCombinedTimeout 验证命令超时会返回可读的「超时」提示。
func TestExecCombinedTimeout(t *testing.T) {
	old := execRunner
	defer func() { execRunner = old }()
	execRunner = func(ctx context.Context, name string, args ...string) (string, string, error) {
		<-ctx.Done()
		return "", "", ctx.Err()
	}
	start := time.Now()
	_, err := execCombined(context.Background(), 300*time.Millisecond, "headscale", "nodes", "list")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("卡死的命令应当超时返回错误")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("错误信息应说明是超时，得到 %q", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("超时没生效，耗时 %v", elapsed)
	}
}

// TestExecKillsChildProcess 验证超时之后子进程真的被杀了。
//
// 不杀的话，它会变成孤儿继续跑，文件也一直锁着。
// 这里用测试二进制自己当被执行的命令：子进程会一直睡。
func TestExecKillsChildProcess(t *testing.T) {
	if os.Getenv("HSA_TEST_HELPER_SLEEP") == "1" {
		if pf := os.Getenv("HSA_TEST_HELPER_PIDFILE"); pf != "" {
			os.WriteFile(pf, []byte(strconv.Itoa(os.Getpid())), 0o644)
		}
		time.Sleep(5 * time.Minute)
		return
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestExecKillsChildProcess")
	cmd.Env = append(os.Environ(), "HSA_TEST_HELPER_SLEEP=1", "HSA_TEST_HELPER_PIDFILE="+pidFile)
	setProcAttr(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err == nil {
		t.Fatal("睡死的子进程应当返回错误")
	}
	// Linux 上再确认一下：子进程确实被收走了。
	b, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		return // 子进程没来得及写 pid 文件，说明它压根没起来，结论一致
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err != nil {
			return // /proc 下没有了：进程已被回收
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("超时之后子进程 %d 还活着：超时必须把它杀掉，不能留成孤儿", pid)
}
