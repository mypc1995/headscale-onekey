#!/usr/bin/env bash
#
# remove-headcontrol.sh —— 彻底卸载 HeadControl
#
# 用途：
#   本项目的 Headscale Admin（3001）已经覆盖 HeadControl（3000）的全部功能，
#   本脚本用于把 HeadControl 从服务器上清干净，避免两套界面并存、多开一个端口。
#
# 会清理：
#   - systemd 服务 headcontrol.service（停止、取消自启、删除 unit）
#   - 二进制 /usr/local/bin/headcontrol
#   - 程序目录 /opt/headcontrol、/opt/headcontrol-src
#   - 数据目录 /var/lib/headcontrol（--keep-data 可保留）
#   - 认证文件 /etc/headcontrol/auth.env、/root/.headcontrol-auth
#   - 本地 API key 文件 /root/.headcontrol-apikey（headscale 侧的 key 记录不受影响）
#   - 防火墙中 3000 端口的放行规则
#
# 不会动：Headscale 本体、Headscale Admin、/etc/headscale/config.yaml
#
# 用法：
#   sudo bash remove-headcontrol.sh [--port 3000] [--keep-data] [--yes]
#
set -uo pipefail

C_RED='\033[0;31m'; C_GREEN='\033[0;32m'; C_YELLOW='\033[1;33m'; C_BLUE='\033[0;36m'; C_RESET='\033[0m'
log()   { printf '%s[INFO]%s %s\n'  "$C_GREEN"  "$C_RESET" "$*"; }
warn()  { printf '%s[WARN]%s %s\n'  "$C_YELLOW" "$C_RESET" "$*"; }
error() { printf '%s[ERROR]%s %s\n' "$C_RED"    "$C_RESET" "$*" >&2; exit 1; }
step()  { printf '\n%s==> %s%s\n'   "$C_BLUE"   "$*" "$C_RESET"; }

PORT="3000"
KEEP_DATA="no"
ASSUME_YES="no"

while [ $# -gt 0 ]; do
  case "$1" in
    --port)      PORT="${2:-}"; shift 2 ;;
    --keep-data) KEEP_DATA="yes"; shift ;;
    -y|--yes)    ASSUME_YES="yes"; shift ;;
    -h|--help)   sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)           error "未知参数：$1（--help 查看用法）" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || error "请用 root 执行（sudo bash $0）"

UNIT_FILE="/etc/systemd/system/headcontrol.service"
BIN="/usr/local/bin/headcontrol"
RUN_DIR="/opt/headcontrol"
SRC_DIR="/opt/headcontrol-src"
DB_DIR="/var/lib/headcontrol"
AUTH_ENV_FILE="/etc/headcontrol"
AUTH_CRED_FILE="/root/.headcontrol-auth"
APIKEY_FILE="/root/.headcontrol-apikey"

step "检查当前状态"
INSTALLED="no"
for p in "$UNIT_FILE" "$BIN" "$RUN_DIR" "$SRC_DIR" "$DB_DIR"; do
  [ -e "$p" ] && INSTALLED="yes"
done
if [ "$INSTALLED" = "no" ]; then
  log "未发现 HeadControl 安装痕迹，无需清理"
else
  systemctl is-active --quiet headcontrol 2>/dev/null \
    && log "服务当前状态：running（将停止）" \
    || log "服务当前状态：未运行"
  log "将清理：$UNIT_FILE $BIN $RUN_DIR $SRC_DIR $DB_DIR $AUTH_ENV_FILE"
  if [ "$ASSUME_YES" != "yes" ]; then
    printf '确认卸载 HeadControl？[y/N] '
    read -r ans </dev/tty || ans="n"
    case "$ans" in y|Y|yes|YES) ;; *) log "已取消" ; exit 0 ;; esac
  fi
fi

step "停止并移除 systemd 服务"
if [ -f "$UNIT_FILE" ] || systemctl cat headcontrol.service >/dev/null 2>&1; then
  systemctl stop headcontrol 2>/dev/null || true
  systemctl disable headcontrol 2>/dev/null || true
  log "服务已停止并取消自启"
fi
rm -f "$UNIT_FILE"
systemctl daemon-reload
systemctl reset-failed headcontrol 2>/dev/null || true
log "unit 文件已删除"

# 兜底：unit 已删但进程仍在（二进制被删后进程会继续持有端口）
if pgrep -x headcontrol >/dev/null 2>&1; then
  pkill -x headcontrol 2>/dev/null || true
  for _ in 1 2 3 4 5; do
    pgrep -x headcontrol >/dev/null 2>&1 || break
    sleep 1
  done
  pgrep -x headcontrol >/dev/null 2>&1 && pkill -9 -x headcontrol 2>/dev/null || true
  log "已终止残留的 headcontrol 进程"
fi

step "删除程序与数据"
[ -f "$BIN" ] && rm -f "$BIN" && log "已删除 $BIN"
[ -d "$RUN_DIR" ] && rm -rf "$RUN_DIR" && log "已删除 $RUN_DIR"
[ -d "$SRC_DIR" ] && rm -rf "$SRC_DIR" && log "已删除 $SRC_DIR"
if [ -d "$DB_DIR" ]; then
  if [ "$KEEP_DATA" = "yes" ]; then
    log "保留数据目录 $DB_DIR（--keep-data）"
  else
    rm -rf "$DB_DIR" && log "已删除 $DB_DIR"
  fi
fi
if [ -d "$AUTH_ENV_FILE" ]; then
  rm -rf "$AUTH_ENV_FILE" && log "已删除 $AUTH_ENV_FILE"
fi
[ -f "$AUTH_CRED_FILE" ] && rm -f "$AUTH_CRED_FILE" && log "已删除 $AUTH_CRED_FILE"
[ -f "$APIKEY_FILE" ]    && rm -f "$APIKEY_FILE"    && log "已删除 $APIKEY_FILE（headscale 侧的 key 记录仍保留，可在管理界面吊销）"

step "清理防火墙规则（TCP/${PORT}）"
if command -v firewall-cmd >/dev/null 2>&1 && systemctl is-active --quiet firewalld; then
  firewall-cmd --permanent --remove-port="${PORT}/tcp" >/dev/null 2>&1 || true
  # 逐条移除带端口的 rich rule
  while read -r rule; do
    [ -n "$rule" ] || continue
    firewall-cmd --permanent --remove-rich-rule="$rule" >/dev/null 2>&1 || true
  done < <(firewall-cmd --permanent --list-rich-rules 2>/dev/null | grep -E "port port=\"?${PORT}\"? ")
  firewall-cmd --reload >/dev/null 2>&1 || true
  log "firewalld 规则已更新"
elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
  ufw delete allow "${PORT}/tcp" >/dev/null 2>&1 || true
  log "ufw 规则已更新"
else
  log "未检测到活动防火墙，跳过"
fi

step "验证"
if ss -lntp 2>/dev/null | grep -qE ":${PORT}\b"; then
  warn "TCP/${PORT} 仍在监听，请检查是否有其他进程占用："
  ss -lntp 2>/dev/null | grep -E ":${PORT}\b" || true
else
  log "TCP/${PORT} 已释放"
fi
for s in headscale headscale-admin; do
  if systemctl is-active --quiet "$s" 2>/dev/null; then
    log "${s} 仍在运行（未受影响）"
  fi
done

printf '\n%s卸载完成%s\n' "$C_GREEN" "$C_RESET"
printf '  管理界面：http://<服务器IP>:3001（Headscale Admin）\n'
[ "$KEEP_DATA" = "yes" ] && printf '  数据备份：%s 仍保留，确认无用后可手动删除\n' "$DB_DIR"
