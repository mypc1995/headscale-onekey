package main

import (
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// 登录限速。
//
// 管理界面的口令用 PBKDF2-HMAC-SHA256 120000 轮存储，校验一次要几十毫秒 CPU。
// 没有限制的话有两个问题：
//   - 攻击者可以在网上逐个猜口令（在线爆破），没有任何退避；
//   - KDF 计算不在数据库锁里，多个请求会并发跑，把整台机器的 CPU 打满——
//     这台机器同时还跑着 headscale 控制面。
//
// 这里按两个维度分别计数：
//   - 「来源 IP + 账号名」维度用延迟：猜错多次后，这个来源对这个账号的
//     登录要等一下，爆破速度降下来，但不会把真正的管理员永久锁在门外；
//   - 来源 IP 维度直接锁定：攻击者换账号名继续猜时，这条线是兜底。
//
// 两个维度都踩过的坑（2026-11-23 安全审计）：
//
//  1. 账号维度的键**不能只用账号名**。只按账号名记的话，任何匿名攻击者
//     持续对某个账号失败一次，就能让**所有来源**（包括管理员自己的）
//     每次登录都多等 10 秒——这是可被用来定向拖慢运维登录的拒绝服务。
//     所以键必须是「来源 + 账号」的组合。
//
//  2. 登录成功**不能清掉来源 IP 的计数**。旧实现里 success() 把整个 IP
//     的计数连同锁定标记一起删了，于是攻击者只要用自己那个合法账号登录
//     一次，就能把 IP 维度的限速状态洗白，锁定永远触发不了。
//     现在 success() 只清「来源 + 账号」这一条。
//
// 另外，硬锁是按 IP 做的，共享同一出口的其他人（NAT / 反代 / 公司统一出口）
// 会被一起锁住。根治办法是把可信来源写进 HSA_LOGIN_TRUSTED_IPS：
// 这些来源既不累计也不锁定。

const (
	// loginWindow 是失败计数的有效期，超过这个时间的失败不再累计。
	loginWindow = 15 * time.Minute
	// loginUserThreshold 是「来源 + 账号」维度开始加延迟的失败次数。
	loginUserThreshold = 5
	// loginUserDelay 是延迟的起步值，每多失败一次再加一个同样的量。
	loginUserDelay = 2 * time.Second
	// loginUserMaxDelay 是额外等待的上限，避免越猜越慢到影响正常使用。
	loginUserMaxDelay = 10 * time.Second
	// loginIPThreshold 是来源 IP 维度触发锁定的失败次数。
	loginIPThreshold = 30
	// loginIPLock 是来源 IP 被锁定的时长。
	//
	// 旧值是 5 分钟：共享出口的场景下，一次误触就要让所有人等 5 分钟。
	// 缩短到 2 分钟既能显著降低误伤，又不影响防爆破——因为锁定期间的
	// 请求根本不会跑 KDF，速率照样被压住。
	loginIPLock = 2 * time.Minute
	// loginMaxKeys 是记录条数上限，防止攻击者用海量账号名/IP 把内存撑爆。
	//
	// 旧值 4096 太小：攻击者用 4096 个垃圾用户名就能把表填满。
	// 现在提到 16384（约 800KB 内存），且满了以后改为淘汰最老的一条，
	// 而不是「不再记这个键」——后者会让真实目标的计数彻底丢失。
	loginMaxKeys = 16384
	// loginTrustedEnv 是可信来源网段的环境变量名。
	//
	// 形如 192.168.1.0/24,10.0.0.5；这些来源既不累计失败也不被锁定。
	loginTrustedEnv = "HSA_LOGIN_TRUSTED_IPS"
)

// loginCounter 记录某个键的失败情况。
type loginCounter struct {
	// count 是有效期内累计的失败次数。
	count int
	// until 是锁定到期时间，零值表示没锁定。
	until time.Time
	// seen 是最近一次失败的时间。
	seen time.Time
}

// loginGuard 记录登录失败并给出惩罚。
type loginGuard struct {
	mu    sync.Mutex
	users map[string]*loginCounter
	ips   map[string]*loginCounter
	// trusted 是可信来源网段：这些来源既不累计失败，也不被锁定。
	trusted []*net.IPNet
}

func newLoginGuard() *loginGuard {
	return &loginGuard{
		users:   map[string]*loginCounter{},
		ips:     map[string]*loginCounter{},
		trusted: loadTrustedNets(os.Getenv(loginTrustedEnv)),
	}
}

// loginKey 把来源 IP 与账号名合成一个键。
//
// 账号维度的惩罚必须按「来源 + 账号」记，不能只按账号名记——
// 只按账号名记的话，匿名攻击者持续对某个账号失败一次，就能让所有来源
// （包括管理员自己的）每次登录都多等 10 秒。
func loginKey(ip, user string) string {
	return ip + "\x00" + user
}

// loadTrustedNets 解析可信来源列表。每一项可以是单个 IP，也可以是 CIDR。
func loadTrustedNets(raw string) []*net.IPNet {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []*net.IPNet
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.Contains(item, "/") {
			ip := net.ParseIP(item)
			if ip == nil {
				continue
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		if _, n, err := net.ParseCIDR(item); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// trustedSource 判断这个来源是不是可信的。
func (g *loginGuard) trustedSource(ip string) bool {
	if len(g.trusted) == 0 {
		return false
	}
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	for _, n := range g.trusted {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

// penalty 返回这次登录要额外等待多久。
// 返回值大于 0 表示需要先等待；locked 为 true 表示该来源已被锁定，
// 调用方应当直接拒绝这次请求。
//
// 可信来源既不等待也不锁定。
func (g *loginGuard) penalty(user, ip string) (wait time.Duration, locked bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.trustedSource(ip) {
		return 0, false
	}
	now := time.Now()

	if c := g.ips[ip]; c != nil && now.Before(c.until) {
		return 0, true
	}

	c := g.counter(g.users, loginKey(ip, user), now)
	if c.count >= loginUserThreshold {
		step := c.count - loginUserThreshold
		d := loginUserDelay + time.Duration(step)*loginUserDelay
		if d > loginUserMaxDelay {
			d = loginUserMaxDelay
		}
		return d, false
	}
	return 0, false
}

// fail 记录一次登录失败。
func (g *loginGuard) fail(user, ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.trustedSource(ip) {
		return
	}
	now := time.Now()

	cu := g.counter(g.users, loginKey(ip, user), now)
	cu.count++
	cu.seen = now

	ci := g.counter(g.ips, ip, now)
	ci.count++
	ci.seen = now
	if ci.count >= loginIPThreshold {
		ci.until = now.Add(loginIPLock)
		ci.count = 0
	}
}

// success 登录成功后清掉「来源 + 账号」这一条的计数。
//
// 刻意**不**清来源 IP 的计数：那一条代表的是「这个来源打错过多少次口令」，
// 跟这次登录的是哪个账号无关。旧实现在这里把整个 IP 的计数连同锁定标记
// 一起删了，攻击者用自己那个合法账号登录一次就能把限速状态洗白。
func (g *loginGuard) success(user, ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.users, loginKey(ip, user))
}

// counter 取出（或新建）某个键的计数器，并顺带清理过期条目。
func (g *loginGuard) counter(m map[string]*loginCounter, key string, now time.Time) *loginCounter {
	c := m[key]
	if c == nil {
		if len(m) >= loginMaxKeys {
			g.purge(m, now)
			if len(m) >= loginMaxKeys {
				g.evictOldest(m)
			}
		}
		c = &loginCounter{}
		m[key] = c
		return c
	}
	if now.Sub(c.seen) > loginWindow {
		c.count = 0
	}
	return c
}

// evictOldest 在表满时淘汰最久没活动的一条，保证新键仍能被计数。
//
// 旧实现是「表满就不再记这个键」：攻击者用一批垃圾用户名把表填满后，
// 真实目标（admin）的失败计数就再也记不上，账号维度的惩罚直接归零。
// 改成淘汰最老的一条之后，攻击者要驱逐目标账号的计数，得先付出同样数量的
// 失败请求（每次都要跑一遍 KDF，还要吃自己的限速），代价高到不划算。
func (g *loginGuard) evictOldest(m map[string]*loginCounter) {
	oldestKey := ""
	var oldest time.Time
	for k, c := range m {
		if oldestKey == "" || c.seen.Before(oldest) {
			oldestKey, oldest = k, c.seen
		}
	}
	if oldestKey != "" {
		delete(m, oldestKey)
	}
}

// purge 清掉已经超出有效期的计数条目。
func (g *loginGuard) purge(m map[string]*loginCounter, now time.Time) {
	for k, c := range m {
		if now.Sub(c.seen) > loginWindow && now.After(c.until) {
			delete(m, k)
		}
	}
}
