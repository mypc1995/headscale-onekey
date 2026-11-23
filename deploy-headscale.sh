#!/usr/bin/env bash
#
# Headscale 一键部署脚本（支持 HTTPS + 定时证书更新）
# ------------------------------------------------------------------
# 适用系统 : Ubuntu / Debian / CentOS / RHEL / Rocky / AlmaLinux / Fedora
# 架构     : x86_64(amd64) / aarch64(arm64)
# 目标版本 : v0.29.x（配置使用稳定版扁平键，与 main 开发版不同）
#
# 功能:
#   下载二进制 -> 系统用户/目录 -> 配置 -> systemd 服务 -> 防火墙 ->
#   HTTPS 证书 -> 定时续期 -> 启动 -> 默认用户与预授权密钥
#
# HTTPS 两种模式（--tls-mode）:
#   acme    : headscale 内置 ACME（autocert）自动向 Let's Encrypt 申请+续期
#             （剩余 30 天时续期，30-60 分钟随机检查，无需外部定时任务）
#   certbot : 用 certbot 申请证书，headscale 读取证书文件；
#             systemd timer 每天两次续期 + deploy hook 自动重启使新证书生效
#             （注意：v0.29.x 的 SIGHUP 只重载 ACL，不重载证书，故用 restart）
#   none    : 纯 HTTP 内网模式（默认，当 URL 以 http:// 开头时）
#
# 用法:
#   sudo bash deploy-headscale.sh                                  # 交互式
#   sudo bash deploy-headscale.sh -u http://1.2.3.4:8080           # 内网 HTTP
#   sudo bash deploy-headscale.sh -u https://hs.example.com        # 内置 ACME（默认）
#   sudo bash deploy-headscale.sh -u https://hs.example.com \
#        --tls-mode certbot -e admin@example.com --derp            # certbot+定时续期
#
#   部署后证书管理: headscale-cert {status|renew|reload}
#     reload 在 v0.29.x 仅重载 ACL 策略；证书更新请用 renew 或 restart 服务
#
# ------------------------------------------------------------------
set -euo pipefail
umask 077

# ===================== 配置变量 =====================
HS_VERSION="${HEADSCALE_VERSION:-0.29.7}"   # 不带 v 前缀（避免与 os-release 的 VERSION 冲突）
INSTALL_BIN="/usr/bin/headscale"
CONFIG_DIR="/etc/headscale"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
DATA_DIR="/var/lib/headscale"
TLS_CERT_DIR="${DATA_DIR}/tls"
SERVICE_FILE="/etc/systemd/system/headscale.service"
CERT_MANAGER="/usr/local/bin/headscale-cert"
DEFAULT_USER="default"

SERVER_URL=""
ACME_EMAIL=""
TLS_MODE=""          # acme | certbot | none，空则按 URL 自动判断
ENABLE_DERP="no"
ENABLE_TLS="no"
DOMAIN=""
BASE_DOMAIN=""       # MagicDNS 根域；v0.29.7 强制要求不能与 server_url 域名冲突
LISTEN_PORT=""       # HTTP 模式下从 -u URL 提取的监听端口（避免与已占用端口冲突）
PUBLIC_IP=""
CERTBOT_BIN=""
LOCAL_BINARY=""      # 本地已有的 headscale 二进制（离线部署用，不再从 GitHub 下载）
LOCAL_CONFIG=""      # 本地已有的配置示例（离线部署用）
PREFIX_V4=""         # 虚拟网段 prefixes.v4（默认用官方示例的 100.64.0.0/10）
PUBLIC_IP_OVERRIDE="" # 显式指定公网 IP（NAT 后面自动探测会拿到内网地址）

# ===================== 输出辅助 =====================
C_RESET=$'\033[0m'; C_GREEN=$'\033[1;32m'; C_YELLOW=$'\033[1;33m'; C_RED=$'\033[1;31m'; C_BLUE=$'\033[1;34m'
log()   { printf '%s[INFO]%s %s\n'  "$C_GREEN"  "$C_RESET" "$*"; }
warn()  { printf '%s[WARN]%s %s\n'  "$C_YELLOW" "$C_RESET" "$*"; }
error() { printf '%s[ERROR]%s %s\n' "$C_RED"    "$C_RESET" "$*" >&2; exit 1; }
step()  { printf '\n%s==> %s%s\n'   "$C_BLUE"   "$C_RESET" "$*"; }

