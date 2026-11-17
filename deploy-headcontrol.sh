#!/usr/bin/env bash
#
# HeadControl 一键部署脚本（Headscale 的轻量 Web 管理界面，Go + HTMX 单二进制）
# ------------------------------------------------------------------
# 适用系统 : AlmaLinux / CentOS / RHEL / Rocky（yum/dnf）+ Ubuntu/Debian（apt）
# 前置条件 : 已部署 headscale（本项目的 deploy-headscale.sh），且能在本机访问其 API
# 版本来源 : https://github.com/ahmadzip/HeadControl （社区项目，非 headscale 官方）
#
# 功能:
#   装 Go/GCC -> clone 编译 -> 建 systemd -> 配置 Basic Auth -> 创建 headscale API key ->
#   POST 完成初始配置 -> 防火墙放行访问端口（默认对所有来源开放）
#
# 访问方式:
#   - 默认直接开放：浏览器访问 http://<服务器IP>:3000，会弹出用户名/密码登录框
#   - 如需限制来源：--allow-cidr 192.168.100.0/24
#
# 安全说明:
#   - 默认启用 HTTP Basic Auth；用户名 admin，密码未指定时自动生成并打印一次
#   - 密码以带盐 SHA-256 摘要写入 /etc/headcontrol/auth.env (0600)，明文不进 systemd unit
#   - HTTP 下 Basic Auth 凭据是 Base64 明文传输；公网使用请配合 HTTPS 或至少用 --allow-cidr
#   - API key 只显示一次，保存到 /root/.headcontrol-apikey (0600)
#
# 汉化:
#   默认中文界面。需同目录存在 headcontrol-zh.patch（汉化补丁）。
#   --no-zh 可关闭汉化，使用原版英文。
#   本仓库不分发 HeadControl 源码；脚本按固定上游提交拉取源码后应用补丁。
#
# 用法:
#   sudo bash deploy-headcontrol.sh [--port 3000] [--hs-url http://127.0.0.1:18080] \
#        [--auth-user admin] [--auth-pass '密码'] [--no-auth] \
#        [--allow-all] [--allow-cidr 192.168.100.0/24] [--no-firewall] [--no-zh]
#
set -euo pipefail

# ===================== 变量 =====================
PORT="3000"
HS_URL=""
ALLOW_CIDR=""          # 限制访问 Web UI 的网段；指定后仅该网段可访问
ALLOW_ALL="yes"        # 默认对所有来源开放 TCP/PORT；--allow-cidr 会自动关闭它
NO_FIREWALL="no"
USE_ZH="yes"           # 默认应用汉化补丁；--no-zh 关闭。补丁与脚本同目录
AUTH_USER="admin"      # Web UI Basic Auth 用户名
AUTH_PASS=""           # 为空时交互输入或自动生成
USE_AUTH="yes"         # 默认启用 Basic Auth；--no-auth 关闭（不推荐）
AUTO_PASS="no"
AUTH_ENV_FILE="/etc/headcontrol/auth.env"
AUTH_CRED_FILE="/root/.headcontrol-auth"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ZH_PATCH="${SCRIPT_DIR}/headcontrol-zh.patch"
HC_REPO="https://github.com/ahmadzip/HeadControl.git"
HC_COMMIT="cfdfc63c766af3658851ef766bd84ab678fe005a"
RUN_DIR="/opt/headcontrol"
SRC_DIR="/opt/headcontrol-src"
DB_DIR="/var/lib/headcontrol"
BIN="/usr/local/bin/headcontrol"
APIKEY_FILE="/root/.headcontrol-apikey"

log()   { printf '%s[INFO]%s %s\n'  "$C_GREEN"  "$C_RESET" "$*"; }
warn()  { printf '%s[WARN]%s %s\n'  "$C_YELLOW" "$C_RESET" "$*"; }
error() { printf '%s[ERROR]%s %s\n' "$C_RED"    "$C_RESET" "$*" >&2; exit 1; }
step()  { printf '\n%s==> %s%s\n'   "$C_BLUE"   "$C_RESET" "$*"; }
C_RESET=$'\033[0m'; C_GREEN=$'\033[1;32m'; C_YELLOW=$'\033[1;33m'; C_RED=$'\033[1;31m'; C_BLUE=$'\033[1;34m'

