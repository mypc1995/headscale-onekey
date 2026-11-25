package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	ServerURL string            // 配置里的对外访问地址，自动填入接入命令
	JoinKey   string            // 刚创建的预授权密钥（仅显示一次）
	JoinCmd   string            // 拼接好的接入命令；缺地址或密钥时为空，前端显示占位提示
	Notes     map[string]string // 预授权密钥备注，键为密钥 ID 字符串
	NewAPIKey string            // 刚创建的 API key（仅显示一次，醒目展示防丢失）
}

type networkData struct {
	ACL            string
	Config         string
	PolicyPath     string
	BaseDomain     string
	MagicDNS       string
	OverrideLocal  string
	Nameservers    []string
	SearchDomains  []string
	DERPEnabled    string
	DERPRegionID   string
	DERPRegionName string
	ServerURL      string
	ListenAddr     string
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
		// 待批准 = 节点公告的全部路由 − 已批准的路由。
		// 审批队列只放还有待批准路由的节点：批完之后这一行就从队列里消失，
		// 不会一直挂在「待审批」里。
		for _, n := range v {
			pending := pendingRoutes(stringList(n["available_routes"]), stringList(n["approved_routes"]))
			n["pending_routes"] = pending
			if len(pending) > 0 {
				d.Routes = append(d.Routes, n)
			}
		}
	} else {
		a.sessions.SetFlash(sessionFrom(r).Token, tr(langFrom(r), "msg.readNodesFail", err.Error()))
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
	d := keysData{Preauth: []map[string]any{}, APIKeys: []map[string]any{}, Users: []HSUser{}, Notes: map[string]string{}}
	if v, err := a.hs.PreauthKeys(); err == nil {
		d.Preauth = v
	}
	if v, err := a.hs.APIKeys(); err == nil {
		d.APIKeys = v
	}
	if v, err := a.hs.Users(); err == nil {
		d.Users = v
	}
	// 备注与密钥 ID 关联；密钥被删后备注一并清理。
	notes := a.store.KeyNotes("preauth")
	alive := map[int64]bool{}
	for _, k := range d.Preauth {
		if id := keyID(k["id"]); id > 0 {
			alive[id] = true
			if n := notes[id]; n != "" {
				d.Notes[strconv.FormatInt(id, 10)] = n
			}
		}
	}
	a.store.PruneKeyNotes("preauth", alive)
	// 对外访问地址以配置为准：绑定 IP/域名并保存后，这里自动带上。
	if v, err := a.hs.ReadConfigText(); err == nil {
		d.ServerURL = topValue(v, "server_url")
	}
	// 刚生成的密钥只显示这一次，取出来就清掉。
	d.JoinKey = a.joinKeys.Take(sessionFrom(r).Token)
	d.JoinCmd = joinCommand(d.ServerURL, d.JoinKey)
	// API key 同理：创建后只在下一次页面加载中完整展示。
	d.NewAPIKey = a.apiKeys.Take(sessionFrom(r).Token)
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
		d.OverrideLocal = nestedValue(v, "dns", "override_local_dns")
		d.Nameservers = listValues(v, "dns", "nameservers", "global")
		d.SearchDomains = listValues(v, "dns", "search_domains")
		d.DERPEnabled = deepValue(v, "derp", "enabled")
		d.DERPRegionID = deepValue(v, "derp", "region_id")
		d.DERPRegionName = deepValue(v, "derp", "region_name")
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

// speedEndpoint 返回节点侧上报接口的完整地址，供管理员分发给节点 agent。
// 节点在 tailnet 内访问时，管理员应把地址里的主机名换成服务器的 tailnet IP，
// 否则节点解析不到。
func (a *App) speedEndpoint(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || loadAdminTLS(a.dataFile).Enabled {
		scheme = "https"
	}
	return scheme + "://" + a.trustedHost(r) + "/api/speed"
}

// trustedHost 决定上报地址里写哪个主机名。
//
// 请求里的 Host 头是客户端自己写的：伪造它就能让管理员复制到一个指向
// 别处的上报地址（钓鱼），所以不能无条件照抄。这里只用两类来源——
// 按地址直连（内网面板的常态）和配置里出现过的域名（运维自己在界面上
// 填的对外地址）。两者都不是就退回本机地址，管理员按提示换成 tailnet IP。
func (a *App) trustedHost(r *http.Request) string {
	host := strings.TrimSpace(r.Host)
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.Trim(name, "[]")
	if name == "" {
		return "127.0.0.1:" + a.port
	}
	// 地址形式的 Host：按 IP 直连，不是别人塞进来的域名。
	if net.ParseIP(name) != nil || strings.EqualFold(name, "localhost") {
		return host
	}
	if a.hostInConfig(name) {
		return host
	}
	return "127.0.0.1:" + a.port
}

// hostInConfig 判断主机名是否出现在 headscale 配置里（对外地址或 MagicDNS 根域）。
func (a *App) hostInConfig(name string) bool {
	if a.hs == nil {
		return false
	}
	raw, err := a.hs.ReadConfigText()
	if err != nil {
		return false
	}
	candidates := []string{topValue(raw, "server_url"), nestedValue(raw, "dns", "base_domain")}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		host := c
		if u, err := url.Parse(c); err == nil && u.Host != "" {
			host = u.Hostname()
		}
		if strings.EqualFold(host, name) {
			return true
		}
	}
	return false
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
	want, err := parseRouteList(r.FormValue("routes"))
	if err != nil {
		a.redirectWithFlash(w, r, "/nodes", err.Error())
		return
	}
	a.doAction(w, r, "/nodes", func() (string, error) {
		// headscale 的 approve-routes 是全量覆盖：只提交本次新批的路由，
		// 反而会把之前批准的抹掉。所以提交前先跟节点当前的已批准集合取并集。
		final, err := a.approvedRoutesOf(id, want)
		if err != nil {
			return "", err
		}
		return a.hs.Action("nodes", "approve-routes", "-i", str(id), "-r", strings.Join(final, ","), "--force")
	})
}