usage() {
  cat <<'USAGE'
Headscale 一键部署脚本（支持 HTTPS + 定时证书更新）

用法:
  sudo bash deploy-headscale.sh [选项]

选项:
  -u, --url URL          客户端访问地址，如 http://1.2.3.4:8080 或 https://hs.example.com
  -e, --email MAIL       ACME 注册邮箱（certbot 模式必填；acme 模式可选）
      --tls-mode MODE    HTTPS 模式: acme(默认) | certbot | none
      --base-domain DOM  MagicDNS 根域（默认自动派生为 server_url 的子域，避免冲突）
      --derp             启用内置 DERP 中继（公网环境建议）
  -v, --version VER      指定 headscale 版本，默认 0.29.7
      --binary PATH      使用本地已有的 headscale 二进制（离线部署：机器连不上 GitHub 时用）
      --config PATH      使用本地已有的配置示例（离线部署时用）
      --prefix-v4 CIDR   虚拟网段，默认官方示例的 100.64.0.0/10，例如 10.0.0.0/24
      --public-ip IP     显式指定公网 IP（NAT 后面自动探测会拿到内网地址，内置 DERP 需要）
  -h, --help             显示帮助

离线部署（GitHub 不可达时，在能上网的机器上备好文件再上传）：
  1. 下载二进制 headscale_0.29.7_linux_amd64 与配置示例 config-example.yaml
  2. scp 到目标机，然后：
     sudo bash deploy-headscale.sh -u http://<公网IP>:18080 --tls-mode none \
          --binary ./headscale_0.29.7_linux_amd64 --config ./config-example.yaml

部署后证书管理:
  headscale-cert status   查看证书到期时间
  headscale-cert renew    手动续期（certbot 模式，会重启服务使新证书生效）
  headscale-cert reload   仅重载 ACL 策略（v0.29.x 的 SIGHUP 不重载证书）
USAGE
  exit 0
}

# ===================== 参数解析 =====================
while [[ $# -gt 0 ]]; do
  case "$1" in
    -u|--url)      SERVER_URL="${2:-}"; shift 2 ;;
    -e|--email)    ACME_EMAIL="${2:-}"; shift 2 ;;
    --tls-mode)    TLS_MODE="${2:-}"; shift 2 ;;
    --base-domain) BASE_DOMAIN="${2:-}"; shift 2 ;;
    -v|--version)  HS_VERSION="${2:-}"; shift 2 ;;
    --derp)        ENABLE_DERP="yes"; shift ;;
    --binary)      LOCAL_BINARY="${2:-}"; shift 2 ;;
    --config)      LOCAL_CONFIG="${2:-}"; shift 2 ;;
    --prefix-v4)   PREFIX_V4="${2:-}"; shift 2 ;;
    --public-ip)   PUBLIC_IP_OVERRIDE="${2:-}"; shift 2 ;;
    -h|--help)     usage ;;
    *)             error "未知参数: $1 （使用 --help 查看用法）" ;;
  esac
done

# ===================== 前置检查 =====================
[[ $EUID -eq 0 ]] || error "请以 root 用户运行（sudo bash $0）"
command -v systemctl >/dev/null 2>&1 || error "未检测到 systemd，本脚本需要 systemd 环境"

# ===================== 检测发行版 / 架构 =====================
step "检测系统环境"
if [[ -r /etc/os-release ]]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  OS_ID="${ID:-unknown}"
  PRETTY="${PRETTY_NAME:-$OS_ID}"
else
  error "无法读取 /etc/os-release，不支持该系统"
fi

case "$OS_ID" in
  ubuntu|debian|linuxmint|pop|raspbian) PKG="apt" ;;
  centos|rhel|rocky|almalinux|fedora|ol|amzn) PKG="yum" ;;
  *)
    warn "未识别的发行版 '$OS_ID'，使用通用二进制方式安装（跳过依赖安装）"
    PKG=""
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64)   HS_ARCH="amd64" ;;
  aarch64|arm64)  HS_ARCH="arm64" ;;
  *) error "不支持的 CPU 架构: $(uname -m)" ;;
esac

log "系统: ${PRETTY} | 包管理: ${PKG:-无} | 架构: ${HS_ARCH} | 版本: v${HS_VERSION}"

# ===================== 公网 IP =====================
detect_public_ip() {
  local ip=""
  ip="$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)"
  [[ -z "$ip" ]] && ip="$(curl -fsS --max-time 5 https://ifconfig.me/ip 2>/dev/null || true)"
  [[ -z "$ip" ]] && ip="$(curl -fsS --max-time 5 https://ipinfo.io/ip 2>/dev/null || true)"
  printf '%s' "$ip"
}

# ===================== 交互补充 =====================
if [[ -z "$SERVER_URL" ]]; then
  PUBLIC_IP="$(detect_public_ip)"
  echo
  log "请输入客户端访问 Headscale 的对外地址。"
  log "  内网测试: http://<服务器IP>:8080"
  log "  公网生产: https://hs.example.com （需域名解析到本机，80/443 可访问）"
  if [[ -n "$PUBLIC_IP" ]]; then
    printf '  检测到公网 IP: %s\n' "$PUBLIC_IP"
    DEFAULT_URL="http://${PUBLIC_IP}:8080"
  else
    DEFAULT_URL="http://127.0.0.1:8080"
  fi
  read -rp "  对外地址 [$DEFAULT_URL]: " SERVER_URL
  SERVER_URL="${SERVER_URL:-$DEFAULT_URL}"