# ===================== 参数 =====================
while [[ $# -gt 0 ]]; do
  case "$1" in
    --port)         PORT="${2:-}"; shift 2 ;;
    --hs-url)       HS_URL="${2:-}"; shift 2 ;;
    --allow-all)    ALLOW_ALL="yes"; ALLOW_CIDR=""; shift ;;
    --allow-cidr)   ALLOW_CIDR="${2:-}"; ALLOW_ALL="no"; shift 2 ;;
    --no-firewall)  NO_FIREWALL="yes"; shift ;;
    --no-zh)        USE_ZH="no"; shift ;;
    --auth-user)    AUTH_USER="${2:-}"; shift 2 ;;
    --auth-pass)    AUTH_PASS="${2:-}"; shift 2 ;;
    --no-auth)      USE_AUTH="no"; shift ;;
    -h|--help)      sed -n '2,/^set -euo pipefail$/p' "$0" | sed '$d'; exit 0 ;;
    *)              error "未知参数: $1" ;;
  esac
done

[[ $EUID -eq 0 ]] || error "请以 root 运行（sudo bash $0）"
command -v headscale >/dev/null 2>&1 || error "未找到 headscale 命令，请先部署 headscale"
command -v curl >/dev/null 2>&1 || error "缺少 curl"
command -v git >/dev/null 2>&1 || error "缺少 git"
command -v openssl >/dev/null 2>&1 || error "缺少 openssl（用于生成密码摘要）"

# ===================== 认证凭据 =====================
if [[ "$USE_AUTH" == "yes" ]]; then
  [[ "$AUTH_USER" =~ ^[A-Za-z0-9_.-]+$ ]] || error "用户名只能包含字母、数字、下划线、点和连字符"
  if [[ -z "$AUTH_PASS" ]]; then
    if [[ -t 0 ]]; then
      read -rsp "  设置管理密码: " AUTH_PASS; echo
      [[ -n "$AUTH_PASS" ]] || error "密码不能为空"
      read -rsp "  确认管理密码: " AUTH_PASS_CONFIRM; echo
      [[ "$AUTH_PASS" == "$AUTH_PASS_CONFIRM" ]] || error "两次输入的密码不一致"
    else
      AUTH_PASS="$(openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 20)"
      AUTO_PASS="yes"
      log "未指定密码且无终端输入，已自动生成随机密码"
    fi
  fi
  AUTH_SALT="$(openssl rand -hex 16)"
  AUTH_HASH="$(printf '%s' "${AUTH_SALT}${AUTH_PASS}" | openssl dgst -sha256 -r | awk '{print $1}')"
  [[ "$AUTH_HASH" =~ ^[0-9a-f]{64}$ ]] || error "密码摘要生成失败"
fi

# 自动探测 headscale 的 server_url 作为 HS_URL
if [[ -z "$HS_URL" ]]; then
  CONF="/etc/headscale/config.yaml"
  [[ -r "$CONF" ]] && HS_URL="$(grep -E '^server_url:' "$CONF" | head -1 | sed -E 's/^server_url:[[:space:]]*//; s/^"|"$//g')"
  HS_URL="${HS_URL:-http://127.0.0.1:8080}"
fi
# 本机访问优先用 127.0.0.1（避免公网回环/证书问题）：提取端口
HS_PORT="$(printf '%s' "$HS_URL" | sed -nE 's#^[a-z]+://[^/]*:([0-9]+).*#\1#p')"
LOCAL_HS_URL="http://127.0.0.1:${HS_PORT:-8080}"

# 初始化请求也要带 Basic Auth，否则会被 401 拦下
CURL_AUTH=()
if [[ "$USE_AUTH" == "yes" ]]; then
  CURL_AUTH=(-u "${AUTH_USER}:${AUTH_PASS}")
fi

# ===================== 1. 编译依赖 =====================
step "安装编译依赖（Go + GCC，go-sqlite3 需 CGO）"
if command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1; then
  (dnf install -y golang gcc 2>/dev/null || yum install -y golang gcc) >/dev/null 2>&1 || error "安装 Go/GCC 失败"
elif command -v apt >/dev/null 2>&1; then
  apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq golang gcc >/dev/null 2>&1 || error "安装 Go/GCC 失败"
fi
go version && gcc --version | head -1

# ===================== 2. clone & 编译 =====================
step "下载并编译 HeadControl"
# 固定上游提交，避免 main 分支变动导致补丁失效
rm -rf "$SRC_DIR"
mkdir -p "$SRC_DIR"
git init -q "$SRC_DIR"
git -C "$SRC_DIR" remote add origin "$HC_REPO"
git -C "$SRC_DIR" fetch -q --depth 1 origin "$HC_COMMIT" || error "拉取 HeadControl 源码失败（提交 ${HC_COMMIT}）"
git -C "$SRC_DIR" checkout -q FETCH_HEAD
cd "$SRC_DIR"

