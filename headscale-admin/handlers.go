package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
)

type nodesData struct {
	Nodes  []map[string]any
	Routes []map[string]any
}

type usersData struct {
	Users []HSUser
}

type keysData struct {
	Preauth   []map[string]any
	APIKeys   []map[string]any
	Users     []HSUser
	ServerURL string // 配置里的对外访问地址，自动填入接入命令
	JoinKey   string // 刚创建的预授权密钥（仅显示一次）
	JoinCmd   string // 拼接好的接入命令；缺地址或密钥时为空，前端显示占位提示
}

type networkData struct {
	ACL         string
	Config      string
	PolicyPath  string
	BaseDomain  string
	MagicDNS    string
	DERPEnabled string
	ServerURL   string
	ListenAddr  string
}

type opsData struct {
	Net        NetSnapshot
	Host       hostView
	Nodes      NodeSnapshot
	Alerts     []MonitorAlert
	Thresholds AlertThresholds
	Service    string
	Version    string
	Cert       string
	Logs       string
	ConfigPath string
	PolicyPath string
}

// monitorResponse 是 /api/monitor 的返回结构：一次请求带回本机资源、
// 节点状态和告警，前端轮询时不需要分别请求多个接口。
type monitorResponse struct {
	Host       hostView        `json:"host"`
	Nodes      NodeSnapshot    `json:"nodes"`
	Alerts     []MonitorAlert  `json:"alerts"`
	Thresholds AlertThresholds `json:"thresholds"`
	Updated    string          `json:"updated"`
}

type adminsData struct {
	Admins []*Admin
}

func (a *App) nodesPage(w http.ResponseWriter, r *http.Request) {
	d := nodesData{Nodes: []map[string]any{}, Routes: []map[string]any{}}
	if v, err := a.hs.Nodes(); err == nil {
		d.Nodes = v
	} else {
		a.sessions.SetFlash(sessionFrom(r).Token, tr(langFrom(r), "msg.readNodesFail", err.Error()))
	}
	if v, err := a.hs.Routes(); err == nil {
		d.Routes = v
	}
	a.render(w, r, "nodes", d)
}

func (a *App) usersPage(w http.ResponseWriter, r *http.Request) {
	d := usersData{Users: []HSUser{}}
	if v, err := a.hs.Users(); err == nil {
		d.Users = v
	} else {
		a.sessions.SetFlash(sessionFrom(r).Token, tr(langFrom(r), "msg.readUsersFail", err.Error()))
	}
	a.render(w, r, "users", d)
}

func (a *App) keysPage(w http.ResponseWriter, r *http.Request) {
	d := keysData{Preauth: []map[string]any{}, APIKeys: []map[string]any{}, Users: []HSUser{}}
	if v, err := a.hs.PreauthKeys(); err == nil {
		d.Preauth = v
	}
	if v, err := a.hs.APIKeys(); err == nil {
		d.APIKeys = v
	}
	if v, err := a.hs.Users(); err == nil {
		d.Users = v
	}
	// 对外访问地址以配置为准：绑定 IP/域名并保存后，这里自动带上。
	if v, err := a.hs.ReadConfigText(); err == nil {
		d.ServerURL = topValue(v, "server_url")
	}
	// 刚生成的密钥只显示这一次，取出来就清掉。
	d.JoinKey = a.joinKeys.Take(sessionFrom(r).Token)
	d.JoinCmd = joinCommand(d.ServerURL, d.JoinKey)
	a.render(w, r, "keys", d)
}

func (a *App) networkPage(w http.ResponseWriter, r *http.Request) {
	d := networkData{}
	if v, err := a.hs.Policy(langFrom(r)); err == nil {
		d.ACL = v
	}
	if v, err := a.hs.ReadConfigText(); err == nil {
		d.Config = v
		d.BaseDomain = nestedValue(v, "dns", "base_domain")
		d.MagicDNS = nestedValue(v, "dns", "magic_dns")
		d.DERPEnabled = deepValue(v, "derp", "enabled")
		d.ServerURL = topValue(v, "server_url")
		d.ListenAddr = topValue(v, "listen_addr")
	}
	d.PolicyPath = a.hs.PolicyPath(langFrom(r))
	a.render(w, r, "network", d)
}