fi

SERVER_URL="${SERVER_URL%/}"
if [[ "$SERVER_URL" == https://* ]]; then
  ENABLE_TLS="yes"
  DOMAIN="$(printf '%s' "$SERVER_URL" | sed -E 's#^https://##; s#:[0-9]+$##; s#/$##')"
  if [[ -z "$TLS_MODE" ]]; then TLS_MODE="acme"; fi
  if [[ "$TLS_MODE" == "certbot" && -z "$ACME_EMAIL" ]]; then
    read -rp "  ACME 注册邮箱 [admin@${DOMAIN}]: " ACME_EMAIL
    ACME_EMAIL="${ACME_EMAIL:-admin@${DOMAIN}}"
  fi
  if [[ "$ENABLE_DERP" != "yes" ]]; then
    read -rp "  是否启用内置 DERP 中继？公网环境建议开启 [y/N]: " ans
    [[ "$ans" =~ ^[Yy] ]] && ENABLE_DERP="yes"
  fi
elif [[ "$SERVER_URL" == http://* ]]; then
  ENABLE_TLS="no"
  TLS_MODE="none"
  # 从 URL 提取端口作为监听端口；无端口时默认 8080
  LISTEN_PORT="$(printf '%s' "$SERVER_URL" | sed -nE 's#^http://[^/]*:([0-9]+).*#\1#p')"
  LISTEN_PORT="${LISTEN_PORT:-8080}"
  DOMAIN="$(printf '%s' "$SERVER_URL" | sed -E 's#^http://##; s#:[0-9]+$##; s#/.*$##')"
  warn "HTTP 明文模式（监听端口 ${LISTEN_PORT}）；较新 Tailscale 客户端可能需要环境变量 TS_ALLOW_HTTP_CONTROL_SERVER=1"
else
  error "对外地址必须以 http:// 或 https:// 开头"
fi

# v0.29.7 起强制校验：server_url 的主机名不能等于 base_domain 或成为其子域，
# 否则 Tailscale 会接管该域导致 headscale/DERP 不可达（源码 isSafeServerURL）。
# 官方示例默认是 example.com，若用户域名恰好是其子域（如 hs.example.com）会冲突。
# 未显式指定时，自动派生为 server_url 域名的子域，天然满足校验。
if [[ -z "$BASE_DOMAIN" ]]; then
  if [[ -n "$DOMAIN" && "$DOMAIN" != *:* ]]; then
    # 去掉最左标签，构造一个与 server 主机名不同的子域；失败则退回固定安全值
    BASE_DOMAIN="hs.${DOMAIN}"
  else
    BASE_DOMAIN="headscale.local"
  fi
fi
log "MagicDNS base_domain: ${BASE_DOMAIN}"

if [[ "$TLS_MODE" == "certbot" && -z "$ACME_EMAIL" ]]; then error "certbot 模式需要 --email 指定 ACME 邮箱"; fi
# 云主机的网卡上是内网地址，自动探测到的「公网 IP」往往不对（NAT 后面），
# 内置 DERP 要把这个地址告诉客户端，指定 --public-ip 才能填对。
[[ -n "$PUBLIC_IP_OVERRIDE" ]] && PUBLIC_IP="$PUBLIC_IP_OVERRIDE"
[[ -z "$PUBLIC_IP" ]] && PUBLIC_IP="$(detect_public_ip)"

echo
log "部署参数确认:"
log "  对外地址 : ${SERVER_URL}"
log "  HTTPS    : ${ENABLE_TLS}"
if [[ "$ENABLE_TLS" == "yes" ]]; then log "  证书模式 : ${TLS_MODE} (域名 ${DOMAIN})"; fi
log "  内置DERP : ${ENABLE_DERP}"
echo

# ===================== 依赖安装 =====================
step "检查并安装依赖"
need_install=()
for c in curl wget openssl; do
  command -v "$c" >/dev/null 2>&1 || need_install+=("$c")
done
if [[ ${#need_install[@]} -gt 0 ]]; then
  case "$PKG" in
    apt) apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${need_install[@]}" ;;
    yum) yum install -y "${need_install[@]}" ;;
    *)   error "缺少依赖 ${need_install[*]}，请手动安装后重试" ;;
  esac
fi
log "基础依赖就绪"

install_certbot() {
  command -v certbot >/dev/null 2>&1 && { CERTBOT_BIN="$(command -v certbot)"; return 0; }
  log "安装 certbot ..."
  case "$PKG" in
    apt) apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq certbot ;;
    yum)
      yum install -y epel-release >/dev/null 2>&1 || true
      yum install -y certbot >/dev/null 2>&1 || { command -v dnf >/dev/null && dnf install -y certbot >/dev/null 2>&1; } || true
      ;;
  esac
  command -v certbot >/dev/null 2>&1 || error "certbot 安装失败，请手动安装后重试，或改用 --tls-mode acme"
  CERTBOT_BIN="$(command -v certbot)"
  log "certbot 就绪: ${CERTBOT_BIN}"
}

# ===================== 下载二进制 =====================
if [[ -n "$LOCAL_BINARY" ]]; then
  step "安装本地 headscale 二进制 ${LOCAL_BINARY}（离线部署）"
  [[ -f "$LOCAL_BINARY" ]] || error "本地二进制不存在: ${LOCAL_BINARY}"
  install -m 0755 "$LOCAL_BINARY" "$INSTALL_BIN"
else
  step "下载 headscale v${HS_VERSION} (linux_${HS_ARCH})"
  DL_URL="https://github.com/juanfont/headscale/releases/download/v${HS_VERSION}/headscale_${HS_VERSION}_linux_${HS_ARCH}"
  TMP_BIN="$(mktemp)"
  curl -fL --retry 3 --progress-bar "$DL_URL" -o "$TMP_BIN" || error "下载失败: $DL_URL"
  install -m 0755 "$TMP_BIN" "$INSTALL_BIN"
  rm -f "$TMP_BIN"
fi
"$INSTALL_BIN" version | head -5

# ===================== 用户与目录 =====================
step "创建系统用户与目录"
if ! id headscale >/dev/null 2>&1; then
  if command -v useradd >/dev/null 2>&1; then
    useradd --create-home --home-dir "$DATA_DIR" --system --user-group \
            --shell /usr/sbin/nologin headscale
  else
    adduser --system --group --home "$DATA_DIR" --no-create-home --disabled-login headscale
  fi
fi
mkdir -p "$CONFIG_DIR" "$DATA_DIR" "${DATA_DIR}/cache" "$TLS_CERT_DIR" /var/run/headscale
chown -R headscale:headscale "$DATA_DIR" "$CONFIG_DIR" /var/run/headscale
chmod 750 "$DATA_DIR" "$CONFIG_DIR"
log "用户/目录就绪"

# ===================== 生成配置 =====================
# 关键：v0.29.x 使用「扁平键」，必须从对应 tag 下载示例；main 分支是嵌套键，不可用。
if [[ -n "$LOCAL_CONFIG" ]]; then
  step "使用本地配置示例 ${LOCAL_CONFIG}（离线部署）"
  [[ -f "$LOCAL_CONFIG" ]] || error "本地配置示例不存在: ${LOCAL_CONFIG}"
  cp "$LOCAL_CONFIG" "$CONFIG_FILE"
  log "配置来源: 本地文件 ${LOCAL_CONFIG}"
else
  step "生成配置文件 ${CONFIG_FILE}（v${HS_VERSION} 对应 tag 的官方示例）"
  EXAMPLE_URL="https://raw.githubusercontent.com/juanfont/headscale/v${HS_VERSION}/config-example.yaml"
  curl -fsSL --retry 3 --retry-delay 2 "$EXAMPLE_URL" -o "$CONFIG_FILE" || error "下载配置示例失败: $EXAMPLE_URL"
  log "配置来源: ${EXAMPLE_URL}"
fi
grep -q '^server_url:' "$CONFIG_FILE" || error "配置示例格式异常（未找到 server_url 扁平键），请确认版本 v${HS_VERSION}"
grep -q '^tls_letsencrypt_hostname:' "$CONFIG_FILE" || error "配置示例缺少 tls_letsencrypt_hostname，版本可能不兼容"

# 顶层扁平键替换（幂等：存在则替换，否则追加）
set_key() { # set_key <key> <value>
  local key="$1" val="$2" f="$CONFIG_FILE"
  if grep -qE "^${key}:" "$f"; then
    sed -i -E "s|^${key}:.*|${key}: ${val}|" "$f"
  else
    printf '%s: %s\n' "$key" "$val" >>"$f"
  fi
}

set_key "server_url" "$SERVER_URL"
set_key "metrics_listen_addr" '""'        # 关闭 metrics，减少暴露面
set_key "disable_check_updates" "true"    # 避免启动时的版本检查

# dns.base_domain 是 2 空格缩进的嵌套键，需在 dns: 块内替换
if grep -qE '^  base_domain:' "$CONFIG_FILE"; then
  sed -i -E "s|^  base_domain:.*|  base_domain: ${BASE_DOMAIN}|" "$CONFIG_FILE"
else
  error "配置示例缺少 dns.base_domain，版本可能不兼容"
fi
log "已设置 MagicDNS base_domain: ${BASE_DOMAIN}"

# prefixes.v4 是 prefixes: 块内的嵌套键（虚拟网段）。
# 必须限定在 prefixes 块内替换：直接 sed 全文替换会把别的块里的同名 v4 键也改掉。
if [[ -n "$PREFIX_V4" ]]; then
  grep -qE '^prefixes:' "$CONFIG_FILE" || error "配置示例缺少 prefixes 块，版本可能不兼容"
  awk -v v="$PREFIX_V4" '
    /^prefixes:[[:space:]]*$/ { inblk=1; print; next }
    inblk && /^[^[:space:]#]/ { inblk=0 }
    inblk && /^[[:space:]]+v4:/ { sub(/v4:.*/, "v4: " v); print; next }
    { print }
  ' "$CONFIG_FILE" >"$CONFIG_FILE.tmp" && mv "$CONFIG_FILE.tmp" "$CONFIG_FILE"
  grep -qE '^  v4: '"${PREFIX_V4}"'$' "$CONFIG_FILE" || error "虚拟网段写入失败: ${PREFIX_V4}"
  log "已设置虚拟网段 prefixes.v4: ${PREFIX_V4}"
fi

case "$TLS_MODE" in
  acme)
    # v0.29.x 扁平键；ACME HTTP-01 用 tls_letsencrypt_listen(:80) 验证，
    # 服务端口 listen_addr 必须是非 80 端口，这里用 443 对外提供 HTTPS。
    set_key "listen_addr" "0.0.0.0:443"
    set_key "tls_letsencrypt_hostname" "$DOMAIN"
    set_key "tls_letsencrypt_challenge_type" "HTTP-01"
    set_key "tls_letsencrypt_listen" '":http"'
    set_key "tls_letsencrypt_cache_dir" "/var/lib/headscale/cache"
    if [[ -n "$ACME_EMAIL" ]]; then set_key "acme_email" "$ACME_EMAIL"; fi
    log "已启用内置 ACME: ${DOMAIN}（autocert 自动续期，无需外部定时任务）"
    ;;
  certbot)
    set_key "listen_addr" "0.0.0.0:443"
    set_key "tls_cert_path" "${TLS_CERT_DIR}/fullchain.pem"
    set_key "tls_key_path" "${TLS_CERT_DIR}/privkey.pem"
    # 确保不启用内置 ACME，避免与外部证书冲突
    set_key "tls_letsencrypt_hostname" '""'
    log "已配置外部证书路径（certbot 模式）"
    ;;
  none)
    set_key "listen_addr" "0.0.0.0:${LISTEN_PORT:-8080}"
    ;;