# 应用汉化补丁（在编译前覆盖源码与模板，使二进制和运行资源都为中文）
if [[ "$USE_ZH" == "yes" && -f "$ZH_PATCH" ]]; then
  git apply --check "$ZH_PATCH" || error "汉化补丁校验失败（上游提交可能已变动）"
  git apply "$ZH_PATCH" || error "汉化补丁应用失败"
  log "已应用汉化补丁: $ZH_PATCH"
elif [[ "$USE_ZH" == "yes" ]]; then
  warn "未找到汉化补丁 $ZH_PATCH，将使用原版英文界面（加 --no-zh 可消除此提示）"
fi

go mod tidy >/dev/null 2>&1 || error "go mod tidy 失败"
# 先编译到临时文件，再停服替换，避免运行中二进制报 Text file busy；编译失败时旧服务不受影响
NEW_BIN="${BIN}.new"
CGO_ENABLED=1 go build -o "$NEW_BIN" . >/dev/null 2>&1 || error "编译失败"
systemctl stop headcontrol >/dev/null 2>&1 || true
install -m 0755 "$NEW_BIN" "$BIN"
rm -f "$NEW_BIN"

# 坑1：新编译二进制在 SELinux enforcing 下带 user_tmp_t，systemd 无法执行 -> 修正上下文
if command -v restorecon >/dev/null 2>&1 && [[ "$(getenforce 2>/dev/null)" == "Enforcing" ]]; then
  restorecon -v "$BIN" >/dev/null 2>&1 || true
  log "已修正二进制 SELinux 上下文"
fi

# 坑2：程序运行时依赖相对路径的 templates/ 和 static/，必须与工作目录一致
mkdir -p "$RUN_DIR" "$DB_DIR"
cp -r "$SRC_DIR/templates" "$SRC_DIR/static" "$RUN_DIR/"
command -v restorecon >/dev/null 2>&1 && restorecon -RF "$RUN_DIR" >/dev/null 2>&1 || true
log "二进制与资源就绪: $BIN, $RUN_DIR"

# ===================== 3. 创建 API key =====================
step "创建 headscale API key（90 天）"
RAW="$(headscale apikeys create --expiration 90d 2>/dev/null | sed -E $'s/\033\\[[0-9;]*[a-zA-Z]//g' || true)"
APIKEY="$(printf '%s' "$RAW" | grep -E '^hskey-api-' | head -1)"
[[ -n "$APIKEY" ]] || error "API key 创建失败"
printf '%s\n' "$APIKEY" > "$APIKEY_FILE"
chmod 600 "$APIKEY_FILE"
log "API key 已保存到 ${APIKEY_FILE}（仅显示一次，请妥善保管）"

# ===================== 4. systemd 服务 =====================
step "创建 systemd 服务（端口 ${PORT}）"
ENV_LINE=""
if [[ "$USE_AUTH" == "yes" ]]; then
  mkdir -p "$(dirname "$AUTH_ENV_FILE")"
  cat >"$AUTH_ENV_FILE" <<EOF
HEADCONTROL_AUTH_USER=${AUTH_USER}
HEADCONTROL_AUTH_SALT=${AUTH_SALT}
HEADCONTROL_AUTH_HASH=${AUTH_HASH}
EOF
  chmod 600 "$AUTH_ENV_FILE"
  ENV_LINE="EnvironmentFile=${AUTH_ENV_FILE}"
  log "Basic Auth 已启用（用户 ${AUTH_USER}，摘要存于 ${AUTH_ENV_FILE}）"
else
  rm -f "$AUTH_ENV_FILE"
  warn "已通过 --no-auth 关闭密码保护，任何能访问 ${PORT} 端口的人都可以管理 headscale"
fi
cat >/etc/systemd/system/headcontrol.service <<EOF
[Unit]
Description=HeadControl - Minimal Headscale admin dashboard
After=network-online.target headscale.service
Wants=network-online.target

[Service]
${ENV_LINE}
Type=simple
ExecStart=${BIN} -port ${PORT} -db ${DB_DIR}/headcontrol.db
Restart=on-failure
RestartSec=3
User=root
WorkingDirectory=${RUN_DIR}

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now headcontrol >/dev/null 2>&1 || true
sleep 2
systemctl is-active --quiet headcontrol || error "HeadControl 启动失败，请查看 journalctl -u headcontrol"
log "服务已启动并自启"

# ===================== 5. 自动完成初始配置 =====================
step "初始化 Web UI 配置（连接 headscale）"
curl "${CURL_AUTH[@]}" -s -o /dev/null -w "" "http://127.0.0.1:${PORT}/setup" || true
RESP="$(curl "${CURL_AUTH[@]}" -s -X POST "http://127.0.0.1:${PORT}/api/save-settings" \
  --data-urlencode "base_url=${LOCAL_HS_URL}" \
  --data-urlencode "api_key=${APIKEY}")"
