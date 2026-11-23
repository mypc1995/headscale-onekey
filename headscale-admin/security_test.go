package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// newTestStore 建一个临时数据库，返回 store 与清理函数。
func newTestStore(t *testing.T) (*AuthStore, func()) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "app.db"), filepath.Join(dir, "admins.json"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	return store, func() { store.DB.Close() }
}

// TestSessionRoleFollowsAdminsTable 验证会话里的角色取自 admins 表的实时值。
//
// 这条守着「降权不生效」这个高危缺陷：会话表里存的是登录时的角色快照，
// 如果直接拿来判权，管理员把账号降为只读之后，那个账号已登录的会话
// 还握着 admin 继续写，撤权形同虚设。
func TestSessionRoleFollowsAdminsTable(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	if err := store.CreateAdmin("bob", "password123", "admin"); err != nil {
		t.Fatal(err)
	}
	m := NewSessionManager(store.DB, false)
	s := m.Create("bob", "admin")

	got, ok := m.Get(s.Token)
	if !ok {
		t.Fatal("刚创建的会话应能取到")
	}
	if got.Role != "admin" {
		t.Fatalf("role = %q，期望 admin", got.Role)
	}

	// 直接改库模拟降权（不等 SetRole 的级联吊销），会话读到的角色必须跟着变。
	if _, err := store.DB.Exec(`UPDATE admins SET role='readonly' WHERE username='bob'`); err != nil {
		t.Fatal(err)
	}
	got, ok = m.Get(s.Token)
	if !ok {
		t.Fatal("会话不应因角色变化而消失（吊销由 SetRole 负责）")
	}
	if got.Role != "readonly" {
		t.Fatalf("role = %q，期望实时值 readonly", got.Role)
	}
}

// TestSetRoleRevokesSessions 验证降权会立刻吊销该账号的已有会话。
func TestSetRoleRevokesSessions(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	if err := store.CreateAdmin("bob", "password123", "admin"); err != nil {
		t.Fatal(err)
	}
	m := NewSessionManager(store.DB, false)
	s := m.Create("bob", "admin")
	if _, ok := m.Get(s.Token); !ok {
		t.Fatal("会话应存在")
	}

	if err := store.SetRole("bob", "readonly"); err != nil {
		t.Fatalf("降权失败: %v", err)
	}
	if _, ok := m.Get(s.Token); ok {
		t.Fatal("降权后旧会话必须失效，否则它还带着 admin 角色继续写")
	}
}

// TestChangePasswordRevokesSessions 验证改密会吊销该账号的已有会话。
func TestChangePasswordRevokesSessions(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	if err := store.CreateAdmin("bob", "password123", "admin"); err != nil {
		t.Fatal(err)
	}
	m := NewSessionManager(store.DB, false)
	s := m.Create("bob", "admin")

	if err := store.ChangePassword("bob", "newpassword456"); err != nil {
		t.Fatalf("改密失败: %v", err)
	}
	if _, ok := m.Get(s.Token); ok {
		t.Fatal("改密后旧会话必须失效，否则泄露的会话还能继续用")
	}
	// 新口令能登录，旧口令不能。
	if _, ok := store.Verify("bob", "newpassword456"); !ok {
		t.Fatal("新口令应能通过校验")
	}
	if _, ok := store.Verify("bob", "password123"); ok {
		t.Fatal("旧口令不应再有效")
	}
}