esac

if [[ "$ENABLE_DERP" == "yes" ]]; then
  if [[ -z "$PUBLIC_IP" ]]; then warn "未检测到公网 IP，DERP 的 ipv4 将留空（直连不受影响）"; fi
  # v0.29.x derp.server 是 4 空格缩进的嵌套键（此版本确为嵌套，非扁平）
  sed -i -E '/^derp:/,/^[^ \t]/ {
    s|^    enabled:.*|    enabled: true|
    s|^    region_id:.*|    region_id: 999|
    s|^    region_code:.*|    region_code: "headscale"|
    s|^    region_name:.*|    region_name: "Headscale Embedded DERP"|
    s|^    stun_listen_addr:.*|    stun_listen_addr: "0.0.0.0:3478"|
    s|^    private_key_path:.*|    private_key_path: /var/lib/headscale/derp_server_private.key|
    s|^    ipv4:.*|    ipv4: "'"${PUBLIC_IP}"'"|
  }' "$CONFIG_FILE"
  log "已启用内置 DERP（region_id=999, STUN :3478）"
fi

chown headscale:headscale "$CONFIG_FILE"
chmod 600 "$CONFIG_FILE"

# 用 v0.29.x 支持的配置校验命令自检（真实二进制验证过，失败即终止，不带病启动）
# 注意：configtest 会真实创建 Noise 私钥/SQLite 库；此时是 root 身份，生成的文件
# 属主为 root(0600)，而服务以 headscale 用户运行 —— 必须重新 chown，否则启动报 permission denied。
if ! CT_OUTPUT="$("$INSTALL_BIN" configtest -c "$CONFIG_FILE" 2>&1)"; then
  error "configtest 配置校验失败，已终止。headscale 原始报错："$'\n'"${CT_OUTPUT}"
