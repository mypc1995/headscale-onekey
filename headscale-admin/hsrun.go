package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 外部命令的执行纪律。
//
// 管理界面靠调用 headscale 命令行来读写状态。这些子进程一旦卡住
// （数据库被锁住、磁盘慢、误进交互等待），旧写法会同时出两个问题：
//  1. 没有超时：请求一直挂着，直到 HTTP 的 WriteTimeout（120s）把它掐断；
//  2. 闸门一直占着：后面所有调用 headscale 的页面全部堵死，只能重启服务——
//     因为卡住的那个调用把闸门一直攥在手里，谁都进不来。
//
// 现在所有外部命令统一走这里：
//   - 每次调用都有超时，超时后杀掉整个进程组（`sh -c` 下面的孙进程也一起收）；
//   - 并发闸门可被 ctx 取消，等不到就明确报错，页面照样出得来。

const (
	// hsCallTimeout 是调用 headscale 命令行读状态的最长等待时间。
	//
	// 实测（生产 ECS，2C1.6G）：version / users list / nodes list 都在 30~36ms，
	// 15s 是几百倍的余量——超时的唯一原因是卡死，不是慢。
	hsCallTimeout = 15 * time.Second
	// hsWriteTimeout 是写操作里的校验与重启：configtest、policy check、
	// systemctl restart、递归 chown。它们要等 headscale 初始化或 systemd 收尾，给宽一点。
	hsWriteTimeout = 60 * time.Second
	// hsLocalTimeout 是 systemctl is-active / journalctl / openssl 这类本机查询。
	hsLocalTimeout = 10 * time.Second
	// hsCertbotTimeout 是 certbot renew：它要出网访问 Let's Encrypt，慢是正常的。
	hsCertbotTimeout = 180 * time.Second
)

// hsCallTimeoutEnv 是调用 headscale 命令行的超时时间，可用环境变量调短。
//
// 单位秒。测试环境用它把「卡死」的等待从 15s 压到几秒；
// 生产上一般不动——真要调短，也得先确认 headscale 在慢盘上不会超过这个值。
func hsCallTimeoutEnv() time.Duration {
	if v := strings.TrimSpace(os.Getenv("HSA_HS_CALL_TIMEOUT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return hsCallTimeout
}

// execSplitFunc 执行一个外部命令，分别返回 stdout 与 stderr。
//
// stdout 与 stderr 必须分开：headscale 的 JSON 结果走 stdout，
// 提示与报错走 stderr。合并两者会把提示混进 JSON 里，解析直接失败。
type execSplitFunc func(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)

// runExec 是真实的子进程执行：进程必须自己看着 ctx 走。
//
// 超时由调用方施加（见 execCombined 与 HSClient.runCtx），
// 这样「等闸门」和「执行」共用同一个预算，不会出现等锁等掉了超时、
// 命令刚起来就被掐的情况。
func runExec(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// 独立成组：超时时能把 `sh -c` 下面的 openssl、find 这些孙进程一起收掉，
	// 否则它们会变成孤儿继续跑，文件也一直锁着。
	setProcAttr(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	// 万一有孙进程还攥着 stdout 管道不放，最多再等 3 秒就强行收尾，
	// 不能让 Wait 因为一条管道永远挂着。
	cmd.WaitDelay = 3 * time.Second

	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return out.String(), errBuf.String(), err
}

// execRunner 是实际执行子进程的入口。
//
// 单独拎出来是为了让测试能替换成「卡死的假执行器」，
// 不用真的去睡一个子进程来测超时。
var execRunner = runExec

// execCombined 在给定时限内执行命令并合并 stdout 与 stderr。
//
// 只用在「输出是给人看的」地方（systemctl、journalctl、openssl）。
// 需要解析 JSON 的地方必须走 execSplit。
func execCombined(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = hsLocalTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, errOut, err := execRunner(ctx, name, args...)
	merged := out + errOut
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return merged, fmt.Errorf("%s 执行超时（超过 %s）", filepath.Base(name), timeout)
		}
		return merged, err
	}
	return merged, nil
}

// hsGate 是外部命令的并发闸门：一次只放一个 headscale 调用进去。
//
// 原来是一把 sync.Mutex，只能干等。持有者卡住时锁永远不放，
// 后面所有调用 headscale 的请求全部堵死，界面整个僵住。
// 换成可取消的闸门之后：等不到就返回「headscale 忙」，页面照样出得来。
type hsGate struct {
	once sync.Once
	ch   chan struct{}
}

func (g *hsGate) init() {
	g.once.Do(func() { g.ch = make(chan struct{}, 1) })
}

// acquire 拿到闸门。ctx 一旦取消或超时，返回错误而不是继续等。
func (g *hsGate) acquire(ctx context.Context) error {
	g.init()
	select {
	case g.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("headscale 忙：另一个操作还没结束，请稍后重试")
		}
		return fmt.Errorf("headscale 忙（请求已取消）: %w", ctx.Err())
	}
}

// release 归还闸门。
//
// 用带默认分支的接收：闸门计数一旦错乱，这里宁可什么都不做，
// 也不能把调用方永远卡住。
func (g *hsGate) release() {
	g.init()
	select {
	case <-g.ch:
	default:
	}
}

// systemctlTimeout 按命令类型给不同的等待时间：
// 重启要等 systemd 收尾，查询几乎立刻返回。
func systemctlTimeout(args ...string) time.Duration {
	for _, a := range args {
		switch a {
		case "restart", "stop", "start", "reload":
			return hsWriteTimeout
		}
	}
	return hsLocalTimeout
}
