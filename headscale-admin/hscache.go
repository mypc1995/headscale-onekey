package main

import (
	"sync"
	"time"
)

// ttlCache 是带过期时间的值缓存。
//
// 总览页要数用户、数密钥、看最近节点，这些数据几秒内不会变；
// 每次刷新都去起一个 headscale 进程纯属浪费（实测每次 30~36ms，
// 而一次总览刷新原本要起 7 个进程）。
//
// 只缓存成功的结果：失败不缓存，否则一次偶发的失败会被缓存住，
// 页面上的数字就长时间不对了。
type ttlCache[T any] struct {
	mu  sync.Mutex
	val T
	at  time.Time
	ok  bool
}

// get 取缓存；没有或已过期则返回 false。
func (c *ttlCache[T]) get(ttl time.Duration) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ok || time.Since(c.at) > ttl {
		var zero T
		return zero, false
	}
	return c.val, true
}

// set 写入缓存并记下时间。
func (c *ttlCache[T]) set(v T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.val, c.at, c.ok = v, time.Now(), true
}