fi
chown -R headscale:headscale "$DATA_DIR"
log "配置校验通过（configtest），数据目录属主已修正为 headscale"

# ===================== 生成证书管理脚本 =====================
step "生成证书管理命令 ${CERT_MANAGER}"
cat >"$CERT_MANAGER" <<'CERTMGR'
#!/usr/bin/env bash
set -euo pipefail
CERT_MODE="__TLS_MODE__"
DOMAIN="__DOMAIN__"
CERT_DIR="/var/lib/headscale/tls"
CERTBOT_BIN="__CERTBOT_BIN__"

case "${1:-status}" in
  status)
    echo "证书模式: ${CERT_MODE}"
    if [[ "$CERT_MODE" == "certbot" ]]; then
      if [[ -f "$CERT_DIR/fullchain.pem" ]]; then
        openssl x509 -in "$CERT_DIR/fullchain.pem" -noout -dates -subject
      else
        echo "未找到证书文件: $CERT_DIR/fullchain.pem"
      fi
      command -v certbot >/dev/null && certbot certificates 2>/dev/null | grep -A4 "$DOMAIN" || true
    elif [[ "$CERT_MODE" == "acme" ]]; then
      echo "headscale 内置 ACME（autocert）自动续期：剩余 30 天时续期，30-60 分钟随机检查"
      find /var/lib/headscale/cache -type f 2>/dev/null | while read -r f; do
        echo "--- $f"
        openssl x509 -in "$f" -noout -dates -subject 2>/dev/null || true
      done
    else
      echo "当前为 HTTP 模式，未启用 HTTPS 证书"
    fi
    ;;
  renew)
    if [[ "$CERT_MODE" != "certbot" ]]; then
      echo "仅 certbot 模式需要手动续期；acme 模式由 headscale 自动续期"
      exit 0
    fi
    "$CERTBOT_BIN" renew --quiet
    # v0.29.x 的 SIGHUP 仅重载 ACL，不重载证书，必须重启使新证书生效
    systemctl restart headscale
    echo "续期完成，已重启 headscale 使新证书生效"
    ;;
  reload)
    # 如实说明：此命令只重载 ACL 策略
    systemctl reload headscale
    echo "已发送 SIGHUP（v0.29.x 仅重载 ACL 策略，不重载 TLS 证书）"
    ;;
  *)
    echo "用法: $0 {status|renew|reload}"; exit 1
    ;;
