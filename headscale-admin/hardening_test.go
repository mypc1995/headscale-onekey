package main

import (
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 本文件守着 2026-11-23 安全审计挖出的中危项。
//
// 每条用例都对应一个具体的攻击手法，不是「跑通就行」的功能测试：
// 变异掉其中任何一条（比如把 success() 改回清 IP 计数、把 MaxBytesReader
// 去掉、把校验失败时的回滚删掉），对应的用例必须立刻变红。

// TestLoginGuardSuccessDoesNotWashIPCount 验证登录成功洗不白来源 IP 的计数。
//
// 旧实现里 success() 把整个 IP 的计数连同锁定标记一起删了，于是攻击者
// 只要用自己那个合法账号登录一次，就能把限速状态清零——累计 38 次失败
// 仍然没有触发锁定，爆破实际上不受限制。
func TestLoginGuardSuccessDoesNotWashIPCount(t *testing.T) {
	g := newLoginGuard()
	for i := 0; i < loginIPThreshold; i++ {
		g.fail("bogus", "10.0.0.5")
	}
	// 攻击者用自己的合法账号登录一次，企图把限速状态洗白。
	g.success("attacker-own-account", "10.0.0.5")
	if _, locked := g.penalty("anyone", "10.0.0.5"); !locked {
		t.Fatal("登录成功不应清掉来源 IP 的计数，否则限速状态可以被洗白")
	}
}

// TestLoginGuardDelayIsPerSourceAndUser 验证账号维度的惩罚按「来源 + 账号」计。
//
// 只按账号名记的话，任何匿名攻击者持续对 admin 失败一次，就能让管理员
// 自己（换个来源）每次登录都多等 10 秒——定向拖慢运维入口的拒绝服务。
func TestLoginGuardDelayIsPerSourceAndUser(t *testing.T) {
	g := newLoginGuard()
	for i := 0; i < loginUserThreshold; i++ {
		g.fail("admin", "10.0.0.1")
	}
	if wait, _ := g.penalty("admin", "10.0.0.1"); wait <= 0 {
		t.Fatalf("同一来源同一账号应加延迟，得到 %v", wait)
	}
	if wait, _ := g.penalty("admin", "10.0.0.2"); wait != 0 {
		t.Fatalf("换一个来源不应被牵连，得到 %v", wait)
	}
	if wait, _ := g.penalty("other", "10.0.0.1"); wait != 0 {
		t.Fatalf("换一个账号不应被牵连，得到 %v", wait)
	}
}

// TestLoginGuardTrustedSourceNeverLocked 验证可信来源既不累计也不锁定。
//
// 共享出口（NAT / 公司统一出口 / 反代）下，一次误触就会把所有人锁在门外。
// 可信网段是唯一的根治办法：这些来源直接绕过限速。
func TestLoginGuardTrustedSourceNeverLocked(t *testing.T) {
	t.Setenv(loginTrustedEnv, "10.0.0.0/24,192.168.1.60")
	g := newLoginGuard()
	for i := 0; i < loginIPThreshold*3; i++ {
		g.fail("admin", "10.0.0.7")
	}
	if wait, locked := g.penalty("admin", "10.0.0.7"); locked || wait != 0 {
		t.Fatalf("可信来源不应被锁定或延迟，得到 wait=%v locked=%v", wait, locked)
	}
	// 可信网段之外的来源照常计数、照常锁定。
	out := newLoginGuard()
	for i := 0; i < loginIPThreshold; i++ {
		out.fail("admin", "10.0.1.7")
	}
	if _, locked := out.penalty("admin", "10.0.1.7"); !locked {
		t.Fatal("不在可信网段里的来源仍应被锁定")
	}
}

// TestLoginGuardTableFullEvictsOldest 验证记录表满了以后新键仍能被计数。
//
// 旧实现是「表满就不再记这个键」：攻击者用一批垃圾用户名把表填满后，
// 真实目标（admin）的失败计数就再也记不上，账号维度的惩罚直接归零。
func TestLoginGuardTableFullEvictsOldest(t *testing.T) {
	g := newLoginGuard()
	// 填充时用互不相同的来源 IP：否则光是填充就会把某一个 IP 打到锁定阈值，
	// 后面断言「目标账号的延迟」时拿到的会是「已锁定」而不是延迟。
	for i := 0; i < loginMaxKeys+10; i++ {
		ip := "10." + strconv.Itoa(i/65025%256) + "." + strconv.Itoa(i/255%256) + "." + strconv.Itoa(i%254+1)
		g.fail("filler-"+strconv.Itoa(i), ip)
	}
	if len(g.users) > loginMaxKeys {
		t.Fatalf("账号表长度 %d 超过上限 %d", len(g.users), loginMaxKeys)
	}
	// 表满之后打真实目标，计数仍然必须被记上。
	for i := 0; i < loginUserThreshold; i++ {
		g.fail("admin", "203.0.113.7")
	}
	if wait, locked := g.penalty("admin", "203.0.113.7"); locked || wait <= 0 {
		t.Fatalf("表满之后目标账号的失败计数丢失了（wait=%v locked=%v）", wait, locked)
	}
}

// TestLoginPostRejectsOversizedBody 验证登录请求体有上限。
//
// 登录表单正常不到 1KB。没有上限时，匿名请求可以往 /login 灌几十 MB
// 的 body，服务端会整个读进内存——实测 10 个并发 9MB 的请求就能让进程
// 常驻内存从 23MB 涨到 63MB。
func TestLoginPostRejectsOversizedBody(t *testing.T) {
	a := &App{login: newLoginGuard()}
	body := "username=admin&password=" + strings.Repeat("x", loginMaxBody+1024)
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.loginPost(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大请求体的状态码 = %d，期望 %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}

// TestLoginPostAcceptsNormalBody 验证正常大小的登录表单不会被误拒。
//
// 上限取得太小的话，管理员自己都登不进去——这是加限制时最容易踩的坑。
func TestLoginPostAcceptsNormalBody(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	tmpl := template.Must(template.New("").Parse(`{{define "login"}}登录失败{{end}}`))
	a := &App{store: store, sessions: NewSessionManager(store.DB, false), login: newLoginGuard(), tmpl: tmpl}
	body := "username=admin&password=" + strings.Repeat("x", 32)
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.loginPost(w, r)
	if w.Code == http.StatusRequestEntityTooLarge {
		t.Fatal("正常大小的登录表单被当成超大请求体拒掉了")
	}
}

// TestKDFFenceLimitsConcurrency 验证口令校验的并发闸门真的会挡住超额请求。
//
// 口令是 PBKDF2-HMAC-SHA256 120000 轮，一次校验几十毫秒纯 CPU。
// 不加闸门，几十个并发登录就能把这台机器的 CPU 打满——而这台机器同时
// 还跑着 headscale 控制面。
func TestKDFFenceLimitsConcurrency(t *testing.T) {
	a := &App{kdfSem: make(chan struct{}, 2)}
	rel1, ok1 := a.acquireKDF()
	rel2, ok2 := a.acquireKDF()
	if !ok1 || !ok2 {
		t.Fatal("槽位没占满时应当能拿到")
	}
	// 槽位占满后，第三个请求必须排队（拿不到槽位）。
	queued := make(chan bool, 1)
	go func() {
		_, got := a.acquireKDF()
		queued <- got
	}()
	select {
	case got := <-queued:
		if got {
			t.Fatal("槽位占满时不应再放行新的口令校验")
		}
	case <-time.After(300 * time.Millisecond):
		// 还在排队 = 闸门生效。
	}
	// 释放一个槽位之后应当能再拿到一个。
	rel1()
	select {
	case got := <-queued:
		if !got {
			t.Fatal("释放槽位后新的口令校验应当能放行")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("释放槽位后新的口令校验仍被挡住")
	}
	rel2()
}

// TestKDFFenceNilIsUnlimited 验证没有闸门时（测试里手工构造 App）
// 登录流程不会被卡死。
func TestKDFFenceNilIsUnlimited(t *testing.T) {
	a := &App{}
	release, ok := a.acquireKDF()
	if !ok {
		t.Fatal("未配置闸门时不应拦下请求")
	}
	release()
}

// TestEnsureAdminRevokesSessions 验证 --reset-admin 改密后吊销既有会话。
//
// --reset-admin 是应急处置入口：怀疑口令泄露时用它换新口令。
// 不吊销的话，攻击者手里那张旧 Cookie 还能继续用满 8 小时，「改密」
// 等于没改。降权与改密都已经做了吊销，这里是最后一条漏掉的路径。
func TestEnsureAdminRevokesSessions(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	if err := store.CreateAdmin("admin", "password123", "admin"); err != nil {
		t.Fatal(err)
	}
	m := NewSessionManager(store.DB, false)
	s := m.Create("admin", "admin")
	if _, ok := m.Get(s.Token); !ok {
		t.Fatal("会话应存在")
	}

	if err := store.EnsureAdmin("admin", "newpassword456"); err != nil {
		t.Fatalf("重置口令失败: %v", err)
	}
	if _, ok := m.Get(s.Token); ok {
		t.Fatal("重置口令后旧会话必须失效，否则泄露的会话还能继续用满 8 小时")
	}
	if _, ok := store.Verify("admin", "newpassword456"); !ok {
		t.Fatal("新口令应能通过校验")
	}
	if _, ok := store.Verify("admin", "password123"); ok {
		t.Fatal("旧口令不应再有效")
	}
}

// TestApplyConfigKeepsOriginalWhenInvalid 验证配置校验不通过时原文件一字不动。
//
// 旧实现是「先 SaveConfig 落盘、再 configtest」：校验没过的话，坏配置
// 已经躺在生产配置上了，界面还只提示一句「已保存但校验失败」。
func TestApplyConfigKeepsOriginalWhenInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	orig := "server_url: http://127.0.0.1:8080\ndns:\n  magic_dns: true\n"
	if err := os.WriteFile(path, []byte(orig), 0600); err != nil {
		t.Fatal(err)
	}
	// 故意指向一个不存在的二进制：configtest 必然失败，等价于「校验不通过」。
	c := &HSClient{Config: path, Bin: filepath.Join(dir, "no-such-headscale")}
	if _, err := c.ApplyConfig("broken: true\n", false, "zh"); err == nil {
		t.Fatal("校验不通过时应当返回错误")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != orig {
		t.Fatalf("校验不通过后原配置被改动了:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".config-candidate.yaml")); !os.IsNotExist(err) {
		t.Fatal("候选文件没有被清理")
	}
}

// TestSavePolicyCheckedKeepsOriginalWhenInvalid 验证策略校验不通过时原文件一字不动。
//
// 策略文件写坏了还留在盘上，headscale 下次启动会直接失败——等于留了颗地雷。
func TestSavePolicyCheckedKeepsOriginalWhenInvalid(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	policy := filepath.Join(dir, "policy.hujson")
	orig := `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`
	if err := os.WriteFile(cfg, []byte("policy:\n  path: \""+policy+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, []byte(orig), 0600); err != nil {
		t.Fatal(err)
	}
	c := &HSClient{Config: cfg, Bin: filepath.Join(dir, "no-such-headscale")}
	if err := c.SavePolicyChecked(`{"acls":[`, "zh"); err == nil {
		t.Fatal("校验不通过时应当返回错误")
	}
	got, err := os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != orig {
		t.Fatalf("校验不通过后原策略被改动了: %s", got)
	}
	if _, err := os.Stat(policy + ".candidate"); !os.IsNotExist(err) {
		t.Fatal("候选策略文件没有被清理")
	}
}

// TestSavePolicyCheckedNeedsPolicyPath 验证没有配 policy.path 时不写文件。
func TestSavePolicyCheckedNeedsPolicyPath(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("policy:\n  path: \"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &HSClient{Config: cfg, Bin: filepath.Join(dir, "no-such-headscale")}
	if err := c.SavePolicyChecked(`{"acls":[]}`, "zh"); err == nil {
		t.Fatal("未配置 policy.path 时应当报错")
	}
}

// TestConnLimiterCountsConnections 验证连接数限流器会放行正常连接、超额直接掐断。
//
// net/http 自己不限制连接条数：超时只约束「单条连接占多久」，
// 慢速连接（Slowloris）可以开成千上万条把连接数堆满。
func TestConnLimiterCountsConnections(t *testing.T) {
	l := newConnLimiter(2)
	hook := l.hook()
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	if l.cur.Load() != 0 {
		t.Fatalf("初始连接数 = %d，应为 0", l.cur.Load())
	}
	// 模拟两条连接建立。
	hook(c1, http.StateNew)
	hook(c2, http.StateNew)
	if l.cur.Load() != 2 {
		t.Fatalf("连接数 = %d，期望 2", l.cur.Load())
	}
	// 一条连接关闭。
	hook(c1, http.StateClosed)
	if l.cur.Load() != 1 {
		t.Fatalf("关闭一条后连接数 = %d，期望 1", l.cur.Load())
	}
}