// TestDeletedAdminSessionInvalid 验证账号被删除后它的会话立即失效。
func TestDeletedAdminSessionInvalid(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	if err := store.CreateAdmin("admin", "password123", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAdmin("bob", "password123", "readonly"); err != nil {
		t.Fatal(err)
	}
	m := NewSessionManager(store.DB, false)
	s := m.Create("bob", "readonly")

	if err := store.DeleteAdmin("bob"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, ok := m.Get(s.Token); ok {
		t.Fatal("账号删除后它的会话必须失效")
	}
}

// TestDisabledAdminSessionInvalid 验证账号被停用后它的会话立即失效。
func TestDisabledAdminSessionInvalid(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	if err := store.CreateAdmin("bob", "password123", "admin"); err != nil {
		t.Fatal(err)
	}
	m := NewSessionManager(store.DB, false)
	s := m.Create("bob", "admin")

	if _, err := store.DB.Exec(`UPDATE admins SET disabled=1 WHERE username='bob'`); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get(s.Token); ok {
		t.Fatal("账号停用后它的会话必须失效")
	}
}

// TestVerifyUnknownUserRunsKDF 验证用户名不存在时也要付 KDF 的代价。
//
// 不存在的账号如果秒回，而存在的账号要跑满 120000 轮，两条路径的耗时差
// 足以让攻击者远程枚举出哪些账号存在。这里用相对耗时做断言。
func TestVerifyUnknownUserRunsKDF(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	if err := store.CreateAdmin("bob", "password123", "admin"); err != nil {
		t.Fatal(err)
	}
	// 先热身，避免首次调用受其它开销影响。
	store.Verify("nobody", "x")
	store.Verify("bob", "wrong")

	start := time.Now()
	store.Verify("nobody", "wrong")
	unknown := time.Since(start)

	start = time.Now()
	store.Verify("bob", "wrong")
	known := time.Since(start)

	// 不存在的账号耗时不应明显短于存在的账号。
	if unknown*2 < known {
		t.Fatalf("不存在的账号耗时 %v，明显短于存在账号的 %v，说明没有做等量 KDF", unknown, known)
	}
}

// TestSafeNext 验证语言切换的跳转目标只能是站内地址。
func TestSafeNext(t *testing.T) {
	cases := []struct {
		next string
		want bool
	}{
		{"/", true},
		{"/settings", true},
		{"/settings?x=1", true},
		{"//evil.com", false},
		{"/\\evil.com", false},
		{"https://evil.com", false},
		{"evil.com", false},
		{"", false},
		{"/a\nb", false},
	}
	for _, c := range cases {
		if got := safeNext(c.next); got != c.want {
			t.Fatalf("safeNext(%q) = %v，期望 %v", c.next, got, c.want)
		}
	}
}

// TestLoginGuardPenalty 验证登录限速：猜错多次后「来源 + 账号」维度开始加延迟，
// 来源 IP 维度累计够多次后直接锁定。
//
// 账号维度的键是「来源 + 账号」而不是单独的账号名：只按账号名记的话，
// 任何匿名攻击者持续对 admin 失败一次，就能让管理员自己每次登录都多等 10 秒。
func TestLoginGuardPenalty(t *testing.T) {
	g := newLoginGuard()
	for i := 0; i < loginUserThreshold; i++ {
		g.fail("admin", "10.0.0.1")
	}
	wait, locked := g.penalty("admin", "10.0.0.1")
	if locked {
		t.Fatal("账号维度不应锁定，只应延迟")
	}
	if wait <= 0 {
		t.Fatalf("账号维度应加延迟，得到 %v", wait)
	}
	// 换个账号名则不受影响。
	wait3, _ := g.penalty("someone", "10.0.0.1")
	if wait3 != 0 {
		t.Fatalf("其它账号不应被牵连，得到 %v", wait3)
	}
	// 换个来源 IP 也不受牵连：账号维度的键是「来源 + 账号」的组合。
	wait2, _ := g.penalty("admin", "10.0.0.2")
	if wait2 != 0 {
		t.Fatalf("其它来源不应被牵连，得到 %v", wait2)
	}

	// IP 维度：累计够多次后锁定。
	g2 := newLoginGuard()
	for i := 0; i < loginIPThreshold; i++ {
		g2.fail("u"+strings.Repeat("x", i), "10.0.0.9")
	}
	if _, locked := g2.penalty("anyone", "10.0.0.9"); !locked {
		t.Fatal("来源 IP 累计失败过多后应被锁定")
	}
	// 登录成功后清掉「来源 + 账号」这一条。
	g3 := newLoginGuard()
	g3.fail("admin", "10.0.0.3")
	g3.success("admin", "10.0.0.3")
	if wait, _ := g3.penalty("admin", "10.0.0.3"); wait != 0 {
		t.Fatalf("登录成功后不应再有延迟，得到 %v", wait)
	}
}

// TestYamlScalarBlocksInjection 验证含换行的值会被整体加引号，
// 不会跳出当前的 YAML 块。
func TestYamlScalarBlocksInjection(t *testing.T) {
	got := yamlScalar("x\ninjected_key:\n  - a")
	if strings.Contains(got, "\n") {
		t.Fatalf("加了引号的值里不该还有裸换行: %q", got)
	}
	if !strings.HasPrefix(got, `"`) {
		t.Fatalf("含换行的值应整体加引号，得到 %q", got)
	}
	// 拼接后不能产生新的顶层键。
	content := "dns:\n  base_domain: " + got + "\n  magic_dns: true\n"
	if strings.Count(content, "injected_key:") != 1 {
		t.Fatalf("注入的键应被引号包住而不是变成新键:\n%s", content)
	}
	// 普通的值不受影响。
	if yamlScalar("hs.example.com") != "hs.example.com" {
		t.Fatalf("普通值不该被加引号，得到 %q", yamlScalar("hs.example.com"))
	}
}

// TestSetKeyKeepsDollarSign 验证写入配置时值里的 "$" 不会被当成模板展开。
//
// regexp.ReplaceAllString 会把替换串当模板：没有对应分组时 "$1" 这类片段
// 会被整段删掉，配置内容就这么静默丢了。
func TestSetKeyKeepsDollarSign(t *testing.T) {
	content := "acme_url: https://old.example.com\n"
	got := setTopKey(content, "acme_url", "https://x/$1dir/$name")
	if !strings.Contains(got, "https://x/$1dir/$name") {
		t.Fatalf("值里的 $ 被吞掉了:\n%s", got)
	}

	got2, err := setNestedKey("tls:\n  cache_dir: /old\n", "tls", "cache_dir", "/var/lib/headscale/$1cache")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got2, "/var/lib/headscale/$1cache") {
		t.Fatalf("嵌套键的值里的 $ 被吞掉了:\n%s", got2)
	}
}

// TestReplaceLineKeepsDollarSign 验证 replaceLine 同样不会吞掉值里的 "$"。
func TestReplaceLineKeepsDollarSign(t *testing.T) {
	got, err := replaceLine("server_url: https://old\n", `(?m)^server_url:.*$`, "server_url: https://x/$1y")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "https://x/$1y") {
		t.Fatalf("值里的 $ 被吞掉了:\n%s", got)
	}
}