esac
CERTMGR
sed -i \
  -e "s|__TLS_MODE__|${TLS_MODE}|g" \
  -e "s|__DOMAIN__|${DOMAIN}|g" \
  -e "s|__CERTBOT_BIN__|${CERTBOT_BIN}|g" \
  "$CERT_MANAGER"
chmod 0755 "$CERT_MANAGER"
log "证书管理命令已就绪: headscale-cert"

# ===================== 创建 systemd 服务 =====================
step "创建 systemd 服务"
cat >"$SERVICE_FILE" <<'EOF'
[Unit]
After=network.target
Description=headscale coordination server for Tailscale

[Service]
Type=simple
User=headscale
Group=headscale
ExecStart=/usr/bin/headscale serve
ExecReload=/usr/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5
RestartPreventExitStatus=78

WorkingDirectory=/var/lib/headscale
ReadWritePaths=/var/lib/headscale

AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
DevicePolicy=closed
LockPersonality=true
MemoryDenyWriteExecute=true
NoNewPrivileges=true
PrivateDevices=true
PrivateMounts=true
PrivateTmp=true
ProtectClock=true
ProtectControlGroups=true
ProtectHome=true
ProtectHostname=true
ProtectKernelLogs=true
ProtectKernelModules=true
ProtectKernelTunables=true
ProtectSystem=strict
RemoveIPC=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
RuntimeDirectory=headscale
RuntimeDirectoryMode=0750
StateDirectory=headscale
StateDirectoryMode=0750
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged
SystemCallFilter=~@resources
UMask=0077

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
log "systemd 服务已写入"

# ===================== HTTPS 证书申请 + 定时续期 =====================
if [[ "$TLS_MODE" == "certbot" ]]; then
  step "certbot 申请证书并配置定时续期"
  install_certbot

  if command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | grep -qE ':80[[:space:]]'; then
    warn "检测到 80 端口已被占用，certbot standalone 验证可能失败"
  fi

  "$CERTBOT_BIN" certonly --standalone --non-interactive --agree-tos \
    -m "$ACME_EMAIL" -d "$DOMAIN" --preferred-challenges http \
    --cert-name headscale 2>&1 | tail -20 \
    || error "certbot 申请证书失败，请确认域名 ${DOMAIN} 已解析到本机且 80 端口可从公网访问"

  LIVE_DIR="/etc/letsencrypt/live/headscale"
  [[ -f "$LIVE_DIR/fullchain.pem" ]] || error "未找到证书文件 $LIVE_DIR/fullchain.pem"

  # certbot 私钥默认 root:600，headscale 用户读不到，故复制到其可读目录
  install -m 0644 -o headscale -g headscale "$LIVE_DIR/fullchain.pem" "$TLS_CERT_DIR/fullchain.pem"
  install -m 0600 -o headscale -g headscale "$LIVE_DIR/privkey.pem" "$TLS_CERT_DIR/privkey.pem"
  log "证书已部署到 ${TLS_CERT_DIR}"

  # deploy hook：续期成功后复制新证书并重启服务（v0.29.x SIGHUP 不重载证书）
  HOOK_DIR="/etc/letsencrypt/renewal-hooks/deploy"
  mkdir -p "$HOOK_DIR"
  cat >"$HOOK_DIR/headscale-reload.sh" <<HOOK