# 验证：首页是否为总览（配置成功会跳过 setup 页）；兼容汉化前后的关键字
if curl "${CURL_AUTH[@]}" -s "http://127.0.0.1:${PORT}/" | grep -qE '总览|用户总数|Dashboard'; then
  log "初始配置完成，Web UI 已连上 headscale (${LOCAL_HS_URL})"
else
  warn "初始配置可能未成功，请浏览器访问 http://<服务器>:${PORT}/setup 手动填写 API key"
  printf '  base_url : %s\n  api_key  : %s\n' "$LOCAL_HS_URL" "$APIKEY"
fi

# ===================== 6. 防火墙（默认对所有来源开放 TCP/PORT）=====================
step "配置防火墙"
if [[ "$NO_FIREWALL" != "yes" ]] && command -v firewall-cmd >/dev/null 2>&1 && systemctl is-active --quiet firewalld; then
  if [[ -n "$ALLOW_CIDR" ]]; then
    firewall-cmd --permanent --add-rich-rule="rule family=ipv4 source address=${ALLOW_CIDR} port port=${PORT} protocol=tcp accept" >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
    log "firewalld 已仅允许 ${ALLOW_CIDR} 访问 TCP/${PORT}"
  else
    firewall-cmd --permanent --add-port="${PORT}/tcp" >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
    log "firewalld 已对所有来源开放 TCP/${PORT}（--allow-all，默认）"
  fi
elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi active; then
  if [[ -n "$ALLOW_CIDR" ]]; then
    ufw allow from "$ALLOW_CIDR" to any port "$PORT" proto tcp >/dev/null 2>&1 || true
    log "ufw 已仅允许 ${ALLOW_CIDR} 访问 TCP/${PORT}"
  else
    ufw allow "${PORT}/tcp" >/dev/null 2>&1 || true
    log "ufw 已对所有来源开放 TCP/${PORT}（--allow-all，默认）"
  fi
else
  if [[ "$ALLOW_ALL" == "yes" ]]; then
    log "未检测到活动防火墙；${PORT} 对所有网卡监听，已可直接访问"
  else
    warn "未检测到活动防火墙；${PORT} 当前对所有网卡监听"
  fi
fi

# ===================== 7. 完成信息 =====================
if [[ "$USE_AUTH" == "yes" ]]; then
  cat >"$AUTH_CRED_FILE" <<EOF
用户名: ${AUTH_USER}
密码:   ${AUTH_PASS}
EOF
  chmod 600 "$AUTH_CRED_FILE"
  AUTH_DISPLAY="${AUTH_USER} / ${AUTH_PASS}"
  LOGIN_NOTE="弹出登录框后填写上面的凭据"
  AUTH_NOTE="  - 用户名/密码副本: ${AUTH_CRED_FILE} (0600)；摘要: ${AUTH_ENV_FILE} (0600)"
  if [[ "$AUTO_PASS" == "yes" ]]; then
    AUTH_NOTE="${AUTH_NOTE}
  - 密码为自动生成，请立即保存；忘记后可删除 ${AUTH_ENV_FILE} 并重跑脚本重设"
  fi
  HTTP_NOTE="  - HTTP 下 Basic Auth 凭据为 Base64 明文；公网使用请配合 HTTPS，或加 --allow-cidr 限制来源"
else
  AUTH_DISPLAY="未启用（--no-auth，不安全）"
  LOGIN_NOTE="当前无需登录"
  AUTH_NOTE="  - 当前无任何认证，任何能连到 TCP/${PORT} 的人都可管理 headscale"
  HTTP_NOTE=""
fi

cat <<EOF

============================================================
 HeadControl 部署完成
============================================================
服务状态     : $(systemctl is-active headcontrol)（开机自启）
headscale API: ${LOCAL_HS_URL}
API key 文件 : ${APIKEY_FILE} (0600)
管理用户名/密码: ${AUTH_DISPLAY}

浏览器访问（防火墙已放行 TCP/${PORT}）：
  浏览器访问   : http://<服务器IP>:${PORT}  （${LOGIN_NOTE}）
  本机地址     : http://127.0.0.1:${PORT}
  可用 ip route get 1 查本机对外网卡 IP：$(ip route get 1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}' | head -1)

注意：
  - HeadControl 是社区项目，非 headscale 官方维护
${AUTH_NOTE}
${HTTP_NOTE}
  - 如需收紧来源，重跑加 --allow-cidr 192.168.100.0/24；完全关闭放行用 --no-firewall
  - API key 泄露可吊销: headscale apikeys expire --id <ID>  （或 delete --id <ID> 彻底删除）
  - 查看所有 key: headscale apikeys list
============================================================
EOF