// TestSafeText 验证命令输出里的非法 UTF-8 会被替换成合法字符。
//
// 子进程的 stderr 是原始字节：中文 locale 下 headscale 输出的 GBK 序列
// 不是合法 UTF-8，直接塞进网页会显示成乱码。
func TestSafeText(t *testing.T) {
	raw := "错误: \xd6\xd0\xce\xc4"
	got := safeText(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("safeText 之后仍不是合法 UTF-8: %q", got)
	}
	if safeText("正常中文") != "正常中文" {
		t.Fatal("合法 UTF-8 不该被改动")
	}
}

// TestSpeedReporterRejectsBeforeParse 验证未认证的请求不会因为请求体
// 里的 token 字段而跳过鉴权。
func TestSpeedReporterRejectsBeforeParse(t *testing.T) {
	r := newSpeedReporter("right-token")
	cases := []struct {
		name string
		body string
		want int
	}{
		{"请求体里的错误令牌", `{"node":"12","rx_bytes":1,"tx_bytes":2,"token":"wrong"}`, http.StatusUnauthorized},
		{"请求体里的正确令牌", `{"node":"12","rx_bytes":1,"tx_bytes":2,"token":"right-token"}`, http.StatusOK},
		{"大小写不同的字段名", `{"node":"12","rx_bytes":1,"tx_bytes":2,"Token":"right-token"}`, http.StatusOK},
		{"完全没有令牌", `{"node":"12","rx_bytes":1,"tx_bytes":2}`, http.StatusUnauthorized},
	}
	for _, c := range cases {
		code, body := postSpeed(t, r, "", c.body)
		if code != c.want {
			t.Fatalf("%s: status = %d，期望 %d (body %s)", c.name, code, c.want, body)
		}
	}
}

// isValidUTF8 是本地的小工具，避免为了一个断言再引一个包。