func (a *App) opsPage(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	hostSnap := a.host.Snapshot()
	nodeSnap := a.nodes.Snapshot()
	d := opsData{
		Net:        a.net.Snapshot(),
		Host:       newHostView(hostSnap, lang),
		Nodes:      newNodesView(nodeSnap, lang),
		Thresholds: a.thresholds,
		Service:    nodeSnap.Service,
		Version:    a.hs.Version(),
		Cert:       certExpiry(),
		Logs:       journal(80),
		ConfigPath: a.hs.Config,
		PolicyPath: a.hs.PolicyPath(lang),
	}
	d.Alerts = evaluateAlerts(alertInput{Host: hostSnap, Nodes: nodeSnap, Lang: lang}, a.thresholds)
	if d.Service == "" {
		d.Service = tr(lang, "state.unknown")
	}
	a.render(w, r, "ops", d)
}

func (a *App) adminsPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "admins", adminsData{Admins: a.store.List()})
}

func (a *App) userCreate(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		a.redirectWithFlash(w, r, "/users", tr(langFrom(r), "msg.usernameRequired"))
		return
	}
	args := []string{"users", "create", name}
	for _, pair := range [][2]string{{"display_name", "--display-name"}, {"email", "--email"}, {"picture_url", "--picture-url"}} {
		if v := strings.TrimSpace(r.FormValue(pair[0])); v != "" {
			args = append(args, pair[1], v)
		}
	}
	a.doAction(w, r, "/users", func() (string, error) { return a.hs.Action(args...) })
}

func (a *App) userRename(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/users", err.Error())
		return
	}
	newName := strings.TrimSpace(r.FormValue("new_name"))
	if newName == "" {
		a.redirectWithFlash(w, r, "/users", tr(langFrom(r), "msg.newNameRequired"))
		return
	}
	a.doAction(w, r, "/users", func() (string, error) {
		return a.hs.Action("users", "rename", "-i", str(id), "-r", newName, "--force")
	})
}

func (a *App) userDelete(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/users", err.Error())
		return
	}
	a.doAction(w, r, "/users", func() (string, error) {
		return a.hs.Action("users", "destroy", "-i", str(id), "--force")
	})
}

func (a *App) nodeRename(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/nodes", err.Error())
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	a.doAction(w, r, "/nodes", func() (string, error) {
		return a.hs.Action("nodes", "rename", name, "-i", str(id), "--force")
	})
}

func (a *App) nodeExpire(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/nodes", err.Error())
		return
	}
	args := []string{"nodes", "expire", "-i", str(id), "--force"}
	if v := strings.TrimSpace(r.FormValue("expiry")); v != "" {
		args = append(args, "--expiry", v)
	}
	a.doAction(w, r, "/nodes", func() (string, error) { return a.hs.Action(args...) })
}

func (a *App) nodeDelete(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/nodes", err.Error())
		return
	}
	a.doAction(w, r, "/nodes", func() (string, error) {
		return a.hs.Action("nodes", "delete", "-i", str(id), "--force")
	})
}

func (a *App) nodeTag(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/nodes", err.Error())
		return
	}
	a.doAction(w, r, "/nodes", func() (string, error) {
		return a.hs.Action("nodes", "tag", "-i", str(id), "-t", r.FormValue("tags"), "--force")
	})
}

func (a *App) nodeApprove(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/nodes", err.Error())
		return
	}
	a.doAction(w, r, "/nodes", func() (string, error) {
		return a.hs.Action("nodes", "approve-routes", "-i", str(id), "-r", r.FormValue("routes"), "--force")
	})
}

// normalizeExpiry 只接受下拉里的固定取值，把「长期」映射成 100 年。
func normalizeExpiry(v, fallback string) string {
	switch strings.TrimSpace(v) {
	case "1h", "24h", "7d", "30d", "90d", "1y":
		return strings.TrimSpace(v)
	case "long":
		return "100y"
	default:
		return fallback
	}
}

func extractKey(out string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err == nil {
		if k := str(m["key"]); k != "" {
			return k
		}
	}
	return strings.TrimSpace(out)
}

func (a *App) preauthCreate(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	userID := strings.TrimSpace(r.FormValue("user_id"))
	if userID == "" {
		a.redirectWithFlash(w, r, "/keys", tr(langFrom(r), "msg.selectUser"))
		return
	}
	exp := normalizeExpiry(r.FormValue("expiration"), "24h")
	out, err := a.hs.CreatePreauthKey(userID, exp, r.FormValue("reusable") == "on", r.FormValue("ephemeral") == "on", strings.TrimSpace(r.FormValue("tags")))
	if err != nil {
		a.redirectWithFlash(w, r, "/keys", tr(langFrom(r), "msg.generateFail", err.Error()))
		return
	}
	// 密钥只显示一次：暂存到会话，重定向后的密钥页自动填入接入命令。
	a.joinKeys.Set(sessionFrom(r).Token, extractKey(out))
	a.redirectWithFlash(w, r, "/keys", tr(langFrom(r), "msg.preauthOnce"))
}

