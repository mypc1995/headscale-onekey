#!/usr/bin/env bash
#
# Headscale Admin 部署脚本（Go 单二进制，完整管理界面）
# ------------------------------------------------------------------
# 功能：编译安装 -> 初始化管理员 -> systemd -> 防火墙 -> 启动
# 默认端口：3001
# 说明：以 root 运行，因为需要调用 headscale CLI、读写 /etc/headscale 与 journalctl
#
set -euo pipefail

PORT="3001"
ALLOW_CIDR=""
NO_FIREWALL="no"
ADMIN_USER="admin"
ADMIN_PASS=""
RESET_ADMIN="no"
AUTO_PASS="no"
PRESERVE_ADMIN="no"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_ROOT="$SCRIPT_DIR"
[[ -f "$SRC_ROOT/main.go" ]] || SRC_ROOT="$SCRIPT_DIR/headscale-admin"
INSTALL_DIR="/opt/headscale-admin"
DATA_DIR="/var/lib/headscale-admin"
DB_FILE="${DATA_DIR}/headscale-admin.db"
LEGACY_JSON="${DATA_DIR}/admins.json"
BIN="/usr/local/bin/headscale-admin"
SERVICE_FILE="/etc/systemd/system/headscale-admin.service"

C_RESET=$'\033[0m'; C_GREEN=$'\033[1;32m'; C_YELLOW=$'\033[1;33m'; C_RED=$'\033[1;31m'; C_BLUE=$'\033[1;34m'
log()   { printf '%s[INFO]%s %s\n'  "$C_GREEN"  "$C_RESET" "$*"; }
warn()  { printf '%s[WARN]%s %s\n'  "$C_YELLOW" "$C_RESET" "$*"; }
error() { printf '%s[ERROR]%s %s\n' "$C_RED"    "$C_RESET" "$*" >&2; exit 1; }
step()  { printf '\n%s==> %s%s\n'   "$C_BLUE"   "$C_RESET" "$*"; }

usage() {
  cat <<'EOF'
用法: sudo bash deploy-headscale-admin.sh [选项]

选项:
  --port PORT           监听端口，默认 3001
  --admin-user USER     初始管理员用户名，默认 admin
  --admin-pass PASS     初始管理员密码；不指定则交互输入或自动生成
  --reset-admin         重置管理员密码（会覆盖该用户旧密码）
  --allow-cidr CIDR     仅允许指定网段访问 Web UI
  --no-firewall         不修改防火墙
  -h, --help            显示帮助
EOF
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --port)        PORT="${2:-}"; shift 2 ;;
    --admin-user)  ADMIN_USER="${2:-}"; shift 2 ;;
    --admin-pass)  ADMIN_PASS="${2:-}"; shift 2 ;;
    --reset-admin) RESET_ADMIN="yes"; shift ;;
    --allow-cidr)  ALLOW_CIDR="${2:-}"; shift 2 ;;
    --no-firewall) NO_FIREWALL="yes"; shift ;;
    -h|--help)     usage ;;
    *)             error "未知参数: $1" ;;
  esac
done

[[ $EUID -eq 0 ]] || error "请以 root 运行（sudo bash $0）"
command -v systemctl >/dev/null 2>&1 || error "未检测到 systemd"
command -v curl >/dev/null 2>&1 || error "缺少 curl"
command -v openssl >/dev/null 2>&1 || error "缺少 openssl"
[[ -f "$SRC_ROOT/main.go" ]] || error "未找到源码 main.go，请在仓库目录运行"

# 已有管理员且不是重置时，保留原密码
if [[ -f "$DB_FILE" || -f "$LEGACY_JSON" ]] && [[ "$RESET_ADMIN" != "yes" ]]; then
  PRESERVE_ADMIN="yes"
  if [[ -f "$DB_FILE" ]]; then
    log "检测到已有数据库，保留现有管理员密码（如需重置加 --reset-admin）"
  else
    log "检测到旧版 admins.json，将自动迁移到 SQLite 并保留原密码"
  fi
else
  if [[ -z "$ADMIN_PASS" ]]; then
    if [[ -t 0 ]]; then
      read -rsp "  设置管理员 ${ADMIN_USER} 的密码: " ADMIN_PASS; echo
      [[ -n "$ADMIN_PASS" ]] || error "密码不能为空"
      read -rsp "  确认密码: " ADMIN_PASS_CONFIRM; echo
      [[ "$ADMIN_PASS" == "$ADMIN_PASS_CONFIRM" ]] || error "两次密码不一致"
    else
      ADMIN_PASS="$(openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 20)"
      AUTO_PASS="yes"
      log "未指定密码且无终端输入，已自动生成随机密码"
    fi
  fi
fi

step "安装编译依赖（Go + GCC）"
if command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1; then
  (dnf install -y golang gcc 2>/dev/null || yum install -y golang gcc) >/dev/null 2>&1 || error "安装 Go/GCC 失败"
elif command -v apt >/dev/null 2>&1; then
  apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq golang gcc >/dev/null 2>&1 || error "安装 Go/GCC 失败"
fi
go version && gcc --version | head -1

