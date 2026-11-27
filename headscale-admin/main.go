package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type App struct {
	store      *AuthStore
	sessions   *SessionManager
	hs         *HSClient
	net        *NetMonitor
	host       *HostMonitor
	nodes      *NodeMonitor
	joinKeys   *joinKeyStore
	apiKeys    *joinKeyStore // 刚创建的 API key 一次性暂存，语义与预授权密钥一致
	thresholds AlertThresholds
	tmpl       *template.Template
	port       string
	dataFile   string
	tplDir     string
	staticDir  string
	speed      *speedReporter
	login      *loginGuard
	captcha    *captchaStore
	// kdfSem 是口令校验的并发闸门，容量决定同时能跑几个 PBKDF2。
	// 见 newKDFFence。
	kdfSem chan struct{}
}

type Page struct {
	Title    string
	Active   string
	CSRF     string
	Flash    string
	Username string
	Role     string
	Lang     string
	Data     any
	Year     int
}

func main() {
	port := flag.String("port", envDefault("HSA_PORT", "3001"), "监听端口")
	dbFile := flag.String("data", defaultDataFile(), "SQLite 数据库路径")
	legacyJSON := flag.String("legacy-json", defaultLegacyJSON(), "旧版管理员 JSON，首次启动时自动迁移")
	tplDir := flag.String("templates", defaultTemplatesDir(), "模板目录")
	staticDir := flag.String("static", defaultStaticDir(), "静态资源目录")
	// 初始/重置管理员的口令优先从环境变量读：命令行参数会出现在
	// /proc/<pid>/cmdline 里，同机的任何用户都能看到；环境变量只有
	// 同用户（以及 root）能读。命令行参数仍然保留，方便手工调试。
	initAdmin := flag.String("init-admin", envDefault("HSA_INIT_ADMIN", ""), "初始化管理员，格式 用户名:密码（也可用环境变量 HSA_INIT_ADMIN，避免口令出现在进程参数里）")
	resetAdmin := flag.String("reset-admin", envDefault("HSA_RESET_ADMIN", ""), "重置管理员密码，格式 用户名:密码（也可用环境变量 HSA_RESET_ADMIN）")
	adminUser := flag.String("admin-user", envDefault("HSA_ADMIN_USER", "admin"), "初始管理员用户名")
	adminPass := flag.String("admin-pass", envDefault("HSA_ADMIN_PASS", ""), "初始管理员密码（配合 -init-only 使用；也可用环境变量 HSA_ADMIN_PASS）")
	secureCookie := flag.Bool("cookie-secure", os.Getenv("HSA_COOKIE_SECURE") == "1", "Cookie 增加 Secure 标记")
	speedToken := flag.String("node-speed-token", envDefault("HSA_NODE_SPEED_TOKEN", ""), "节点网速上报令牌（节点侧 agent 调用 POST /api/speed 使用）")
	speedTokenFile := flag.String("node-speed-token-file", envDefault("HSA_NODE_SPEED_TOKEN_FILE", ""), "节点网速上报令牌文件（优先于令牌本身，避免令牌出现在进程参数里）")
	initOnly := flag.Bool("init-only", false, "初始化管理员后立即退出")
	flag.Parse()

	store, err := OpenStore(*dbFile, *legacyJSON)
	if err != nil {
		log.Fatalf("加载管理员数据失败: %v", err)
	}
	// 管理界面自身的 HTTPS：设置页写入 admin-tls.json 后重启生效。
	//
	// 必须在这里就解析出「实际生效」的状态，因为监听协议和 Cookie 的 Secure
	// 标记都要用它。之前 Secure 用的是配置里的 Enabled 标志，而证书可用性在
	// 后面才检查：配置写着启用 HTTPS 但证书坏了 → 服务回退成 HTTP，Cookie 却
	// 仍带 Secure，浏览器明文下不发送该 Cookie，登录态必然失效、运维入口被
	// 锁死——恰好是这个兜底本来要防的事。
	tlsCfg, tlsOn := resolveAdminTLS(*dbFile)
	// HTTPS 生效时 Cookie 自动加 Secure。
	secure := sessionCookieSecure(*secureCookie, tlsOn)
	initSpec := *initAdmin
	if initSpec == "" && *adminPass != "" {
		initSpec = *adminUser + ":" + *adminPass
	}
	for _, spec := range []struct {
		value string
		reset bool
	}{{initSpec, false}, {*resetAdmin, true}} {
		if spec.value == "" {
			continue
		}
		user, pass, ok := splitCredential(spec.value)
		if !ok {
			log.Fatalf("凭据格式应为 用户名:密码")
		}
		if spec.reset {
			err = store.EnsureAdmin(user, pass)
		} else if !store.Exists(user) {
			err = store.CreateAdmin(user, pass, "admin")
		}
		if err != nil {
			log.Fatalf("设置管理员失败: %v", err)
		}
		log.Printf("管理员 %s 已就绪", user)
	}
	if *initOnly {
		log.Printf("管理员初始化完成")
		return
	}

	// 静态资源版本号：启动时按 static 目录下的文件内容算一个哈希。
	// 样式和脚本的 URL 带上它，浏览器拿到的就是新 URL，不会再吃旧缓存。
	assetVer := assetVersion(*staticDir)

	funcs := template.FuncMap{
		"field":      fieldValue,
		"nodeName":   nodeDisplayName,
		"tf":         protoTime,
		"masked":     maskedKey,
		"keyExpired": keyExpired,
		"isTrue":     isTrue,
		"userOf":     userOf,
		"join":       strings.Join,
		"list":       joinAny,
		"has":        strings.Contains,
		"short": func(s string, n int) string {
			if len(s) > n {
				return s[:n] + "…"
			}
			return s
		},
		"yearNow": func() int { return time.Now().Year() },
		"t":       tr,
		"icon":    icon,
		"asset": func(name string) string {
			return "/static/" + name + "?v=" + assetVer
		},
		"langName": func(l string) string {
			if l == "en" {
				return "English"
			}
			return "中文"
		},
		"isLang": func(l, cur string) bool { return l == cur },
		"pagePath": func(active string) string {
			if active == "dashboard" || active == "" {
				return "/"
			}
			return "/" + active
		},
		"rate":  fmtRate,
		"bytes": fmtBytes,
		"pct":   func(v float64) string { return fmt.Sprintf("%.0f%%", v) },
		"dict": func(pairs ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(pairs); i += 2 {
				m[fmt.Sprint(pairs[i])] = pairs[i+1]
			}
			return m
		},
	}
	tmpl, err := template.New("").Funcs(funcs).ParseGlob(filepath.Join(*tplDir, "*.html"))
	if err != nil {
		log.Fatalf("加载模板失败: %v", err)
	}

	hs := NewHSClient()
	// 节点网速上报通道。没有令牌时整个接口关闭（返回 404），不暴露匿名写入入口。
	reporter := newSpeedReporter(loadSpeedToken(*speedToken, *speedTokenFile))
	if reporter.enabled() {
		log.Printf("节点网速上报通道已启用（POST /api/speed）")
	}
	a := &App{
		store: store, sessions: NewSessionManager(store.DB, secure), hs: hs, net: NewNetMonitor(),
		host: NewHostMonitor(), nodes: NewNodeMonitor(hs, reporter.data), joinKeys: newJoinKeyStore(), apiKeys: newJoinKeyStore(), thresholds: defaultAlertThresholds(),
		tmpl: tmpl, port: *port, tplDir: *tplDir, staticDir: *staticDir, dataFile: *dbFile,
		speed: reporter, login: newLoginGuard(), kdfSem: newKDFFence(), captcha: newCaptchaStore(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.loginPost)
	mux.HandleFunc("GET /login/captcha.png", a.captchaImage)
	mux.HandleFunc("GET /lang", a.setLanguage)
	mux.Handle("/static/", a.staticHandler(*staticDir))
	mux.Handle("GET /health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	// 节点侧上报入口：走独立令牌认证，不经过管理员会话，因此挂在外层 mux 上。
	mux.HandleFunc("POST /api/speed", reporter.handle)

	p := http.NewServeMux()
	p.HandleFunc("GET /", a.dashboard)
	p.HandleFunc("GET /nodes", a.nodesPage)
	p.HandleFunc("GET /users", a.usersPage)
	p.HandleFunc("GET /keys", a.keysPage)
	p.HandleFunc("GET /network", a.networkPage)
	p.HandleFunc("GET /settings", a.settingsPage)
	p.HandleFunc("GET /ops", a.opsPage)
	p.HandleFunc("GET /admins", a.adminsPage)
	p.HandleFunc("GET /api/netmon", a.netmonAPI)
	p.HandleFunc("GET /api/monitor", a.monitorAPI)
	a.registerActions(p)
	mux.Handle("/", a.sessions.Wrap(p))

	addr := ":" + *port
	// 必须显式设置超时：http.ListenAndServe 的默认 Server 一个超时都没有，
	// 慢速连接（Slowloris）可以把连接数占满，拖垮整台机器。
	srv := &http.Server{
		Addr:              addr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		// 同时在处理的连接数上限。
		//
		// net/http 没有 MaxConns 这种字段：ReadTimeout 之类只限制「单条连接
		// 占多久」，不限制「同时有多少条连接」。慢速连接（Slowloris）可以
		// 开成千上万条把文件描述符和内存堆满。超时都是 30/120 秒，堆起来
		// 一样能拖垮机器。所以这里用 ConnState 回调自己数连接。
		ConnState: newConnLimiter(256).hook(),
	}
	if tlsOn {
		log.Printf("Headscale Admin 监听 %s（HTTPS）", addr)
		if err := srv.ListenAndServeTLS(tlsCfg.CertPath, tlsCfg.KeyPath); err != nil {
			log.Fatal(err)
		}
		return
	}
	log.Printf("Headscale Admin 监听 %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func envDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func splitCredential(s string) (string, string, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func fieldValue(m map[string]any, key string) string { return str(m[key]) }
func userOf(m map[string]any) string                 { return str(asMap(m["user"])["name"]) }

// nodeDisplayName 取节点的受管名（given_name）：headscale v0.29 的 `nodes rename` 改的就是它，
// 它同时是 MagicDNS 域名里主机名那一段。节点 JSON 里的 name 是客户端上报的主机名，
// 客户端重连就会覆盖它，**不能**拿它当节点名显示或回显——否则改完名页面纹丝不动。
func nodeDisplayName(m map[string]any) string {
	if v := str(m["given_name"]); v != "" {
		return v
	}
	return str(m["name"])
}

func joinAny(v any) string {
	switch x := v.(type) {
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			parts = append(parts, str(item))
		}
		return strings.Join(parts, ", ")
	case []string:
		return strings.Join(x, ", ")
	default:
		return str(v)
	}
}

var pageTitles = map[string]string{
	"dashboard": "title.dashboard", "nodes": "title.nodes", "users": "title.users", "keys": "title.keys",
	"network": "title.network", "settings": "title.settings", "ops": "title.ops", "admins": "title.admins",
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	s := sessionFrom(r)
	lang := langFrom(r)
	title := pageTitles[name]
	if title == "" {
		title = name
	}
	title = tr(lang, title)
	p := Page{Title: title, Active: name, CSRF: s.CSRF, Username: s.Username, Role: s.Role, Lang: lang, Data: data, Year: time.Now().Year()}
	p.Flash = a.sessions.TakeFlash(s.Token)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tmpl.ExecuteTemplate(w, name, p); err != nil {
		log.Printf("渲染 %s 失败: %v", name, err)
	}
}

func (a *App) redirectWithFlash(w http.ResponseWriter, r *http.Request, to, msg string) {
	a.sessions.SetFlash(sessionFrom(r).Token, msg)
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (a *App) mutate(w http.ResponseWriter, r *http.Request) bool {
	s := sessionFrom(r)
	lang := langFrom(r)
	if !validCSRF(r, s) {
		http.Error(w, tr(lang, "msg.csrfFailed"), http.StatusBadRequest)
		return false
	}
	if s.Role != "admin" {
		ref := r.Header.Get("Referer")
		if ref == "" {
			ref = "/"
		}
		a.redirectWithFlash(w, r, ref, tr(lang, "msg.readonlyBlocked"))
		return false
	}
	return true
}

func formUint(r *http.Request, key string) (uint64, error) {
	v := strings.TrimSpace(r.FormValue(key))
	if v == "" {
		return 0, errors.New(tr(langFrom(r), "msg.missingParam", key))
	}
	return strconv.ParseUint(v, 10, 64)
}

// actionDetail 把 headscale CLI 的输出整理成给人看的提示详情。
//
// CLI 调用一律带 -o json，变更类命令（建用户、删节点、改标签…）的 stdout 是
// JSON，空结果就是一个 "{}"。直接把它拼进提示会变成「操作成功：{}」这种
// 没人看得懂的文案——JSON 是给程序看的，不是给人看的。
// 所以只有非 JSON 的纯文本才当作详情展示，其余一律用「操作成功」收尾。
func actionDetail(out string) string {
	msg := strings.TrimSpace(out)
	if msg == "" {
		return ""
	}
	if strings.HasPrefix(msg, "{") || strings.HasPrefix(msg, "[") {
		return ""
	}
	// 按 rune 截断：中文输出按字节切会把一个汉字劈成两半，显示成乱码。
	if runes := []rune(msg); len(runes) > 160 {
		msg = string(runes[:160]) + "…"
	}
	return msg
}

func (a *App) doAction(w http.ResponseWriter, r *http.Request, to string, fn func() (string, error)) {
	lang := langFrom(r)
	out, err := fn()
	if err != nil {
		a.redirectWithFlash(w, r, to, tr(lang, "msg.operationFailed", err.Error()))
		return
	}
	msg := actionDetail(out)
	if msg == "" {
		msg = tr(lang, "msg.operationSuccess")
	} else {
		msg = tr(lang, "msg.successWith", msg)
	}
	a.redirectWithFlash(w, r, to, msg)
}

// setLanguage 在未登录时也可用，方便登录页切换语言。
func (a *App) setLanguage(w http.ResponseWriter, r *http.Request) {
	lang := normalizeLang(r.URL.Query().Get("lang"))
	http.SetCookie(w, &http.Cookie{
		Name: "hsa_lang", Value: lang, Path: "/", SameSite: http.SameSiteLaxMode,
		Secure: a.sessions.secure, MaxAge: 365 * 24 * 3600,
	})
	next := r.URL.Query().Get("next")
	if !safeNext(next) {
		next = "/login"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// safeNext 判断跳转目标是不是站内地址。
//
// 只放行以单个 "/" 开头的路径。协议相对地址（"//evil.com"）会被浏览器
// 当成外站，而 "/\evil.com" 里的反斜杠也会被浏览器归一化成斜杠，
// 两者都能把用户骗到外站，所以都要挡掉；控制字符同样挡掉。
func safeNext(next string) bool {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return false
	}
	if strings.Contains(next, "\\") {
		return false
	}
	for _, r := range next {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

const (
	// loginMaxBody 是登录表单请求体的上限。
	//
	// 登录表单只有两个字段（用户名、口令），正常请求体不到 1KB。
	// 不加限制的话，匿名请求可以往 /login 灌几十 MB 的 body，
	// 服务端会把它整个读进内存——实测 10 个并发 9MB 的请求就能让
	// 进程常驻内存从 23MB 涨到 63MB。/api/speed 早有 64KB 的限制，
	// /login 反而漏了。
	loginMaxBody = 64 << 10
	// kdfQueueTimeout 是等待口令校验槽位的最长时间。
	//
	// 超过这个时间还没排上，就直接回 429：排队的人越多，
	// 每个请求要等的时间越长，不设上限的话队列可以无限长。
	kdfQueueTimeout = 8 * time.Second
)

// newKDFFence 返回口令校验的并发闸门。
//
// 口令用 PBKDF2-HMAC-SHA256 120000 轮存储，校验一次是几十毫秒的纯 CPU。
// 闸门保证同时最多跑 N 个校验，攻击者用并发请求也榨不出更多 CPU。
// N 取 CPU 核数减 2：给 headscale 控制面留出余量。
func newKDFFence() chan struct{} {
	n := runtime.NumCPU() - 2
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	return make(chan struct{}, n)
}

// acquireKDF 申请一个口令校验槽位。
//
// 拿不到就返回 false：调用方应当直接拒绝这次请求，而不是无限排队。
// kdfSem 为 nil（测试里手工构造 App 时）时不做限制。
func (a *App) acquireKDF() (release func(), ok bool) {
	if a.kdfSem == nil {
		return func() {}, true
	}
	timer := time.NewTimer(kdfQueueTimeout)
	defer timer.Stop()
	select {
	case a.kdfSem <- struct{}{}:
		return func() { <-a.kdfSem }, true
	case <-timer.C:
		return nil, false
	}
}

// connLimiter 按连接数限制并发连接。
//
// net/http 自己不限制连接条数，这里用 ConnState 回调数：
// 新建连接 +1，连接关闭 -1，超过上限的直接把这条连接的读写截止时间
// 设成「当下」，让它立刻握手失败/断开。
//
// 计数不会算错：net/http 保证每条被 Accept 的连接先回调 StateNew，
// 随后 conn.serve 的 defer 一定回调 StateClosed，一一对应。
type connLimiter struct {
	cur atomic.Int64
	max int64
}

// newConnLimiter 返回一个上限为 max 条连接的限流器。
func newConnLimiter(max int64) *connLimiter {
	if max < 1 {
		max = 1
	}
	return &connLimiter{max: max}
}

// hook 返回可以挂到 http.Server.ConnState 上的回调。
func (l *connLimiter) hook() func(net.Conn, http.ConnState) {
	return func(c net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			if l.cur.Add(1) > l.max {
				// 超过上限：让这条连接立刻失效。
				// 计数不在这里减——连接关闭后服务端一定会回调 StateClosed，
				// 那里统一减，否则会出现重复扣减把计数减成负数。
				_ = c.SetDeadline(time.Now())
			}
		case http.StateClosed:
			l.cur.Add(-1)
		}
	}
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, ok := a.sessions.Get(c.Value); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	lang := langFrom(r)
	data := a.loginPageData(r, lang)
	// 改密之后旧会话会被吊销，这里给一句说明，别让人以为是登录失败。
	if r.URL.Query().Get("passchanged") == "1" {
		data["Notice"] = tr(lang, "msg.passChangedRelogin")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = a.tmpl.ExecuteTemplate(w, "login", data)
}

// securityHeaders 给每个响应加上安全响应头。
//
// 管理界面直接暴露在公网上，缺这些头的后果分别是：
//   - 没有 X-Frame-Options：别人可以把管理界面嵌进自己的页面，
//     诱导管理员点「保存」，等于替别人执行操作（点击劫持）；
//   - 没有 CSP：一次 XSS 就能把会话 Cookie 与 CSRF 令牌一起拿走；
//   - 没有 nosniff：浏览器可能把返回内容当成别的类型解析；
//   - 没有 HSTS：用户第一次走 http 就可能被链路上的设备降级。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		// script-src 不带 'unsafe-inline'：页面上不允许再有内联脚本，
		// 出现 XSS 就没有第二次机会（原先那段内联主题脚本已挪到 theme.js）。
		// style-src 带 'unsafe-inline'：进度条宽度、主题色块用的是行内
		// style 属性，那不是脚本，放宽这一项不引入脚本执行风险。
		h.Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"script-src 'self'",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data:",
			"font-src 'self'",
			"connect-src 'self'",
			"object-src 'none'",
			"base-uri 'none'",
			"form-action 'self'",
			"frame-ancestors 'none'",
		}, "; "))
		if r.TLS != nil {
			// 只在直连 HTTPS 时加 HSTS。
			// 反代终止 TLS 的部署里服务端看不到 r.TLS，
			// 那边的 HSTS 要由反代自己加。
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// assetVersion 按 static 目录下的文件内容算一个版本号。
//
// 样式和脚本的 URL 不带版本号时，浏览器会按「文件年龄的 10%」做启发式缓存：
// 上线新版样式后，老浏览器的缓存还能撑十几个小时，用户看到的还是旧界面。
// URL 里带上内容哈希之后，每次改动都是一个新 URL，浏览器必然重新取。
func assetVersion(dir string) string {
	h := sha256.New()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "0"
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		h.Write([]byte(name))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// staticHandler 提供静态资源（样式表、脚本）。
//
// 每次都要回源校验，不能让浏览器自己缓存：样式和脚本的 URL 不带版本号，
// 而 http.FileServer 只发 Last-Modified，浏览器会按「文件年龄的 10%」做启发式
// 缓存——上线新版样式后，老浏览器的缓存还能撑上十几个小时，用户看到的界面
// 是旧版（本次加验证码就踩到了：新样式没生效，验证码图片换行错位）。
// no-cache 的意思是「可以存，但用之前必须问服务器一次」，服务器回 304，
// 流量很小，同时保证拿到的一定是最新版本。
func (a *App) staticHandler(dir string) http.Handler {
	fs := http.StripPrefix("/static/", http.FileServer(http.Dir(dir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		fs.ServeHTTP(w, r)
	})
}

// loginPageData 组装登录页的数据，公网来源还要带上验证码。
func (a *App) loginPageData(r *http.Request, lang string) map[string]any {
	data := map[string]any{"Error": "", "Lang": lang}
	if a.captcha != nil && a.captchaRequired(r) {
		data["Captcha"] = true
		// 图片 URL 里带一个随机数，避免浏览器用缓存里的旧图。
		if nonce, err := randomHex(8); err == nil {
			data["CaptchaNonce"] = nonce
		}
	}
	return data
}

// renderLogin 重新渲染登录页（出错时用），验证码也要重新取。
func (a *App) renderLogin(w http.ResponseWriter, r *http.Request, lang, errMsg string) {
	data := a.loginPageData(r, lang)
	data["Error"] = errMsg
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = a.tmpl.ExecuteTemplate(w, "login", data)
}

func (a *App) loginPost(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	// 先给请求体上闸：登录表单正常不到 1KB，超过 64KB 一律拒掉，
	// 不让它有机会被读进内存。超限时 MaxBytesReader 会让 ParseForm 报错。
	r.Body = http.MaxBytesReader(w, r.Body, loginMaxBody)
	if err := r.ParseForm(); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, tr(lang, "login.bodyTooLarge"), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, tr(lang, "msg.operationFailed", err.Error()), http.StatusBadRequest)
		return
	}
	user := strings.TrimSpace(r.FormValue("username"))
	ip := clientIP(r)

	// 验证码先于口令校验：验证码不对就不跑 PBKDF2，攻击者烧不掉 CPU。
	if a.captcha != nil && a.captchaRequired(r) {
		token := ""
		if c, err := r.Cookie(captchaCookie); err == nil {
			token = c.Value
		}
		if !a.captcha.verify(token, r.FormValue("captcha")) {
			// 猜验证码也算一次失败，纳入限速：否则可以无限次刷图试答案。
			a.login.fail(user, ip)
			a.renderLogin(w, r, lang, tr(lang, "login.captchaWrong"))
			return
		}
	}

	// 先看惩罚：被锁定时直接拒绝，不再消耗一次 KDF；
	// 只是延迟时先等一下，抬高爆破的时间成本。
	wait, locked := a.login.penalty(user, ip)
	if locked {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		a.renderLogin(w, r, lang, tr(lang, "login.tooMany"))
		return
	}
	if wait > 0 {
		time.Sleep(wait)
	}

	// 口令校验前先占一个 KDF 槽位。
	//
	// 排队超时就直接回 429：既保护 CPU，也避免攻击者用慢请求
	// 把服务端的 goroutine 队列撑长。
	release, got := a.acquireKDF()
	if !got {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		a.renderLogin(w, r, lang, tr(lang, "login.busy"))
		return
	}
	admin, ok := a.store.Verify(user, r.FormValue("password"))
	release()
	if !ok {
		a.login.fail(user, ip)
		a.renderLogin(w, r, lang, tr(lang, "login.error"))
		return
	}
	a.login.success(user, ip)
	s := a.sessions.Create(admin.Username, admin.Role)
	a.sessions.SetCookie(w, s)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// clientIP 取请求的来源地址。
//
// 只用 RemoteAddr，不认 X-Forwarded-For：那个头是请求方自己写的，
// 认它等于让攻击者随手换个头就绕过 IP 维度的限速。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type dashboardData struct {
	Users, Nodes, Online, PendingRoutes, PreauthKeys, APIKeys int
	Service, Version, Cert                                    string
	ConfigPath                                                string
	Recent                                                    []map[string]any
}

func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	d := dashboardData{Service: "未知", Version: a.hs.Version(), Cert: a.hs.CertExpiry(), ConfigPath: a.hs.Config}
	// 服务状态直接用监控快照里的：监控本来就在每 15 秒查一次 systemctl，
	// 这里没必要再起一个进程。
	if snap := a.nodes.Snapshot(); snap.Service != "" {
		d.Service = snap.Service
	}
	if users, err := a.hs.cachedUsers(); err == nil {
		d.Users = len(users)
	}
	if nodes, err := a.hs.cachedNodes(); err == nil {
		d.Nodes = len(nodes)
		for _, n := range nodes {
			if strings.EqualFold(str(n["online"]), "true") {
				d.Online++
			}
			// 待审批路由 = 公告的全部路由 − 已批准的路由。
			d.PendingRoutes += len(pendingRoutes(stringList(n["available_routes"]), stringList(n["approved_routes"])))
		}
		d.Recent = nodes
		if len(d.Recent) > 6 {
			d.Recent = d.Recent[:6]
		}
	}
	if v, err := a.hs.cachedPreauthKeys(); err == nil {
		d.PreauthKeys = len(v)
	}
	if v, err := a.hs.cachedAPIKeys(); err == nil {
		d.APIKeys = len(v)
	}
	a.render(w, r, "dashboard", d)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	// 登出只校验 CSRF，不做角色判定：只读账号也要能退出登录，
	// 否则它只能靠清 Cookie 或等会话过期才能离开。
	s := sessionFrom(r)
	if !validCSRF(r, s) {
		http.Error(w, tr(langFrom(r), "msg.csrfFailed"), http.StatusBadRequest)
		return
	}
	a.sessions.Delete(s.Token)
	a.sessions.ClearCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) registerActions(p *http.ServeMux) {
	p.HandleFunc("POST /logout", a.logout)
	p.HandleFunc("POST /users/create", a.userCreate)
	p.HandleFunc("POST /users/rename", a.userRename)
	p.HandleFunc("POST /users/delete", a.userDelete)
	p.HandleFunc("POST /nodes/rename", a.nodeRename)
	p.HandleFunc("POST /nodes/expire", a.nodeExpire)
	p.HandleFunc("POST /nodes/delete", a.nodeDelete)
	p.HandleFunc("POST /nodes/tag", a.nodeTag)
	p.HandleFunc("POST /nodes/approve", a.nodeApprove)
	p.HandleFunc("POST /keys/preauth/create", a.preauthCreate)
	p.HandleFunc("POST /keys/preauth/note", a.preauthNote)
	p.HandleFunc("POST /keys/preauth/expire", a.preauthExpire)
	p.HandleFunc("POST /keys/preauth/delete", a.preauthDelete)
	p.HandleFunc("POST /keys/api/create", a.apiKeyCreate)
	p.HandleFunc("POST /keys/api/expire", a.apiKeyExpire)
	p.HandleFunc("POST /keys/api/delete", a.apiKeyDelete)
	p.HandleFunc("POST /network/acl", a.saveACL)
	p.HandleFunc("POST /network/config", a.saveConfig)
	p.HandleFunc("POST /network/dns", a.saveDNS)
	p.HandleFunc("POST /network/derp", a.saveDERP)
	p.HandleFunc("POST /settings/domain", a.settingsDomain)
	p.HandleFunc("POST /settings/network", a.settingsNetwork)
	p.HandleFunc("POST /settings/tls", a.settingsTLS)
	p.HandleFunc("POST /settings/renew", a.settingsRenew)
	p.HandleFunc("POST /settings/admin-tls", a.settingsAdminTLS)
	p.HandleFunc("POST /settings/renew-timer", a.settingsRenewTimer)
	p.HandleFunc("POST /ops/restart", a.restartHeadscale)
	p.HandleFunc("POST /admins/create", a.adminCreate)
	p.HandleFunc("POST /admins/delete", a.adminDelete)
	p.HandleFunc("POST /admins/role", a.adminRole)
	p.HandleFunc("POST /password", a.passwordChange)
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fmtRate(v float64) string {
	if v >= 1024 {
		return fmt.Sprintf("%.2f MB/s", v/1024)
	}
	if v >= 1 {
		return fmt.Sprintf("%.1f KB/s", v)
	}
	return fmt.Sprintf("%.0f B/s", v*1024)
}

func fmtBytes(v uint64) string {
	if v >= 1<<30 {
		return fmt.Sprintf("%.2f GB", float64(v)/(1<<30))
	}
	if v >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(v)/(1<<20))
	}
	if v >= 1<<10 {
		return fmt.Sprintf("%.1f KB", float64(v)/(1<<10))
	}
	return fmt.Sprintf("%d B", v)
}