// approvedRoutesOf 取节点当前已批准的路由，与本次要批准的合并成最终集合。
// 节点读不到时按「只有本次提交的路由」处理，不阻断审批。
func (a *App) approvedRoutesOf(id uint64, want []string) ([]string, error) {
	nodes, err := a.hs.Nodes()
	if err != nil {
		return mergeRouteLists(nil, want), nil
	}
	return mergeRouteLists(approvedRoutesOfNode(nodes, id), want), nil
}

// approvedRoutesOfNode 从节点列表里取出指定节点已批准的路由。
func approvedRoutesOfNode(nodes []map[string]any, id uint64) []string {
	for _, n := range nodes {
		if nodeIDOf(n) == id {
			return stringList(n["approved_routes"])
		}
	}
	return nil
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
	raw := strings.TrimSpace(out)
	// apikeys create 的输出是裸 JSON 字符串："hskey-api-..."
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err == nil && s != "" {
		return s
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err == nil {
		// 预授权密钥创建返回 {"key": ...}，API key 也可能返回 {"apiKey": ...}。
		for _, name := range []string{"key", "apiKey", "api_key", "token"} {
			if k := str(m[name]); k != "" {
				return k
			}
		}
	}
	return raw
}

// keyID 从 JSON 值解析密钥 ID（protojson 里 int64 常序列化为字符串）。
func keyID(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case string:
		id, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return id
	}
	return 0
}

// extractKeyID 从 preauthkeys create 的输出里取新密钥的 ID。
func extractKeyID(out string) int64 {
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err == nil {
		return keyID(m["id"])
	}
	return 0
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
	// 创建时填的备注按新密钥 ID 存档，之后列表里可再修改。
	if note := strings.TrimSpace(r.FormValue("note")); note != "" {
		if id := extractKeyID(out); id > 0 {
			_ = a.store.SetKeyNote("preauth", id, note)
		}
	}
	// 密钥只显示一次：暂存到会话，重定向后的密钥页自动填入接入命令。
	a.joinKeys.Set(sessionFrom(r).Token, extractKey(out))
	a.redirectWithFlash(w, r, "/keys", tr(langFrom(r), "msg.preauthOnce"))
}