func (a *App) preauthExpire(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/keys", err.Error())
		return
	}
	a.doAction(w, r, "/keys", func() (string, error) { return a.hs.Action("preauthkeys", "expire", "-i", str(id), "--force") })
}

func (a *App) preauthDelete(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/keys", err.Error())
		return
	}
	a.doAction(w, r, "/keys", func() (string, error) { return a.hs.Action("preauthkeys", "delete", "-i", str(id), "--force") })
}

func (a *App) apiKeyCreate(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	exp := normalizeExpiry(r.FormValue("expiration"), "90d")
	out, err := a.hs.CreateAPIKey(exp)
	if err != nil {
		a.redirectWithFlash(w, r, "/keys", tr(langFrom(r), "msg.generateFail", err.Error()))
		return
	}
	a.redirectWithFlash(w, r, "/keys", tr(langFrom(r), "msg.apiOnce", extractKey(out)))
}

func (a *App) apiKeyExpire(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/keys", err.Error())
		return
	}
	a.doAction(w, r, "/keys", func() (string, error) { return a.hs.Action("apikeys", "expire", "-i", str(id), "--force") })
}

func (a *App) apiKeyDelete(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/keys", err.Error())
		return
	}
	a.doAction(w, r, "/keys", func() (string, error) { return a.hs.Action("apikeys", "delete", "-i", str(id), "--force") })
}

func (a *App) saveACL(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	content := r.FormValue("acl")
	if strings.TrimSpace(content) == "" {
		a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.aclRequired"))
		return
	}
	if err := a.hs.SavePolicy(content, langFrom(r)); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.saveFail", err.Error()))
		return
	}
	if out, err := a.hs.Action("configtest", "-c", a.hs.Config); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.savedInvalid", err.Error(), out))
		return
	}
	a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.aclSaved"))
}

func (a *App) saveConfig(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	content := r.FormValue("config")
	if strings.TrimSpace(content) == "" {
		a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.configRequired"))
		return
	}
	if err := a.hs.SaveConfig(content); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.saveFail", err.Error()))
		return
	}
	if out, err := a.hs.Action("configtest", "-c", a.hs.Config); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.savedInvalid", err.Error(), out))
		return
	}
	a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.configSaved"))
}

func (a *App) saveDNS(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	base := strings.TrimSpace(r.FormValue("base_domain"))
	if base == "" {
		a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.baseRequired"))
		return
	}
	magic := "false"
	if r.FormValue("magic_dns") == "on" {
		magic = "true"
	}
	if err := a.hs.UpdateNested("dns", "base_domain", base, langFrom(r)); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.saveFail", err.Error()))
		return
	}
	if err := a.hs.UpdateNested("dns", "magic_dns", magic, langFrom(r)); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.saveFail", err.Error()))
		return
	}
	a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.dnsSaved"))
}

func (a *App) saveDERP(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	enabled := "false"
	if r.FormValue("enabled") == "on" {
		enabled = "true"
	}
	if err := a.hs.UpdateDERP("enabled", enabled, langFrom(r)); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.saveFail", err.Error()))
		return
	}
	a.redirectWithFlash(w, r, "/network", tr(langFrom(r), "msg.derpSaved"))
}

func (a *App) restartHeadscale(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	out, err := systemctl("restart", "headscale")
	// 立即刷新一次，避免页面沿用重启前的服务状态与节点快照。
	a.nodes.sample()
	if err != nil {
		a.redirectWithFlash(w, r, "/ops", tr(langFrom(r), "msg.restartFail", err.Error(), out))
		return
	}
	a.redirectWithFlash(w, r, "/ops", tr(langFrom(r), "msg.restarted"))
}

func (a *App) adminCreate(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	user := strings.TrimSpace(r.FormValue("username"))
	pass := r.FormValue("password")
	role := r.FormValue("role")
	if user == "" || pass == "" {
		a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.adminRequired"))
		return
	}
	if err := a.store.CreateAdmin(user, pass, role, langFrom(r)); err != nil {
		a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.createFail", err.Error()))
		return
	}
	a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.adminCreated", user))
}

func (a *App) adminDelete(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	user := strings.TrimSpace(r.FormValue("username"))
	if err := a.store.DeleteAdmin(user, langFrom(r)); err != nil {
		a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.deleteFail", err.Error()))
		return
	}
	a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.adminDeleted", user))
}

func (a *App) adminRole(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	user := strings.TrimSpace(r.FormValue("username"))
	if err := a.store.SetRole(user, r.FormValue("role"), langFrom(r)); err != nil {
		a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.roleFail", err.Error()))
		return
	}
	a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.roleUpdated"))
}