step "编译 Headscale Admin"
cd "$SRC_ROOT"
# 纯 Go 的 SQLite 驱动，无需 gcc；国内机器默认走 goproxy.cn，可用 GOPROXY 覆盖。
export GOPROXY="${GOPROXY:-https://goproxy.cn,https://proxy.golang.org,direct}"
log "下载 Go 依赖（GOPROXY=${GOPROXY}）"
go mod download >/dev/null 2>&1 || warn "go mod download 未完全成功，尝试直接编译"
NEW_BIN="${BIN}.new"
CGO_ENABLED=0 go build -o "$NEW_BIN" . >/dev/null 2>&1 || error "编译失败"
mkdir -p "$INSTALL_DIR" "$DATA_DIR"
systemctl stop headscale-admin >/dev/null 2>&1 || true
install -m 0755 "$NEW_BIN" "$BIN"
rm -f "$NEW_BIN"
cp -r "$SRC_ROOT/templates" "$SRC_ROOT/static" "$INSTALL_DIR/"
chmod 700 "$DATA_DIR"
command -v restorecon >/dev/null 2>&1 && restorecon -RF "$INSTALL_DIR" "$BIN" >/dev/null 2>&1 || true
log "二进制与资源已就绪: $BIN, $INSTALL_DIR"

INIT_ARGS=()
if [[ "$PRESERVE_ADMIN" != "yes" ]]; then
  if [[ "$RESET_ADMIN" == "yes" ]]; then
    INIT_ARGS+=(--reset-admin "${ADMIN_USER}:${ADMIN_PASS}")
  else
    INIT_ARGS+=(--init-admin "${ADMIN_USER}:${ADMIN_PASS}")
  fi
  step "初始化管理员"
  HSA_DATA_FILE="$DB_FILE" HSA_LEGACY_JSON="$LEGACY_JSON" "$BIN" --init-only "${INIT_ARGS[@]}" || error "管理员初始化失败"
  chmod 600 "$DB_FILE" 2>/dev/null || true
  [[ -f "$DB_FILE-wal" ]] && chmod 600 "$DB_FILE-wal"
  [[ -f "$DB_FILE-shm" ]] && chmod 600 "$DB_FILE-shm"
fi

step "创建 systemd 服务（端口 ${PORT}）"
cat >"$SERVICE_FILE" <<EOF
[Unit]
Description=Headscale Admin Console
After=network-online.target headscale.service
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=${INSTALL_DIR}
Environment=HSA_PORT=${PORT}
Environment=HSA_DATA_FILE=${DB_FILE}
Environment=HSA_LEGACY_JSON=${LEGACY_JSON}
Environment=HSA_TEMPLATES=${INSTALL_DIR}/templates
Environment=HSA_STATIC=${INSTALL_DIR}/static
ExecStart=${BIN}
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now headscale-admin >/dev/null 2>&1 || true
sleep 2
systemctl is-active --quiet headscale-admin || { journalctl -u headscale-admin -n 30 --no-pager || true; error "服务启动失败"; }
log "服务已启动并自启"

step "配置防火墙"
if [[ "$NO_FIREWALL" != "yes" ]] && command -v firewall-cmd >/dev/null 2>&1 && systemctl is-active --quiet firewalld; then
  if [[ -n "$ALLOW_CIDR" ]]; then
    firewall-cmd --permanent --add-rich-rule="rule family=ipv4 source address=${ALLOW_CIDR} port port=${PORT} protocol=tcp accept" >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
    log "firewalld 已仅允许 ${ALLOW_CIDR} 访问 TCP/${PORT}"
  else
    firewall-cmd --permanent --add-port="${PORT}/tcp" >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
    log "firewalld 已对所有来源开放 TCP/${PORT}"
  fi
elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi active; then
  if [[ -n "$ALLOW_CIDR" ]]; then
    ufw allow from "$ALLOW_CIDR" to any port "$PORT" proto tcp >/dev/null 2>&1 || true
    log "ufw 已仅允许 ${ALLOW_CIDR} 访问 TCP/${PORT}"
  else
    ufw allow "${PORT}/tcp" >/dev/null 2>&1 || true
    log "ufw 已对所有来源开放 TCP/${PORT}"
  fi
else
  warn "未检测到活动防火墙，请自行放行 TCP/${PORT}"
fi

HEALTH="$(curl -s --max-time 3 "http://127.0.0.1:${PORT}/health" || true)"
[[ "$HEALTH" == "ok" ]] || warn "本机健康检查未返回 ok，请检查 journalctl -u headscale-admin"

cat <<EOF

============================================================
 Headscale Admin 部署完成
============================================================
服务状态     : $(systemctl is-active headscale-admin)（开机自启）
访问地址     : http://<服务器IP>:${PORT}
健康检查     : http://127.0.0.1:${PORT}/health
数据库文件   : ${DB_FILE} (0600)
============
EOF
if [[ "$PRESERVE_ADMIN" == "yes" ]]; then
  echo "管理员账号   : 保留原密码（${ADMIN_USER} 若存在）"
else
  echo "管理员账号   : ${ADMIN_USER}"
  echo "管理员密码   : ${ADMIN_PASS}"
  echo "密码副本     : 请立即保存；服务本身只保存 PBKDF2 摘要"
fi
cat <<EOF
============
说明：
  - 访问地址：http://<服务器IP>:<端口>，账号密码见上方
  - 支持：总览、节点、用户、密钥与接入、网络与策略、运维监控、管理员
  - 修改配置/ACL 会自动备份原文件并做 configtest 校验
  - 停止服务：systemctl stop headscale-admin
============================================================
EOF