// preauthNote 保存单个预授权密钥的备注（空备注即清除）。
func (a *App) preauthNote(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	id, err := formUint(r, "id")
	if err != nil {
		a.redirectWithFlash(w, r, "/keys", err.Error())
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if len([]rune(note)) > 100 {
		note = string([]rune(note)[:100])
	}
	if err := a.store.SetKeyNote("preauth", int64(id), note); err != nil {
		a.redirectWithFlash(w, r, "/keys", tr(langFrom(r), "msg.saveFail", err.Error()))
		return
	}
	a.redirectWithFlash(w, r, "/keys", tr(langFrom(r), "msg.noteSaved"))
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
	a.doAction(w, r, "/keys", func() (string, error) {
		out, err := a.hs.Action("preauthkeys", "delete", "-i", str(id), "--force")
		if err == nil {
			a.store.DeleteKeyNote("preauth", int64(id))
		}
		return out, err
	})
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
	// API key 只在创建时返回完整值，暂存到会话，下一页醒目展示一次。
	a.apiKeys.Set(sessionFrom(r).Token, extractKey(out))
	a.redirectWithFlash(w, r, "/keys", tr(langFrom(r), "msg.apiOnce"))
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
	lang := langFrom(r)
	content := r.FormValue("acl")
	if strings.TrimSpace(content) == "" {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.aclRequired"))
		return
	}
	// 校验通过才落地：策略文件写坏了还留在盘上，headscale 下次启动会直接失败。
	if err := a.hs.SavePolicyChecked(content, lang); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.aclInvalid", err.Error()))
		return
	}
	a.redirectWithFlash(w, r, "/network", tr(lang, "msg.aclSaved"))
}

func (a *App) saveConfig(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	lang := langFrom(r)
	content := r.FormValue("config")
	if strings.TrimSpace(content) == "" {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.configRequired"))
		return
	}
	// 走「候选文件 → configtest → 备份 → 原子替换」的流程。
	// 旧实现是先 SaveConfig 落盘、再 configtest：校验没过的话，坏配置
	// 已经躺在生产配置上了，而且界面只提示一句「已保存但校验失败」——
	// 管理员很可能以为已经改好了。
	if _, err := a.hs.ApplyConfig(content, false, lang); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.configInvalid", err.Error()))
		return
	}
	a.redirectWithFlash(w, r, "/network", tr(lang, "msg.configSaved"))
}

func (a *App) saveDNS(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	lang := langFrom(r)
	base := strings.TrimSpace(r.FormValue("base_domain"))
	if base == "" {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.baseRequired"))
		return
	}
	// DNS 服务器与搜索域来自表单的动态行，逐条校验。
	ns, err := cleanRows(r.Form["ns"])
	if err != nil {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.invalidNS", err.Error()))
		return
	}
	search, err := cleanRows(r.Form["search"])
	if err != nil {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.invalidSearch", err.Error()))
		return
	}
	for _, v := range ns {
		if !validNameserver(v) {
			a.redirectWithFlash(w, r, "/network", tr(lang, "msg.invalidNS", v))
			return
		}
	}
	for _, v := range search {
		if !validSearchDomain(v) {
			a.redirectWithFlash(w, r, "/network", tr(lang, "msg.invalidSearch", v))
			return
		}
	}

	next, err := a.hs.ReadConfigText()
	if err != nil {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.saveFail", err.Error()))
		return
	}
	magic := "false"
	if r.FormValue("magic_dns") == "on" {
		magic = "true"
	}
	override := "false"
	if r.FormValue("override_local_dns") == "on" {
		override = "true"
	}
	for _, kv := range [][2]string{{"base_domain", yamlScalar(base)}, {"magic_dns", magic}, {"override_local_dns", override}} {
		if next, err = setNestedKey(next, "dns", kv[0], kv[1]); err != nil {
			a.redirectWithFlash(w, r, "/network", tr(lang, "msg.saveFail", err.Error()))
			return
		}
	}
	if next, err = setNestedList(next, ns, "dns", "nameservers", "global"); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.saveFail", err.Error()))
		return
	}
	if next, err = setNestedList(next, search, "dns", "search_domains"); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.saveFail", err.Error()))
		return
	}
	a.applyNetworkConfig(w, r, next, "msg.dnsSaved")
}

