package main

import (
	"fmt"
	"net/http"
)

// 词条按 [中文, English] 排列。key 用点分层，便于核对。
var dict = map[string][2]string{
	// 通用 / common
	"app.name":            {"Headscale Admin", "Headscale Admin"},
	"app.subtitle":        {"Headscale 管理控制台", "Headscale control panel"},
	"nav.manage":          {"管理", "MANAGE"},
	"nav.system":          {"系统", "SYSTEM"},
	"nav.dashboard":       {"总览", "Dashboard"},
	"nav.nodes":           {"节点", "Nodes"},
	"nav.users":           {"用户", "Users"},
	"nav.keys":            {"密钥与接入", "Keys & Access"},
	"nav.network":         {"网络与策略", "Network & Policy"},
	"nav.ops":             {"运维与监控", "Operations"},
	"nav.admins":          {"管理员", "Administrators"},
	"role.admin":          {"管理员", "Admin"},
	"role.readonly":       {"只读", "Read-only"},
	"action.create":       {"创建", "Create"},
	"action.rename":       {"重命名", "Rename"},
	"action.delete":       {"删除", "Delete"},
	"action.expire":       {"过期", "Expire"},
	"action.revoke":       {"失效", "Revoke"},
	"action.tag":          {"设标签", "Set tags"},
	"action.approve":      {"批准", "Approve"},
	"action.generate":     {"生成", "Generate"},
	"action.save":         {"保存", "Save"},
	"action.changeRole":   {"改角色", "Change role"},
	"action.changePass":   {"修改密码", "Change password"},
	"action.logout":       {"退出登录", "Sign out"},
	"action.restart":      {"重启 headscale", "Restart headscale"},
	"label.username":      {"用户名", "Username"},
	"label.password":      {"密码", "Password"},
	"label.displayName":   {"显示名", "Display name"},
	"label.email":         {"邮箱", "Email"},
	"label.avatar":        {"头像 URL", "Avatar URL"},
	"label.role":          {"角色", "Role"},
	"label.user":          {"用户", "User"},
	"label.expiry":        {"有效期", "Expiration"},
	"label.tags":          {"标签", "Tags"},
	"label.reusable":      {"可重复使用", "Reusable"},
	"label.ephemeral":     {"临时节点", "Ephemeral"},
	"label.baseDomain":    {"MagicDNS 根域", "MagicDNS base domain"},
	"label.magicDNS":      {"启用 MagicDNS", "Enable MagicDNS"},
	"label.derp":          {"启用内置 DERP", "Enable embedded DERP"},
	"label.oldPassword":   {"原密码", "Current password"},
	"label.newPassword":   {"新密码", "New password"},
	"label.name":          {"名称", "Name"},
	"label.count":         {"个", "total"},
	"col.id":              {"ID", "ID"},
	"col.name":            {"名称", "Name"},
	"col.user":            {"用户", "User"},
	"col.address":         {"地址", "Addresses"},
	"col.status":          {"状态", "Status"},
	"col.tagsRoutes":      {"标签/路由", "Tags / Routes"},
	"col.actions":         {"操作", "Actions"},
	"col.nodeID":          {"节点 ID", "Node ID"},
	"col.node":            {"节点", "Node"},
	"col.availableRoutes": {"可用路由", "Available routes"},
	"col.approvedRoutes":  {"已批准", "Approved"},
	"col.createdAt":       {"创建时间", "Created"},
	"col.key":             {"Key", "Key"},
	"col.prefix":          {"前缀", "Prefix"},
	"col.expires":         {"过期时间", "Expires"},
	"col.lastSeen":        {"最后在线", "Last seen"},
	"col.online":          {"在线", "Online"},
	"state.online":        {"在线", "Online"},
	"state.offline":       {"离线", "Offline"},
	"state.active":        {"运行中", "Active"},
	"state.unknown":       {"未知", "Unknown"},
	"state.true":          {"是", "Yes"},
	"state.false":         {"否", "No"},
	"field.service":       {"headscale 服务", "headscale service"},
	"field.version":       {"版本", "Version"},
	"field.cert":          {"证书到期", "Certificate expiry"},
	"field.configFile":    {"配置文件", "Config file"},
	"field.policyFile":    {"策略文件", "Policy file"},
	"title.theme":         {"切换主题", "Change theme"},
	"title.lang":          {"切换语言", "Change language"},
	"lang.zh":             {"中文", "Chinese"},
	"lang.en":             {"English", "English"},
	"theme.light":         {"浅色", "Light"},
	"theme.dark":          {"深色", "Dark"},
	"theme.cyberpunk":     {"赛博朋克", "Cyberpunk"},
	"theme.ocean":         {"海洋", "Ocean"},
	"theme.sunset":        {"日落", "Sunset"},
	"theme.forest":        {"森林", "Forest"},
	"theme.midnight":      {"午夜", "Midnight"},
	"theme.dracula":       {"德古拉", "Dracula"},
	"theme.nord":          {"北欧", "Nord"},
	"theme.rose":          {"玫瑰", "Rose"},
	"theme.coffee":        {"咖啡", "Coffee"},
	"theme.terminal":      {"终端", "Terminal"},
	"theme.pastel":        {"柔和", "Pastel"},
	"theme.synthwave":     {"合成波", "Synthwave"},

	// 登录 / login
	"login.title":  {"登录管理控制台", "Sign in to console"},
	"login.submit": {"登录", "Sign in"},
	"login.error":  {"用户名或密码错误", "Invalid username or password"},
	"login.hint":   {"会话 8 小时有效，Cookie 仅 HttpOnly。", "Sessions last 8 hours. Cookies are HttpOnly."},

	// 总览 / dashboard
	"title.dashboard":      {"总览", "Dashboard"},
	"stat.users":           {"用户总数", "Users"},
	"stat.nodes":           {"节点总数", "Nodes"},
	"stat.online":          {"在线节点", "Online nodes"},
	"stat.routes":          {"可用路由", "Routes"},
	"stat.preauth":         {"预授权密钥", "Pre-auth keys"},
	"stat.apiKeys":         {"API key", "API keys"},
	"section.service":      {"服务状态", "Service status"},
	"section.recent":       {"最近节点", "Recent nodes"},
	"empty.recent":         {"暂无节点接入。可在「密钥与接入」生成预授权密钥。", "No nodes yet. Generate a pre-auth key under Keys & Access."},
	"hint.recentCount":     {"显示最近 %d 个节点", "Showing the latest %d nodes"},

	// 节点 / nodes
	"title.nodes":          {"节点管理", "Nodes"},
	"section.nodeList":     {"节点列表", "Node list"},
	"section.routeReview":  {"路由审批", "Route approval"},
	"label.nodeTags":       {"标签", "Tags"},
	"label.approved":       {"已批路由", "Approved routes"},
	"label.available":      {"可用路由", "Available routes"},
	"empty.nodes":          {"暂无节点。生成预授权密钥后用 Tailscale 客户端接入。", "No nodes yet. Generate a pre-auth key and join with a Tailscale client."},
	"empty.routes":         {"没有节点宣告路由。", "No nodes are advertising routes."},
	"confirm.renameNode":   {"重命名节点？", "Rename this node?"},
	"confirm.tagNode":      {"覆盖节点标签？", "Overwrite node tags?"},
	"confirm.expireNode":   {"立即登出该节点？", "Expire this node immediately?"},
	"confirm.deleteNode":   {"确认删除该节点？不可恢复。", "Delete this node? This cannot be undone."},
	"confirm.approveRoute": {"更新已批准路由？", "Update approved routes?"},

	// 用户 / users
	"title.users":         {"用户管理", "Users"},
	"section.newUser":     {"新建用户", "New user"},
	"section.userList":    {"用户列表", "User list"},
	"empty.users":         {"暂无用户。", "No users yet."},
	"confirm.renameUser":  {"重命名用户？", "Rename this user?"},
	"confirm.deleteUser":  {"删除用户 %s？该用户下的节点需要重新分配。", "Delete user %s? Its nodes must be reassigned."},

	// 密钥 / keys
	"title.keys":          {"密钥与接入", "Keys & Access"},
	"section.genPreauth":  {"生成预授权密钥", "Generate pre-auth key"},
	"section.preauth":     {"预授权密钥", "Pre-auth keys"},
	"section.genAPI":      {"生成 API key", "Generate API key"},
	"section.apiKeys":     {"API keys", "API keys"},
	"hint.join":           {"客户端接入：", "Client join command:"},
	"empty.preauth":       {"暂无预授权密钥。", "No pre-auth keys yet."},
	"empty.apiKeys":       {"暂无 API key。", "No API keys yet."},
	"confirm.expireKey":   {"立即使密钥失效？", "Revoke this key immediately?"},
	"confirm.deleteKey":   {"确认删除密钥？", "Delete this key?"},
	"confirm.expireAPI":   {"立即使该 API key 失效？", "Revoke this API key immediately?"},
	"confirm.deleteAPI":   {"确认删除该 API key？", "Delete this API key?"},

	// 网络与策略 / network
	"title.network":       {"网络与策略", "Network & Policy"},
	"section.acl":         {"ACL 策略", "ACL policy"},
	"section.dnsDerp":     {"DNS 与 DERP", "DNS & DERP"},
	"section.fullConfig":  {"完整配置", "Full configuration"},
	"action.saveValidate": {"保存并校验", "Save & validate"},
	"action.saveDNS":      {"保存 DNS", "Save DNS"},
	"action.saveDERP":     {"保存 DERP", "Save DERP"},
	"action.overwrite":    {"覆盖保存", "Overwrite & save"},
	"hint.serverURL":      {"当前 server_url：%s　监听：%s。配置修改后需重启服务。", "Current server_url: %s  Listen: %s. Restart the service after changing config."},
	"confirm.saveACL":     {"保存 ACL 策略？原文件会自动备份。", "Save the ACL policy? The original file will be backed up."},
	"confirm.saveConfig":  {"确认覆盖完整配置？原文件会自动备份，保存后会先做 configtest 校验。", "Overwrite the full config? A backup is created and configtest runs before activation."},

	// 运维 / operations
	"title.ops":           {"运维与监控", "Operations"},
	"section.status":      {"运行状态", "Status"},
	"section.logs":        {"最近日志", "Recent logs"},
	"section.netmon":      {"实时网速", "Live network speed"},
	"net.download":        {"下行", "Download"},
	"net.upload":          {"上行", "Upload"},
	"net.iface":           {"网卡", "Interface"},
	"net.totalDown":       {"累计接收", "Total received"},
	"net.totalUp":         {"累计发送", "Total sent"},
	"net.unavailable":     {"无法读取网卡流量（仅支持 Linux /proc/net/dev）", "Interface counters unavailable (requires Linux /proc/net/dev)"},
	"net.updated":         {"更新于 %s，每 3 秒自动刷新", "Updated at %s, refreshes every 3s"},
	"net.sparkDown":       {"下行速率曲线", "Download rate history"},
	"net.sparkUp":         {"上行速率曲线", "Upload rate history"},
	"confirm.restart":     {"确认重启 headscale 服务？现有连接会短暂中断。", "Restart headscale? Existing connections will briefly drop."},

	// 管理员 / administrators
	"title.admins":        {"管理员", "Administrators"},
	"section.adminList":   {"管理员列表", "Administrators"},
	"section.newAdmin":    {"新增管理员", "Add administrator"},
	"section.changePass":  {"修改我的密码", "Change my password"},
	"confirm.deleteAdmin": {"确认删除管理员 %s？", "Delete administrator %s?"},

	// 操作提示 / flash messages
	"msg.readNodesFail":    {"读取节点失败：%s", "Failed to read nodes: %s"},
	"msg.readUsersFail":    {"读取用户失败：%s", "Failed to read users: %s"},
	"msg.operationSuccess": {"操作成功", "Operation succeeded"},
	"msg.successWith":      {"操作成功：%s", "Success: %s"},
	"msg.operationFailed":  {"操作失败：%s", "Operation failed: %s"},
	"msg.usernameRequired": {"用户名不能为空", "Username is required"},
	"msg.newNameRequired":  {"新用户名不能为空", "New username is required"},
	"msg.selectUser":       {"请选择用户", "Select a user"},
	"msg.generateFail":     {"生成失败：%s", "Generation failed: %s"},
	"msg.preauthOnce":      {"预授权密钥（仅显示一次）：%s", "Pre-auth key (shown once): %s"},
	"msg.apiOnce":          {"API key（仅显示一次）：%s", "API key (shown once): %s"},
	"msg.aclRequired":      {"ACL 策略不能为空", "ACL policy cannot be empty"},
	"msg.configRequired":   {"配置不能为空", "Configuration cannot be empty"},
	"msg.saveFail":         {"保存失败：%s", "Save failed: %s"},
	"msg.savedInvalid":     {"已保存，但配置校验失败：%s %s", "Saved, but configtest failed: %s %s"},
	"msg.aclSaved":         {"ACL 已保存并通过配置校验（原文件已备份）", "ACL saved and validated (original backed up)"},
	"msg.configSaved":      {"配置已保存并通过校验（原文件已备份），重启服务后生效", "Config saved and validated (original backed up); restart to apply"},
	"msg.baseRequired":     {"base_domain 不能为空", "base_domain is required"},
	"msg.dnsSaved":         {"DNS 设置已保存，重启服务后生效", "DNS settings saved; restart to apply"},
	"msg.derpSaved":        {"DERP 开关已保存，重启服务后生效", "DERP setting saved; restart to apply"},
	"msg.restartFail":      {"重启失败：%s %s", "Restart failed: %s %s"},
	"msg.restarted":        {"headscale 服务已重启", "headscale service restarted"},
	"msg.adminRequired":    {"用户名和密码不能为空", "Username and password are required"},
	"msg.adminCreated":     {"管理员 %s 已创建", "Administrator %s created"},
	"msg.adminDeleted":     {"管理员 %s 已删除", "Administrator %s deleted"},
	"msg.createFail":       {"创建失败：%s", "Create failed: %s"},
	"msg.deleteFail":       {"删除失败：%s", "Delete failed: %s"},
	"msg.roleUpdated":      {"角色已更新", "Role updated"},
	"msg.roleFail":         {"修改失败：%s", "Update failed: %s"},
	"msg.updateFail":       {"修改失败：%s", "Update failed: %s"},
	"msg.oldPassWrong":     {"原密码错误", "Current password is incorrect"},
	"msg.newPassShort":     {"新密码至少 8 位", "New password must be at least 8 characters"},
	"msg.passChanged":      {"密码已修改", "Password changed"},
	"msg.readonlyBlocked":  {"只读账号无权修改", "Read-only accounts cannot make changes"},
	"msg.csrfFailed":       {"CSRF 校验失败", "CSRF validation failed"},
	"msg.missingParam":     {"缺少参数 %s", "Missing parameter %s"},
	"msg.invalidRole":      {"无效角色", "Invalid role"},
	"msg.userMissing":      {"用户不存在", "User not found"},

	// 后端内部错误 / backend errors
	"auth.emptyCred":   {"用户名或密码为空", "Username or password is empty"},
	"auth.userExists":  {"用户已存在", "User already exists"},
	"auth.lastAdmin":   {"不能删除最后一个管理员", "Cannot delete the last administrator"},
	"auth.invalidRole": {"无效角色", "Invalid role"},
	"hs.noConfigItem":  {"未找到待修改的配置项", "Config item not found"},
	"hs.noConfigBlock": {"未找到配置块 %s", "Config block %s not found"},
	"hs.noDerpBlock":   {"未找到 derp 配置块", "DERP config block not found"},
	"hs.noPolicyPath":  {"未配置 policy.path", "policy.path is not configured"},
}

// reqLang 让后端方法可用变参接收界面语言，默认中文。
func reqLang(lang []string) string {
	if len(lang) > 0 {
		return lang[0]
	}
	return "zh"
}

// 中文为默认语言；英文只认 en / en-US。
func normalizeLang(v string) string {
	if len(v) >= 2 && (v[:2] == "en" || v[:2] == "EN") {
		return "en"
	}
	return "zh"
}

func langFrom(r *http.Request) string {
	if c, err := r.Cookie("hsa_lang"); err == nil {
		return normalizeLang(c.Value)
	}
	return "zh"
}

// tr 按请求语言取词条并格式化。
func tr(lang, key string, args ...any) string {
	v, ok := dict[key]
	if !ok {
		return key
	}
	s := v[0]
	if lang == "en" {
		s = v[1]
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}
