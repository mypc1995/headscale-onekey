package main

import (
	"sync"
	"time"
)

// joinCommand 拼接客户端接入命令。server_url 或密钥缺任一时返回空串，
// 由前端展示占位提示，避免拼出半截可执行的命令。
func joinCommand(serverURL, key string) string {
	if serverURL == "" || key == "" {
		return ""
	}
	return "tailscale up --login-server=" + serverURL + " --authkey=" + key
}

// joinKeyEntry 是暂存的一条新生成密钥。
type joinKeyEntry struct {
	key     string
	expires time.Time
}

// joinKeyStore 按会话暂存刚创建的预授权密钥，让重定向后的密钥页
// 能自动把密钥填入接入命令。密钥只取一次（一次性展示），
// 纯内存保存，进程重启即失效——与「密钥仅显示一次」的语义一致。
type joinKeyStore struct {
	mu    sync.Mutex
	items map[string]joinKeyEntry
}

func newJoinKeyStore() *joinKeyStore {
	return &joinKeyStore{items: map[string]joinKeyEntry{}}
}

// Set 记录某会话最新生成的密钥，30 分钟内有效。
func (s *joinKeyStore) Set(token, key string) {
	if token == "" || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[token] = joinKeyEntry{key: key, expires: time.Now().Add(30 * time.Minute)}
}

// Take 取出并立即清除该会话暂存的密钥；没有或已过期返回空串。
func (s *joinKeyStore) Take(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[token]
	if !ok {
		return ""
	}
	delete(s.items, token)
	if time.Now().After(e.expires) {
		return ""
	}
	return e.key
}