func (a *App) saveDERP(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	lang := langFrom(r)
	enabled := "false"
	if r.FormValue("enabled") == "on" {
		enabled = "true"
	}
	regionID := strings.TrimSpace(r.FormValue("region_id"))
	if regionID == "" {
		regionID = "900"
	}
	if _, err := strconv.ParseUint(regionID, 10, 16); err != nil {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.invalidRegionID", regionID))
		return
	}
	regionName := strings.TrimSpace(r.FormValue("region_name"))
	if regionName == "" {
		regionName = "Headscale Embedded DERP"
	}

	next, err := a.hs.ReadConfigText()
	if err != nil {
		a.redirectWithFlash(w, r, "/network", tr(lang, "msg.saveFail", err.Error()))
		return
	}
	for _, kv := range [][2]string{{"enabled", enabled}, {"region_id", regionID}, {"region_name", yamlScalar(regionName)}} {
		if next, err = setDeepKey(next, "derp", "server", kv[0], kv[1]); err != nil {
			a.redirectWithFlash(w, r, "/network", tr(lang, "msg.saveFail", err.Error()))
			return
		}
	}
	a.applyNetworkConfig(w, r, next, "msg.derpSaved")
}

// applyNetworkConfig 与配置管理页同一套保存逻辑：configtest 校验 → 备份 → 原子写入 → 可选重启。
func (a *App) applyNetworkConfig(w http.ResponseWriter, r *http.Request, next, successKey string) {
	lang := langFrom(r)
	restart := r.FormValue("apply_now") == "on"
	bak, err := a.hs.ApplyConfig(next, restart, lang)
	if err != nil {
		a.redirectWithFlash(w, r, "/network", err.Error())
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
	a.redirectWithFlash(w, r, "/network", msg)
}

// cleanRows 清洗动态行表单值：去空白、去空行。
func cleanRows(vals []string) ([]string, error) {
	var out []string
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

// validNameserver 接受 IP（v4/v6）或带协议的 DoH/DoT/DoQ 地址。
func validNameserver(v string) bool {
	if net.ParseIP(v) != nil {
		return true
	}
	if u, err := url.Parse(v); err == nil {
		switch u.Scheme {
		case "https", "tls", "quic":
			return u.Host != ""
		}
	}
	return false
}

// validSearchDomain 校验搜索域格式（字母数字、点、短横线）。
var searchDomainRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)

func validSearchDomain(v string) bool {
	return searchDomainRE.MatchString(v)
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
	// 创建路径要和改密路径用同一套口令长度要求，否则管理员能创建出
	// 一个 1 位口令的账号，而改密时又不允许这么短。
	if len(pass) < 8 {
		a.redirectWithFlash(w, r, "/admins", tr(langFrom(r), "msg.newPassShort"))
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
	// 改密会吊销该账号的全部会话（包含当前这个），所以不能再走 flash：
	// 会话已经没了，提示消息发不出去。改成清 Cookie 后跳回登录页。
	a.sessions.Delete(s.Token)
	a.sessions.ClearCookie(w)
	http.Redirect(w, r, "/login?passchanged=1", http.StatusSeeOther)
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
	a.renderSettings(w, r, a.loadSettingsPage(langFrom(r), r.TLS != nil))
}

// renderSettings 渲染配置页。节点网速上报通道的地址依赖请求的 Host、
// 令牌依赖会话角色，所以所有配置页的渲染都统一走这里，保证每次都带上
// 最新的通道状态——表单校验失败重新渲染时也不会漏掉这一段。
func (a *App) renderSettings(w http.ResponseWriter, r *http.Request, p *ConfigPage) {
	a.applySpeedSettings(r, p)
	a.render(w, r, "settings", p)
}

// applySpeedSettings 把节点网速上报通道的状态填进配置页模型。
// 上报令牌只下发给 admin 角色，readonly 账号看不到。
func (a *App) applySpeedSettings(r *http.Request, p *ConfigPage) {
	if p == nil {
		return
	}
	p.SpeedEndpoint = a.speedEndpoint(r)
	p.SpeedEnabled = a.speed != nil && a.speed.enabled()
	if a.nodes != nil {
		p.SpeedChannel = a.nodes.Snapshot().SpeedChannel
	}
	if s := sessionFrom(r); s != nil && s.Role == "admin" && a.speed != nil {
		p.SpeedToken = a.speed.tokenValue()
	}
}

// loadSettingsPage 在 headscale 配置之外补充管理界面自身的 HTTPS 状态。
func (a *App) loadSettingsPage(lang string, liveTLS bool) *ConfigPage {
	p := a.hs.loadConfigPage(lang)
	p.AdminTLS = loadAdminTLS(a.dataFile)
	p.AdminHTTPSLive = liveTLS
	if p.AdminTLS.Enabled {
		p.AdminTLSCert = describeCertFile(p.AdminTLS.CertPath, lang)
	}
	return p
}

// describeCertFile 简述证书文件（CN 与到期时间），用于管理界面 HTTPS 状态展示。
func describeCertFile(path, lang string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return tr(lang, "adminTLS.certUnreadable")
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return tr(lang, "adminTLS.certUnreadable")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return tr(lang, "adminTLS.certUnreadable")
	}
	return tr(lang, "adminTLS.certInfo", cert.Subject.CommonName, cert.NotAfter.Format("2006-01-02"))
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
		a.renderSettings(w, r, p)
		return
	}
	next, err := a.hs.buildConfig(p, "domain")
	if err != nil {
		p.Errors = map[string]string{"server_url": err.Error()}
		a.renderSettings(w, r, p)
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
	// IPv6 是可选开关：未勾选时清空 v6 网段，而不是沿用旧值。
	if r.FormValue("ipv6_enabled") == "on" {
		p.PrefixV6 = strings.TrimSpace(r.FormValue("prefix_v6"))
	} else {
		p.PrefixV6 = ""
	}

	errs := validateNetwork(p, lang)
	if r.FormValue("ipv6_enabled") == "on" && p.PrefixV6 == "" {
		errs["prefix_v6"] = tr(lang, "cfg.err.prefixV6Required")
	}
	// 监听端口变化会影响 server_url 端口，这里一并校验。
	for k, v := range validateDomain(p, lang) {
		if k == "listen_addr" {
			errs[k] = v
		}
	}
	if len(errs) > 0 {
		p.Errors = errs
		p.Warnings = configWarnings(p, lang)
		a.renderSettings(w, r, p)
		return
	}
	next, err := a.hs.buildConfig(p, "network")
	if err != nil {
		p.Errors = map[string]string{"listen_addr": err.Error()}
		a.renderSettings(w, r, p)
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

// settingsTLS 保存证书方式。手动方式可以上传证书文件（公钥/中间证书/私钥），
// 也可以粘贴 PEM 文本；校验通过后写入固定位置，再交给 headscale 配置使用。
func (a *App) settingsTLS(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	// 证书支持文件上传，表单是 multipart：先解析（CSRF 令牌也在 multipart 字段里），
	// 再做权限与令牌校验。请求体大小同时受限，防止超大上传把内存打满。
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			p := a.hs.loadConfigPage(lang)
			p.TLSMode = strings.TrimSpace(r.FormValue("tls_mode"))
			p.TLSInputMode = strings.TrimSpace(r.FormValue("tls_input_mode"))
			p.Errors = map[string]string{"cert_pem": tr(lang, "cfg.err.uploadTooLarge")}
			p.Warnings = configWarnings(p, lang)
			a.renderSettings(w, r, p)
			return
		}
	}
	if !a.mutate(w, r) {
		return
	}
	_ = r.ParseForm()
	p := a.hs.loadConfigPage(lang)
	p.TLSMode = strings.TrimSpace(r.FormValue("tls_mode"))
	p.TLSInputMode = strings.TrimSpace(r.FormValue("tls_input_mode"))
	if p.TLSInputMode == "" {
		p.TLSInputMode = "upload"
	}
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
	// 选了文件就以文件内容为准（上传优先于粘贴）。
	if up, field, code := readUploadedPair(r); code != "" {
		p.Errors = map[string]string{field: tr(lang, code)}
		p.Warnings = configWarnings(p, lang)
		a.renderSettings(w, r, p)
		return
	} else if up.cert != "" {
		p.CertPEM, p.KeyPEM = up.cert, up.key
	}
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
		a.renderSettings(w, r, p)
		return
	}

	// 只有手动方式且确实粘贴了内容时才落盘，避免空提交覆盖已有证书。
	rollback := func() {}
	if p.TLSMode == "manual" && p.CertPEM != "" {
		certPath, keyPath, undo, err := saveManualCert(p.CertPEM, p.KeyPEM)
		if err != nil {
			p.Errors = map[string]string{"cert_pem": tr(lang, "cfg.err.certSave", err.Error())}
			p.Warnings = configWarnings(p, lang)
			a.renderSettings(w, r, p)
			return
		}
		p.CertPath, p.KeyPath, rollback = certPath, keyPath, undo
	}

	next, err := a.hs.buildConfig(p, "tls")
	if err != nil {
		rollback()
		p.Errors = map[string]string{"tls_mode": err.Error()}
		p.Warnings = configWarnings(p, lang)
		a.renderSettings(w, r, p)
		return
	}
	a.applySettings(w, r, p, next, "cfg.msg.tlsSaved", rollback)
}

