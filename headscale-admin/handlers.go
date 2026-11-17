package main

import (
	"encoding/json"
	"net/http"
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
	Preauth []map[string]any
	APIKeys []map[string]any
	Users   []HSUser
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
	Service    string
	Version    string
	Cert       string
	Logs       string
	ConfigPath string
	PolicyPath string
}

type adminsData struct {
	Admins []*Admin
}

func (a *App) nodesPage(w http.ResponseWriter, r *http.Request) {
	d := nodesData{Nodes: []map[string]any{}, Routes: []map[string]any{}}
	if v, err := a.hs.Nodes(); err == nil {
		d.Nodes = v
	} else {
		a.sessions.SetFlash(sessionFrom(r).Token, "读取节点失败："+err.Error())
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
		a.sessions.SetFlash(sessionFrom(r).Token, "读取用户失败："+err.Error())
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
	a.render(w, r, "keys", d)
}

func (a *App) networkPage(w http.ResponseWriter, r *http.Request) {
	d := networkData{}
	if v, err := a.hs.Policy(); err == nil {
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
	d.PolicyPath = a.hs.PolicyPath()
	a.render(w, r, "network", d)
}

func (a *App) opsPage(w http.ResponseWriter, r *http.Request) {
	d := opsData{Service: "未知", Version: a.hs.Version(), Cert: certExpiry(), Logs: journal(80), ConfigPath: a.hs.Config, PolicyPath: a.hs.PolicyPath()}
	if out, err := systemctl("is-active", "headscale"); err == nil {
		d.Service = strings.TrimSpace(out)
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
		a.redirectWithFlash(w, r, "/users", "用户名不能为空")
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
		a.redirectWithFlash(w, r, "/users", "新用户名不能为空")
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
		a.redirectWithFlash(w, r, "/keys", "请选择用户")
		return
	}
	exp := strings.TrimSpace(r.FormValue("expiration"))
	if exp == "" {
		exp = "24h"
	}
	out, err := a.hs.CreatePreauthKey(userID, exp, r.FormValue("reusable") == "on", r.FormValue("ephemeral") == "on", strings.TrimSpace(r.FormValue("tags")))
	if err != nil {
		a.redirectWithFlash(w, r, "/keys", "生成失败："+err.Error())
		return
	}
	a.redirectWithFlash(w, r, "/keys", "预授权密钥（仅显示一次）："+extractKey(out))
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
	exp := strings.TrimSpace(r.FormValue("expiration"))
	if exp == "" {
		exp = "90d"
	}
	out, err := a.hs.CreateAPIKey(exp)
	if err != nil {
		a.redirectWithFlash(w, r, "/keys", "生成失败："+err.Error())
		return
	}
	a.redirectWithFlash(w, r, "/keys", "API key（仅显示一次）："+extractKey(out))
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
		a.redirectWithFlash(w, r, "/network", "ACL 策略不能为空")
		return
	}
	if err := a.hs.SavePolicy(content); err != nil {
		a.redirectWithFlash(w, r, "/network", "保存失败："+err.Error())
		return
	}
	if out, err := a.hs.Action("configtest", "-c", a.hs.Config); err != nil {
		a.redirectWithFlash(w, r, "/network", "已保存，但配置校验失败："+err.Error()+" "+out)
		return
	}
	a.redirectWithFlash(w, r, "/network", "ACL 已保存并通过配置校验（原文件已备份）")
}

func (a *App) saveConfig(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	content := r.FormValue("config")
	if strings.TrimSpace(content) == "" {
		a.redirectWithFlash(w, r, "/network", "配置不能为空")
		return
	}
	if err := a.hs.SaveConfig(content); err != nil {
		a.redirectWithFlash(w, r, "/network", "保存失败："+err.Error())
		return
	}
	if out, err := a.hs.Action("configtest", "-c", a.hs.Config); err != nil {
		a.redirectWithFlash(w, r, "/network", "已保存，但配置校验失败："+err.Error()+" "+out)
		return
	}
	a.redirectWithFlash(w, r, "/network", "配置已保存并通过校验（原文件已备份），重启服务后生效")
}

func (a *App) saveDNS(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	base := strings.TrimSpace(r.FormValue("base_domain"))
	if base == "" {
		a.redirectWithFlash(w, r, "/network", "base_domain 不能为空")
		return
	}
	magic := "false"
	if r.FormValue("magic_dns") == "on" {
		magic = "true"
	}
	if err := a.hs.UpdateNested("dns", "base_domain", base); err != nil {
		a.redirectWithFlash(w, r, "/network", "保存失败："+err.Error())
		return
	}
	if err := a.hs.UpdateNested("dns", "magic_dns", magic); err != nil {
		a.redirectWithFlash(w, r, "/network", "保存失败："+err.Error())
		return
	}
	a.redirectWithFlash(w, r, "/network", "DNS 设置已保存，重启服务后生效")
}

func (a *App) saveDERP(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	enabled := "false"
	if r.FormValue("enabled") == "on" {
		enabled = "true"
	}
	if err := a.hs.UpdateDERP("enabled", enabled); err != nil {
		a.redirectWithFlash(w, r, "/network", "保存失败："+err.Error())
		return
	}
	a.redirectWithFlash(w, r, "/network", "DERP 开关已保存，重启服务后生效")
}

func (a *App) restartHeadscale(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	out, err := systemctl("restart", "headscale")
	if err != nil {
		a.redirectWithFlash(w, r, "/ops", "重启失败："+err.Error()+" "+out)
		return
	}
	a.redirectWithFlash(w, r, "/ops", "headscale 服务已重启")
}

func (a *App) adminCreate(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	user := strings.TrimSpace(r.FormValue("username"))
	pass := r.FormValue("password")
	role := r.FormValue("role")
	if user == "" || pass == "" {
		a.redirectWithFlash(w, r, "/admins", "用户名和密码不能为空")
		return
	}
	if err := a.store.CreateAdmin(user, pass, role); err != nil {
		a.redirectWithFlash(w, r, "/admins", "创建失败："+err.Error())
		return
	}
	a.redirectWithFlash(w, r, "/admins", "管理员 "+user+" 已创建")
}

func (a *App) adminDelete(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	user := strings.TrimSpace(r.FormValue("username"))
	if err := a.store.DeleteAdmin(user); err != nil {
		a.redirectWithFlash(w, r, "/admins", "删除失败："+err.Error())
		return
	}
	a.redirectWithFlash(w, r, "/admins", "管理员 "+user+" 已删除")
}

func (a *App) adminRole(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	user := strings.TrimSpace(r.FormValue("username"))
	if err := a.store.SetRole(user, r.FormValue("role")); err != nil {
		a.redirectWithFlash(w, r, "/admins", "修改失败："+err.Error())
		return
	}
	a.redirectWithFlash(w, r, "/admins", "角色已更新")
}

func (a *App) passwordChange(w http.ResponseWriter, r *http.Request) {
	if !a.mutate(w, r) {
		return
	}
	s := sessionFrom(r)
	if _, ok := a.store.Verify(s.Username, r.FormValue("old_password")); !ok {
		a.redirectWithFlash(w, r, "/admins", "原密码错误")
		return
	}
	newPass := r.FormValue("new_password")
	if len(newPass) < 8 {
		a.redirectWithFlash(w, r, "/admins", "新密码至少 8 位")
		return
	}
	if err := a.store.ChangePassword(s.Username, newPass); err != nil {
		a.redirectWithFlash(w, r, "/admins", "修改失败："+err.Error())
		return
	}
	a.redirectWithFlash(w, r, "/admins", "密码已修改")
}