#!/bin/sh
set -e
install -m 0644 -o headscale -g headscale ${LIVE_DIR}/fullchain.pem ${TLS_CERT_DIR}/fullchain.pem
install -m 0600 -o headscale -g headscale ${LIVE_DIR}/privkey.pem ${TLS_CERT_DIR}/privkey.pem
systemctl restart headscale
HOOK
  chmod 0755 "$HOOK_DIR/headscale-reload.sh"
  log "已配置 deploy hook（续期后复制新证书并重启 headscale）"

  # systemd timer：每天两次续期（错峰 + 随机延迟），避免依赖发行版自带 timer
  cat >/etc/systemd/system/headscale-certbot-renew.service <<EOF
[Unit]
Description=Renew Let's Encrypt certificate for headscale
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=${CERTBOT_BIN} renew --quiet
EOF
  cat >/etc/systemd/system/headscale-certbot-renew.timer <<'EOF'
[Unit]
Description=Twice-daily Let's Encrypt renewal for headscale

[Timer]
OnCalendar=*-*-* 00,12:17:00
RandomizedDelaySec=3600
Persistent=true

[Install]
WantedBy=timers.target
EOF
  systemctl daemon-reload
  systemctl enable --now headscale-certbot-renew.timer
  log "已启用定时续期 timer（每天 00:17 / 12:17，随机延迟）"

  if "$CERTBOT_BIN" renew --dry-run >/dev/null 2>&1; then
    log "续期链路 dry-run 验证通过"
  else
    warn "续期 dry-run 未通过，请稍后执行: certbot renew --dry-run"
  fi
elif [[ "$TLS_MODE" == "acme" ]]; then
  step "内置 ACME 说明"
  log "headscale 使用 autocert 自动申请/续期证书："
  log "  - 证书有效期 3 个月，剩余 30 天时自动续期"
  log "  - 30-60 分钟随机间隔检查，无需外部 cron/timer"
  log "  - 首次启动向 Let's Encrypt 申请证书（需 80 端口可达）"
  log "  - 可用 headscale-cert status 查看证书到期时间"
fi

# ===================== 防火墙 =====================
step "配置防火墙"
if [[ "$TLS_MODE" == "acme" || "$TLS_MODE" == "certbot" ]]; then
  PORTS_TCP="80 443"
else
  PORTS_TCP="${LISTEN_PORT:-8080}"   # HTTP 模式放行实际监听端口
fi
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi "active"; then
  for p in $PORTS_TCP; do ufw allow "${p}/tcp" >/dev/null 2>&1 || true; done
  if [[ "$ENABLE_DERP" == "yes" ]]; then ufw allow 3478/udp >/dev/null 2>&1 || true; fi
  log "ufw 已放行 TCP: ${PORTS_TCP}"$([[ "$ENABLE_DERP" == "yes" ]] && echo ", UDP: 3478")
elif command -v firewall-cmd >/dev/null 2>&1 && systemctl is-active --quiet firewalld; then
  for p in $PORTS_TCP; do firewall-cmd --permanent --add-port="${p}/tcp" >/dev/null 2>&1 || true; done
  if [[ "$ENABLE_DERP" == "yes" ]]; then firewall-cmd --permanent --add-port=3478/udp >/dev/null 2>&1 || true; fi
  firewall-cmd --reload >/dev/null 2>&1 || true
  log "firewalld 已放行 TCP: ${PORTS_TCP}"$([[ "$ENABLE_DERP" == "yes" ]] && echo ", UDP: 3478")
else
  warn "未检测到启用的防火墙，请自行放行 TCP: ${PORTS_TCP}$([[ "$ENABLE_DERP" == "yes" ]] && echo ", UDP: 3478")"
fi

# ===================== 启动服务 =====================
step "启动 headscale 服务"
systemctl enable --now headscale
sleep 3
if systemctl is-active --quiet headscale; then
  log "服务运行正常"
  systemctl --no-pager status headscale | sed -n '1,8p' || true
else
  warn "服务未进入 active 状态，最近日志："
  journalctl -u headscale -n 30 --no-pager || true
  error "启动失败，请根据日志排查（常见：端口占用、80/443 未放行、域名未解析、配置错误）"