// maxUploadBytes 限制证书上传请求体大小：一条完整链通常几十 KB，8 MiB 已经宽松。
const maxUploadBytes = 8 << 20

// maxUploadFileBytes 限制单个上传文件的大小。
const maxUploadFileBytes = 2 << 20

// uploadPair 是一次证书上传解析出来的内容（证书链已按签名关系排好）。
type uploadPair struct {
	cert string
	key  string
}

// readUploadedPair 读取上传的证书文件：公钥证书（叶子）、中间证书、私钥。
// 返回出错字段与 i18n 键；两者都空表示没有上传任何文件（应回落到粘贴的文本）。
func readUploadedPair(r *http.Request) (uploadPair, string, string) {
	if r.MultipartForm == nil {
		return uploadPair{}, "", ""
	}
	certRaw, hasCert, err := readUploadFile(r, "tls_cert_file")
	if err != nil {
		return uploadPair{}, "cert_pem", "cfg.err.uploadTooLarge"
	}
	keyRaw, hasKey, err := readUploadFile(r, "tls_key_file")
	if err != nil {
		return uploadPair{}, "key_pem", "cfg.err.uploadTooLarge"
	}
	chainRaw, _, err := readUploadFile(r, "tls_chain_file")
	if err != nil {
		return uploadPair{}, "cert_pem", "cfg.err.uploadTooLarge"
	}
	if !hasCert && !hasKey {
		return uploadPair{}, "", ""
	}
	if !hasCert {
		return uploadPair{}, "cert_pem", "cfg.err.certFileEmpty"
	}
	if !hasKey {
		return uploadPair{}, "key_pem", "cfg.err.keyFileEmpty"
	}
	certPEM, err := buildChainPEM(certRaw, chainRaw)
	if err != nil {
		return uploadPair{}, "cert_pem", "cfg.err.certFileInvalid"
	}
	return uploadPair{cert: certPEM, key: keyRaw}, "", ""
}

