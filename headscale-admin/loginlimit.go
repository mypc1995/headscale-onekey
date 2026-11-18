package main

import (
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
// 这里按「账号名」和「来源 IP」两个维度分别计数：
//   - 账号名维度用延迟而不是锁定：猜错多次后每次登录都要等一下，
//     爆破速度降下来，但不会把真正的管理员永久锁在门外；
//   - IP 维度直接锁定：攻击者换 IP 继续猜时，这条线是兜底。

const (
	// loginWindow 是失败计数的有效期，超过这个时间的失败不再累计。
	loginWindow = 15 * time.Minute
	// loginUserThreshold 是账号名维度开始加延迟的失败次数。
	loginUserThreshold = 5
	// loginUserDelay 是账号名维度每次登录要额外等待的时间。
	loginUserDelay = 2 * time.Second
	// loginUserMaxDelay 是额外等待的上限，避免越猜越慢到影响正常使用。
	loginUserMaxDelay = 10 * time.Second
	// loginIPThreshold 是来源 IP 维度触发锁定的失败次数。
	loginIPThreshold = 20
	// loginIPLock 是来源 IP 被锁定的时长。
	loginIPLock = 5 * time.Minute
	// loginMaxKeys 是记录条数上限，防止攻击者用海量账号名/IP 把内存撑爆。
	loginMaxKeys = 4096
)

// loginCounter 记录某个键（账号名或 IP）的失败情况。
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
}

func newLoginGuard() *loginGuard {
	return &loginGuard{
		users: map[string]*loginCounter{},
		ips:   map[string]*loginCounter{},
	}
}

// penalty 返回这次登录要额外等待多久。
// 返回值大于 0 表示需要先等待；locked 为 true 表示该来源已被锁定，
// 调用方应当直接拒绝这次请求。
func (g *loginGuard) penalty(user, ip string) (wait time.Duration, locked bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()

	if c := g.ips[ip]; c != nil && now.Before(c.until) {
		return 0, true
	}

	c := g.counter(g.users, user, now)
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
	now := time.Now()

	cu := g.counter(g.users, user, now)
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

// success 登录成功后清掉该账号与该来源的失败计数。
func (g *loginGuard) success(user, ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.users, user)
	delete(g.ips, ip)
}

// counter 取出（或新建）某个键的计数器，并顺带清理过期条目。
func (g *loginGuard) counter(m map[string]*loginCounter, key string, now time.Time) *loginCounter {
	if len(m) >= loginMaxKeys {
		g.purge(m, now)
	}
	c := m[key]
	if c == nil {
		if len(m) >= loginMaxKeys {
			// 清理之后还是超上限就不再记这个键：放弃这条计数不会让
			// 爆破变容易，因为 IP 维度仍然在记。
			return &loginCounter{}
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

// purge 清掉已经超出有效期的计数条目。
func (g *loginGuard) purge(m map[string]*loginCounter, now time.Time) {
	for k, c := range m {
		if now.Sub(c.seen) > loginWindow && now.After(c.until) {
			delete(m, k)
		}
	}
}