fi

# ===================== 用户与预授权密钥 =====================
step "创建默认用户与预授权密钥"
HS="$INSTALL_BIN"
if "$HS" users list 2>/dev/null | grep -qw "$DEFAULT_USER"; then
  log "用户 '${DEFAULT_USER}' 已存在"
else
  if "$HS" users create "$DEFAULT_USER" >/dev/null 2>&1; then
    log "已创建用户 '${DEFAULT_USER}'"
  else
    warn "创建用户失败，请手动执行: headscale users create ${DEFAULT_USER}"
  fi
fi

# v0.29.x 的 preauthkeys --user 只接受数字 ID（Uint64）。
# users list 是表格：ID | Name | Username | ...，且输出带 ANSI 颜色码，必须先剥离再按 '|' 分列。
# 关键1：先 sed 去掉 \033[...m 颜色码，否则 $3 含转义序列导致匹配失败。
# 关键2：管道末端必须 || true，否则无匹配时在 set -euo pipefail 下会直接终止脚本。
STRIP_ANSI=$'s/\033\\[[0-9;]*[a-zA-Z]//g'
USER_ID="$("$HS" users list 2>/dev/null | sed -E "$STRIP_ANSI" | awk -F'|' -v u="$DEFAULT_USER" '{gsub(/^[ \t]+|[ \t]+$/, "", $3); if ($3==u) {gsub(/^[ \t]+|[ \t]+$/, "", $1); print $1}}' | head -1 || true)"
AUTHKEY=""
if [[ "$USER_ID" =~ ^[0-9]+$ ]]; then
  # 成功时默认输出为一整行 URL-safe 的 key（含大小写字母、连字符、下划线），不能用 hex 正则
  AUTHKEY="$("$HS" preauthkeys create --user "$USER_ID" --expiration 24h 2>/dev/null | grep -vE '^[[:space:]]*$' | tail -1 || true)"
fi
if [[ -n "$AUTHKEY" ]]; then
  log "已生成 24h 预授权密钥: ${AUTHKEY}"
else
  warn "预授权密钥生成失败，可手动（--user 需数字 ID）: headscale users list && headscale preauthkeys create --user <ID> --expiration 24h"
fi

# ===================== 完成 =====================
cat <<EOF

${C_GREEN}================== 部署完成 ==================${C_RESET}

服务地址   : ${SERVER_URL}
健康检查   : ${SERVER_URL}/health
配置文件   : ${CONFIG_FILE}
数据目录   : ${DATA_DIR}
服务管理   : systemctl {start|stop|restart|status|reload} headscale
证书管理   : headscale-cert {status|renew|reload}
常用命令   : headscale users list
             headscale nodes list
             headscale preauthkeys create --user <数字ID> --expiration 24h

${C_BLUE}HTTPS / 证书:${C_RESET}
EOF

if [[ "$TLS_MODE" == "certbot" ]]; then
  cat <<EOF
  模式       : certbot 外部证书
  证书文件   : ${TLS_CERT_DIR}/fullchain.pem
  定时续期   : systemd timer 每天 00:17 / 12:17（随机延迟）
  续期钩子   : 复制新证书并重启 headscale 使其生效
  手动检查   : headscale-cert status && certbot renew --dry-run
EOF
elif [[ "$TLS_MODE" == "acme" ]]; then
  cat <<EOF
  模式       : headscale 内置 ACME (autocert)
  自动续期   : 剩余 30 天时续期，30-60 分钟随机检查，无需外部定时任务
  查看到期   : headscale-cert status
EOF
else
  cat <<EOF
  模式       : HTTP（未启用 HTTPS）。如需 HTTPS 重新运行脚本并传 https:// 地址。
EOF
fi

cat <<EOF

${C_BLUE}客户端接入 (Linux/macOS):${C_RESET}
  curl -fsSL https://tailscale.com/install.sh | sh
  sudo tailscale up --login-server=${SERVER_URL} --authkey=${AUTHKEY:-<在服务端生成密钥>}

  Windows: 按住 Shift 右键托盘图标 -> Run tailscale up，执行：
  tailscale up --login-server=${SERVER_URL} --authkey=${AUTHKEY:-<在服务端生成密钥>}

${C_YELLOW}注意事项:${C_RESET}
  1. HTTPS 模式需确保域名解析到本机，且 80（验证）/443（服务）端口可从公网访问。
  2. 启用内置 DERP 时放行 UDP/3478，用于 STUN NAT 穿透。
  3. v0.29.x 的 SIGHUP 仅重载 ACL 策略；TLS 证书更新需重启服务（headscale-cert renew 已处理）。
  4. 升级 headscale 请逐个小版本升级（0.28->0.29...），不要跨版本。

EOF