// readUploadFile 读取一个上传文件的文本内容。第二个返回值表示是否真的选了文件。
func readUploadFile(r *http.Request, field string) (string, bool, error) {
	if r.MultipartForm == nil {
		return "", false, nil
	}
	fhs := r.MultipartForm.File[field]
	if len(fhs) == 0 {
		return "", false, nil
	}
	fh := fhs[0]
	if strings.TrimSpace(fh.Filename) == "" {
		return "", false, nil
	}
	if fh.Size > maxUploadFileBytes {
		return "", true, errors.New("file too large")
	}
	f, err := fh.Open()
	if err != nil {
		return "", true, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxUploadFileBytes+1))
	if err != nil {
		return "", true, err
	}
	if int64(len(b)) > maxUploadFileBytes {
		return "", true, errors.New("file too large")
	}
	return normalizePEMText(string(b)), true, nil
}

// normalizePEMText 统一行尾、去掉 BOM 与首尾空白：上传的文件可能是 CRLF。
func normalizePEMText(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s)
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
		a.renderSettings(w, r, fresh)
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
// settingsAdminTLS 配置管理界面自身的 HTTPS：启用/关闭、证书来源（复用 headscale 手动证书或粘贴 PEM）。
// 保存后写入 admin-tls.json 并重启管理界面进程生效；启用后网页访问必须走 HTTPS。
func (a *App) settingsAdminTLS(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	lang := langFrom(r)
	fail := func(key string, args ...any) {
		p := a.loadSettingsPage(lang, r.TLS != nil)
		p.Errors = map[string]string{"admin_tls": tr(lang, key, args...)}
		a.renderSettings(w, r, p)
	}

	cfg := loadAdminTLS(a.dataFile)
	cfg.Enabled = r.FormValue("enabled") == "on"

	if cfg.Enabled {
		switch r.FormValue("source") {
		case "reuse":
			// 复用 headscale 手动模式的证书文件。
			content, err := a.hs.ReadConfigText()
			if err != nil {
				fail("msg.saveFail", err.Error())
				return
			}
			cfg.CertPath = topValue(content, "tls_cert_path")
			cfg.KeyPath = topValue(content, "tls_key_path")
			if cfg.CertPath == "" || cfg.KeyPath == "" {
				fail("adminTLS.err.noHeadscaleCert")
				return
			}
			if err := cfg.validPair(); err != nil {
				fail("adminTLS.err.reuseInvalid", err.Error())
				return
			}
		default: // paste
			certPEM := strings.TrimSpace(r.FormValue("admin_cert_pem"))
			keyPEM := strings.TrimSpace(r.FormValue("admin_key_pem"))
			if certPEM == "" && keyPEM == "" && cfg.CertPath != "" && cfg.KeyPath != "" {
				// 两项留空 = 沿用已有证书，但配对必须仍然有效。
				if err := cfg.validPair(); err != nil {
					fail("adminTLS.err.keepInvalid", err.Error())
					return
				}
			} else {
				certPath, keyPath, err := writeAdminCerts(a.dataFile, certPEM, keyPEM)
				if err != nil {
					fail("adminTLS.err.pemInvalid", err.Error())
					return
				}
				cfg.CertPath, cfg.KeyPath = certPath, keyPath
			}
		}
	}

	if err := saveAdminTLS(a.dataFile, cfg); err != nil {
		fail("msg.saveFail", err.Error())
		return
	}
	if err := restartSelf(); err != nil {
		a.redirectWithFlash(w, r, "/settings", tr(lang, "adminTLS.savedManualRestart"))
		return
	}
	if cfg.Enabled {
		a.redirectWithFlash(w, r, "/settings", tr(lang, "adminTLS.savedOn", r.Host))
	} else {
		a.redirectWithFlash(w, r, "/settings", tr(lang, "adminTLS.savedOff", r.Host))
	}
}

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