func (a *App) passwordChange(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	s := sessionFrom(r)
	if _, ok := a.store.Verify(s.Username, r.FormValue("old_password")); !ok {
		a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.oldPassWrong"))
		return
	}
	newPass := r.FormValue("new_password")
	if len(newPass) < 8 {
		a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.newPassShort"))
		return
	}
	if err := a.store.ChangePassword(s.Username, newPass, langFrom(r)); err != nil {
		a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.updateFail", err.Error()))
		return
	}
	a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.passChanged"))
}

// netmonAPI 供运维页定时刷新实时网速。
func (a *App) netmonAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(a.net.Snapshot())
}

// monitorAPI 返回本机资源、节点状态与当前告警，供运维页每 5 秒轮询。
func (a *App) monitorAPI(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	host := a.host.Snapshot()
	nodes := a.nodes.Snapshot()
	resp := monitorResponse{
		Host:       newHostView(host, lang),
		Nodes:      newNodesView(nodes, lang),
		Alerts:     evaluateAlerts(alertInput{Host: host, Nodes: nodes, Lang: lang}, a.thresholds),
		Thresholds: a.thresholds,
		Updated:    nodes.Updated,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// ---------- Headscale 配置管理 ----------

func (a *App) settingsPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "settings", a.hs.loadConfigPage(langFrom(r)))
}

// settingsDomain 保存域名与接入配置（server_url / MagicDNS 根域 / MagicDNS 开关）。
func (a *App) settingsDomain(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	lang := langFrom(r)
	p := a.hs.loadConfigPage(lang)
	p.ServerURL = strings.TrimSpace(r.FormValue("server_url"))
	p.BaseDomain = strings.TrimSpace(r.FormValue("base_domain"))
	p.MagicDNS = r.FormValue("magic_dns") == "on"

	if errs := validateDomain(p, lang); len(errs) > 0 {
		p.Errors = errs
		p.Warnings = configWarnings(p, lang)
		a.render(w, r, "settings", p)
		return
	}
	next, err := a.hs.buildConfig(p, "domain")
	if err != nil {
		p.Errors = map[string]string{"server_url": err.Error()}
		a.render(w, r, "settings", p)
		return
	}
	a.applySettings(w, r, p, next, "cfg.msg.domainSaved", nil)
}

// settingsNetwork 保存监听地址与客户端地址段。
func (a *App) settingsNetwork(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	lang := langFrom(r)
	p := a.hs.loadConfigPage(lang)
	p.ListenAddr = strings.TrimSpace(r.FormValue("listen_addr"))
	p.MetricsListenAddr = strings.TrimSpace(r.FormValue("metrics_listen_addr"))
	p.GRPCListenAddr = strings.TrimSpace(r.FormValue("grpc_listen_addr"))
	p.PrefixV4 = strings.TrimSpace(r.FormValue("prefix_v4"))
	p.PrefixV6 = strings.TrimSpace(r.FormValue("prefix_v6"))

	errs := validateNetwork(p, lang)
	// 监听端口变化会影响 server_url 端口，这里一并校验。
	for k, v := range validateDomain(p, lang) {
		if k == "listen_addr" {
			errs[k] = v
		}
	}
	if len(errs) > 0 {
		p.Errors = errs
		p.Warnings = configWarnings(p, lang)
		a.render(w, r, "settings", p)
		return
	}
	next, err := a.hs.buildConfig(p, "network")
	if err != nil {
		p.Errors = map[string]string{"listen_addr": err.Error()}
		a.render(w, r, "settings", p)
		return
	}
	a.applySettings(w, r, p, next, "cfg.msg.networkSaved", nil)
}

// formField 判断字段是否真的随请求提交。隐藏的证书分区可能不提交某些字段，
// 这时应沿用已有值，而不是把它们清空。
func formField(r *http.Request, key string) (string, bool) {
	if _, ok := r.Form[key]; !ok {
		return "", false
	}
	return strings.TrimSpace(r.Form.Get(key)), true
}

