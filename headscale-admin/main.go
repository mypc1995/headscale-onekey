package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type App struct {
	store    *AuthStore
	sessions *SessionManager
	hs       *HSClient
	tmpl     *template.Template
	port     string
	tplDir   string
	staticDir string
}

type Page struct {
	Title    string
	Active   string
	CSRF     string
	Flash    string
	Username string
	Role     string
	Data     any
	Year     int
}

func main() {
	port := flag.String("port", envDefault("HSA_PORT", "3001"), "监听端口")
	dataFile := flag.String("data", envDefault("HSA_DATA_FILE", "/var/lib/headscale-admin/admins.json"), "管理员数据文件")
	tplDir := flag.String("templates", envDefault("HSA_TEMPLATES", "/opt/headscale-admin/templates"), "模板目录")
	staticDir := flag.String("static", envDefault("HSA_STATIC", "/opt/headscale-admin/static"), "静态资源目录")
	initAdmin := flag.String("init-admin", "", "初始化管理员，格式 用户名:密码")
	resetAdmin := flag.String("reset-admin", "", "重置管理员密码，格式 用户名:密码")
	adminUser := flag.String("admin-user", "admin", "初始管理员用户名")
	adminPass := flag.String("admin-pass", "", "初始管理员密码（配合 -init-only 使用）")
	secureCookie := flag.Bool("cookie-secure", os.Getenv("HSA_COOKIE_SECURE") == "1", "Cookie 增加 Secure 标记")
	initOnly := flag.Bool("init-only", false, "初始化管理员后立即退出")
	flag.Parse()

	store, err := LoadAuthStore(*dataFile)
	if err != nil {
		log.Fatalf("加载管理员数据失败: %v", err)
	}
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
		"field":    fieldValue,
		"tf":       protoTime,
		"userOf":   userOf,
		"join":     strings.Join,
		"list":     joinAny,
		"has":      strings.Contains,
		"short":    func(s string, n int) string { if len(s) > n { return s[:n] + "…" }; return s },
		"yearNow":  func() int { return time.Now().Year() },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseGlob(filepath.Join(*tplDir, "*.html"))
	if err != nil {
		log.Fatalf("加载模板失败: %v", err)
	}

	a := &App{
		store: store, sessions: NewSessionManager(*secureCookie), hs: NewHSClient(),
		tmpl: tmpl, port: *port, tplDir: *tplDir, staticDir: *staticDir,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.loginPost)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(*staticDir))))
	mux.Handle("GET /health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))

	p := http.NewServeMux()
	p.HandleFunc("GET /", a.dashboard)
	p.HandleFunc("GET /nodes", a.nodesPage)
	p.HandleFunc("GET /users", a.usersPage)
	p.HandleFunc("GET /keys", a.keysPage)
	p.HandleFunc("GET /network", a.networkPage)
	p.HandleFunc("GET /ops", a.opsPage)
	p.HandleFunc("GET /admins", a.adminsPage)
	a.registerActions(p)
	mux.Handle("/", a.sessions.Wrap(p))

	addr := ":" + *port
	log.Printf("Headscale Admin 监听 %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
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
	"dashboard": "总览", "nodes": "节点管理", "users": "用户管理", "keys": "密钥与接入",
	"network": "网络与策略", "ops": "运维与监控", "admins": "管理员",
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	s := sessionFrom(r)
	title := pageTitles[name]
	if title == "" {
		title = name
	}
	p := Page{Title: title, Active: name, CSRF: s.CSRF, Username: s.Username, Role: s.Role, Data: data, Year: time.Now().Year()}
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
	if !validCSRF(r, s) {
		http.Error(w, "CSRF 校验失败", http.StatusBadRequest)
		return false
	}
	if s.Role != "admin" {
		ref := r.Header.Get("Referer")
		if ref == "" {
			ref = "/"
		}
		a.redirectWithFlash(w, r, ref, "只读账号无权修改")
		return false
	}
	return true
}

func formUint(r *http.Request, key string) (uint64, error) {
	v := strings.TrimSpace(r.FormValue(key))
	if v == "" {
		return 0, errors.New("缺少参数 " + key)
	}
	return strconv.ParseUint(v, 10, 64)
}

func (a *App) doAction(w http.ResponseWriter, r *http.Request, to string, fn func() (string, error)) {
	out, err := fn()
	if err != nil {
		a.redirectWithFlash(w, r, to, "操作失败："+err.Error())
		return
	}
	msg := strings.TrimSpace(out)
	if len(msg) > 120 {
		msg = msg[:120] + "…"
	}
	if msg == "" {
		msg = "操作成功"
	} else {
		msg = "操作成功：" + msg
	}
	a.redirectWithFlash(w, r, to, msg)
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, ok := a.sessions.Get(c.Value); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = a.tmpl.ExecuteTemplate(w, "login", map[string]any{"Error": ""})
}

func (a *App) loginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", http.StatusBadRequest)
		return
	}
	admin, ok := a.store.Verify(r.FormValue("username"), r.FormValue("password"))
	if !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = a.tmpl.ExecuteTemplate(w, "login", map[string]any{"Error": "用户名或密码错误"})
		return
	}
	s := a.sessions.Create(admin.Username, admin.Role)
	a.sessions.SetCookie(w, s)
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
	if !a.mutate(w, r) {
		return
	}
	a.sessions.Delete(sessionFrom(r).Token)
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
	p.HandleFunc("POST /keys/preauth/expire", a.preauthExpire)
	p.HandleFunc("POST /keys/preauth/delete", a.preauthDelete)
	p.HandleFunc("POST /keys/api/create", a.apiKeyCreate)
	p.HandleFunc("POST /keys/api/expire", a.apiKeyExpire)
	p.HandleFunc("POST /keys/api/delete", a.apiKeyDelete)
	p.HandleFunc("POST /network/acl", a.saveACL)
	p.HandleFunc("POST /network/config", a.saveConfig)
	p.HandleFunc("POST /network/dns", a.saveDNS)
	p.HandleFunc("POST /network/derp", a.saveDERP)
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
