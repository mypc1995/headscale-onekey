package main

import (
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
	"strconv"
	"strings"
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
	dbFile := flag.String("data", envDefault("HSA_DATA_FILE", "/var/lib/headscale-admin/headscale-admin.db"), "SQLite 数据库路径")
	legacyJSON := flag.String("legacy-json", envDefault("HSA_LEGACY_JSON", "/var/lib/headscale-admin/admins.json"), "旧版管理员 JSON，首次启动时自动迁移")
	tplDir := flag.String("templates", envDefault("HSA_TEMPLATES", "/opt/headscale-admin/templates"), "模板目录")
	staticDir := flag.String("static", envDefault("HSA_STATIC", "/opt/headscale-admin/static"), "静态资源目录")
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
	// HTTPS 开启时 Cookie 自动加 Secure。
	adminTLSOn := loadAdminTLS(*dbFile).Enabled
	secure := *secureCookie || adminTLSOn
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

	funcs := template.FuncMap{
		"field":      fieldValue,
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
		speed: reporter, login: newLoginGuard(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.loginPost)
	mux.HandleFunc("GET /lang", a.setLanguage)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(*staticDir))))
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
	// 管理界面自身的 HTTPS：设置页写入 admin-tls.json 后重启生效。
	tlsCfg := loadAdminTLS(*dbFile)
	if tlsCfg.Enabled {
		if err := tlsCfg.validPair(); err != nil {
			log.Printf("管理界面 HTTPS 证书不可用，回退为 HTTP：%v", err)
			tlsCfg.Enabled = false
		}
	}
	// 必须显式设置超时：http.ListenAndServe 的默认 Server 一个超时都没有，
	// 慢速连接（Slowloris）可以把连接数占满，拖垮整台机器。
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	if tlsCfg.Enabled {
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

func (a *App) doAction(w http.ResponseWriter, r *http.Request, to string, fn func() (string, error)) {
	lang := langFrom(r)
	out, err := fn()
	if err != nil {
		a.redirectWithFlash(w, r, to, tr(lang, "msg.operationFailed", err.Error()))
		return
	}
	msg := strings.TrimSpace(out)
	if len(msg) > 160 {
		msg = msg[:160] + "…"
	}
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

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, ok := a.sessions.Get(c.Value); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	lang := langFrom(r)
	data := map[string]any{"Error": "", "Lang": lang}
	// 改密之后旧会话会被吊销，这里给一句说明，别让人以为是登录失败。
	if r.URL.Query().Get("passchanged") == "1" {
		data["Notice"] = tr(lang, "msg.passChangedRelogin")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = a.tmpl.ExecuteTemplate(w, "login", data)
}

func (a *App) loginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, tr(langFrom(r), "msg.operationFailed", err.Error()), http.StatusBadRequest)
		return
	}
	lang := langFrom(r)
	user := strings.TrimSpace(r.FormValue("username"))
	ip := clientIP(r)

	// 先看惩罚：被锁定时直接拒绝，不再消耗一次 KDF；
	// 只是延迟时先等一下，抬高爆破的时间成本。
	wait, locked := a.login.penalty(user, ip)
	if locked {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = a.tmpl.ExecuteTemplate(w, "login", map[string]any{"Error": tr(lang, "login.tooMany"), "Lang": lang})
		return
	}
	if wait > 0 {
		time.Sleep(wait)
	}

	admin, ok := a.store.Verify(user, r.FormValue("password"))
	if !ok {
		a.login.fail(user, ip)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = a.tmpl.ExecuteTemplate(w, "login", map[string]any{"Error": tr(lang, "login.error"), "Lang": lang})
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
	Users, Nodes, Online, Routes, PreauthKeys, APIKeys int
	Service, Version, Cert                             string
	Recent                                             []map[string]any
}

func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	d := dashboardData{Service: "未知", Version: a.hs.Version(), Cert: certExpiry()}
	if out, err := systemctl("is-active", "headscale"); err == nil {
		d.Service = strings.TrimSpace(out)
	}
	if users, err := a.hs.Users(); err == nil {
		d.Users = len(users)
	}
	if nodes, err := a.hs.Nodes(); err == nil {
		d.Nodes = len(nodes)
		for _, n := range nodes {
			if strings.EqualFold(str(n["online"]), "true") {
				d.Online++
			}
		}
		d.Recent = nodes
		if len(d.Recent) > 6 {
			d.Recent = d.Recent[:6]
		}
	}
	if v, err := a.hs.Routes(); err == nil {
		d.Routes = len(v)
	}
	if v, err := a.hs.PreauthKeys(); err == nil {
		d.PreauthKeys = len(v)
	}
	if v, err := a.hs.APIKeys(); err == nil {
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