// settingsTLS 保存证书方式。手动方式接收用户粘贴的证书与私钥文本，
// 校验通过后写入固定位置，再交给 headscale 配置使用。
func (a *App) settingsTLS(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	_ = r.ParseForm()
	lang := langFrom(r)
	p := a.hs.loadConfigPage(lang)
	p.TLSMode = strings.TrimSpace(r.FormValue("tls_mode"))
	if v, ok := formField(r, "acme_email"); ok {
		p.ACMEEmail = v
	}
	if v, ok := formField(r, "acme_url"); ok && v != "" {
		p.ACMEURL = v
	}
	if v, ok := formField(r, "tls_letsencrypt_challenge_type"); ok && v != "" {
		p.ChallengeType = v
	}
	if v, ok := formField(r, "tls_letsencrypt_listen"); ok && v != "" {
		p.LEListen = v
	}
	if v, ok := formField(r, "tls_letsencrypt_cache_dir"); ok && v != "" {
		p.LECacheDir = v
	}
	p.CertPEM = strings.TrimSpace(r.FormValue("tls_cert_pem"))
	p.KeyPEM = strings.TrimSpace(r.FormValue("tls_key_pem"))
	host := strings.TrimSpace(r.FormValue("tls_letsencrypt_hostname"))

	switch p.TLSMode {
	case "letsencrypt":
		p.LEHostname = host
		p.CertPath, p.KeyPath = "", ""
	case "manual":
		// 沿用已配置的证书路径；粘贴了新内容会在下面覆盖。
		p.LEHostname = ""
	case "http":
		p.LEHostname, p.CertPath, p.KeyPath = "", "", ""
	}
	if p.LECacheDir == "" {
		p.LECacheDir = "/var/lib/headscale/cache"
	}

	if errs := validateTLS(p, lang); len(errs) > 0 {
		p.Errors = errs
		p.Warnings = configWarnings(p, lang)
		a.render(w, r, "settings", p)
		return
	}

	// 只有手动方式且确实粘贴了内容时才落盘，避免空提交覆盖已有证书。
	rollback := func() {}
	if p.TLSMode == "manual" && p.CertPEM != "" {
		certPath, keyPath, undo, err := saveManualCert(p.CertPEM, p.KeyPEM)
		if err != nil {
			p.Errors = map[string]string{"cert_pem": tr(lang, "cfg.err.certSave", err.Error())}
			p.Warnings = configWarnings(p, lang)
			a.render(w, r, "settings", p)
			return
		}
		p.CertPath, p.KeyPath, rollback = certPath, keyPath, undo
	}

	next, err := a.hs.buildConfig(p, "tls")
	if err != nil {
		rollback()
		p.Errors = map[string]string{"tls_mode": err.Error()}
		p.Warnings = configWarnings(p, lang)
		a.render(w, r, "settings", p)
		return
	}
	a.applySettings(w, r, p, next, "cfg.msg.tlsSaved", rollback)
}

// applySettings 应用配置文本；失败时调用 rollback（可为 nil）撤销附带副作用。
func (a *App) applySettings(w http.ResponseWriter, r *http.Request, p *ConfigPage, next, successKey string, rollback func()) {
	lang := langFrom(r)
	restart := r.FormValue("apply_now") == "on"
	bak, err := a.hs.ApplyConfig(next, restart, lang)
	if err != nil {
		if rollback != nil {
			rollback()
		}
		p.Errors = map[string]string{"_form": err.Error()}
		p.Warnings = configWarnings(p, lang)
		fresh := a.hs.loadConfigPage(lang)
		fresh.Errors = p.Errors
		a.render(w, r, "settings", fresh)
		return
	}
	msg := tr(lang, successKey)
	if restart {
		msg += tr(lang, "cfg.msg.applied")
	} else {
		msg += tr(lang, "cfg.msg.pendingRestart")
	}
	if bak != "" {
		msg += tr(lang, "cfg.msg.backup", filepath.Base(bak))
	}
	a.redirectWithFlash(w, r, "/settings", msg)
}

// settingsRenew 手动触发 certbot 续期，成功后续期并重启 headscale。
func (a *App) settingsRenew(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	out, err := certRenew(langFrom(r))
	if err != nil {
		a.redirectWithFlash(w, r, "/settings", err.Error())
		return
	}
	if len(out) > 400 {
		out = out[len(out)-400:]
	}
	a.redirectWithFlash(w, r, "/settings", tr(langFrom(r), "cfg.msg.renewed", strings.TrimSpace(out)))
}

// settingsRenewTimer 启停 certbot.timer 并设置每日检查时间。
func (a *App) settingsRenewTimer(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	lang := langFrom(r)
	enable := r.FormValue("enabled") == "on"
	schedule := strings.TrimSpace(r.FormValue("schedule"))
	if err := setRenewTimer(enable, schedule, lang); err != nil {
		a.redirectWithFlash(w, r, "/settings", tr(lang, "cfg.msg.renewSaveFail", err.Error()))
		return
	}
	a.redirectWithFlash(w, r, "/settings", tr(lang, "cfg.msg.renewSaved"))
}
