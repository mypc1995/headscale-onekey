#!/usr/bin/env bash
#
# 管理界面 HTTPS 实机全流程验证（待办 3「发布前必做」）
#
# 在隔离实例上跑完整条链路，**不碰生产服务**：
#   启用 → 重启生效 → Secure Cookie 行为 → 关闭 → 重启生效
#   → 证书失效回退 HTTP → 复用 headscale 手动证书
#
# 用法: bash test-headscale-admin-tls.sh [二进制路径]
#   默认用 /usr/local/bin/headscale-admin（线上已部署的二进制）
#
set -uo pipefail

BIN="${1:-/usr/local/bin/headscale-admin}"
WORK=/root/hsa-tls-verify
PORT=3015
PASS='TlsVerify-7n4q'
FAILS=0

ok()   { printf '[PASS] %s\n' "$*"; }
fail() { printf '[FAIL] %s\n' "$*"; FAILS=$((FAILS+1)); }
info() { printf '[INFO] %s\n' "$*"; }

[[ -x "$BIN" ]] || { echo "找不到二进制: $BIN"; exit 1; }
command -v curl >/dev/null || { echo "缺少 curl"; exit 1; }
command -v openssl >/dev/null || { echo "缺少 openssl"; exit 1; }

cleanup() {
  [[ -n "${PID:-}" ]] && kill "$PID" 2>/dev/null
  [[ -n "${UNIT:-}" ]] && systemctl stop "$UNIT" >/dev/null 2>&1
  rm -rf "$WORK" /etc/systemd/system/hsa-tls-verify.service
  systemctl daemon-reload >/dev/null 2>&1
  return 0
}
trap cleanup EXIT

rm -rf "$WORK"; mkdir -p "$WORK"

# ---- 测试证书 ----
openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -keyout "$WORK/admin.key" -out "$WORK/admin.crt" \
  -subj "/CN=hsa-tls-verify.local" >/dev/null 2>&1 \
  || { echo "生成测试证书失败"; exit 1; }
chmod 600 "$WORK/admin.key" "$WORK/admin.crt"

# ---- 生产配置副本（复用 headscale 手动证书的路径来源）----
cp /etc/headscale/config.yaml "$WORK/config.yaml"
chmod 600 "$WORK/config.yaml"

HSA_DATA_FILE="$WORK/app.db" HSA_LEGACY_JSON="$WORK/admins.json" \
  HSA_INIT_ADMIN="tlsverify:${PASS}" "$BIN" --init-only >"$WORK/init.log" 2>&1 \
  || { echo "初始化临时管理员失败"; cat "$WORK/init.log"; exit 1; }

# ---- 以 systemd 单元运行，单元名独立，避免 restartSelf 误重启生产服务 ----
UNIT=hsa-tls-verify
cat >/etc/systemd/system/${UNIT}.service <<EOF
[Unit]
Description=Headscale Admin TLS verify (isolated instance)
After=network.target
# 本脚本要连续重启 6 次；不关掉启动限流，第 6 次会撞上 start-limit-hit，
# 服务根本起不来，表现为「未监听 HTTPS」——那是 systemd 限流，不是被测逻辑坏了。
StartLimitIntervalSec=0

[Service]
Type=simple
User=root
WorkingDirectory=/opt/headscale-admin
Environment=HSA_PORT=${PORT}
Environment=HSA_DATA_FILE=${WORK}/app.db
Environment=HSA_LEGACY_JSON=${WORK}/admins.json
Environment=HSA_TEMPLATES=/opt/headscale-admin/templates
Environment=HSA_STATIC=/opt/headscale-admin/static
Environment=HSA_CONFIG_FILE=${WORK}/config.yaml
Environment=HSA_HEADSCALE_BIN=/usr/bin/headscale
Environment=HSA_SYSTEMD_UNIT=${UNIT}
ExecStart=${BIN}
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl reset-failed "$UNIT" >/dev/null 2>&1 || true
systemctl start "$UNIT" >/dev/null 2>&1

wait_up() {  # $1=协议 http/https
  local proto="$1" i
  for i in $(seq 1 40); do
    if [[ "$proto" == "https" ]]; then
      curl -skf -m 1 "https://127.0.0.1:${PORT}/health" >/dev/null 2>&1 && return 0
    else
      curl -sf -m 1 "http://127.0.0.1:${PORT}/health" >/dev/null 2>&1 && return 0
    fi
    sleep 0.25
  done
  return 1
}

# 重启隔离实例并确认单元真的起来了。
# systemctl restart 命中 systemd 启动限流时不会报错、只是静默不启动，
# 后面 wait_up 只会给出「未监听 HTTPS」这种看不出原因的信息，
# 所以这里先把失败状态清掉、再把「单元没起来」单独报出来。
restart_unit() {
  systemctl reset-failed "$UNIT" >/dev/null 2>&1 || true
  systemctl restart "$UNIT" >/dev/null 2>&1
  sleep 0.3
  if ! systemctl is-active --quiet "$UNIT"; then
    fail "systemd 单元未处于 active（可能命中 start-limit-hit）"
    journalctl -u "$UNIT" -n 20 --no-pager
    return 1
  fi
  return 0
}

# 等待日志里出现关键字。$2=起始 epoch 秒（只认本轮之后的日志）。
#
# 两个坑都要避开：
# ① 不能写成 `journalctl ... | grep -q 关键字`：grep 命中即退出会给 journalctl 发
#    SIGPIPE，管道退出码变成 141；脚本开了 pipefail，整条管道被判成失败——
#    同一条日志时对时错（实测复现过）。改成先把日志读进变量、再用 here-string 匹配。
# ② 窗口不能写成「最近 N 分钟」：同一个单元名会被多轮验证复用，历史日志里的
#    同一句话会把断言喂成假绿。必须按本轮的起始时间戳圈定。
wait_log() {  # $1=关键字 $2=起始 epoch 秒 $3=超时秒（默认 10）
  local pat="$1" since="$2" limit="${3:-10}" i text
  for i in $(seq 1 $((limit * 4))); do
    text="$(journalctl -u "$UNIT" --since "@${since}" --no-pager 2>/dev/null || true)"
    if grep -q "$pat" <<<"$text"; then return 0; fi
    sleep 0.25
  done
  return 1
}

login_cookie() {  # $1=协议；输出 Set-Cookie 头
  local proto="$1" jar="$WORK/cookies.txt" page="$WORK/login.html"
  rm -f "$jar"
  local scheme="http"; [[ "$proto" == "https" ]] && scheme="https"
  curl -sk -c "$jar" "${scheme}://127.0.0.1:${PORT}/login" -o "$page"
  local csrf
  csrf="$(grep -o 'name="csrf" value="[^"]*"' "$page" | head -1 | sed 's/.*value="//;s/"//')"
  curl -sk -D "$WORK/headers.txt" -b "$jar" -c "$jar" -X POST "${scheme}://127.0.0.1:${PORT}/login" \
    --data-urlencode "csrf=${csrf}" --data-urlencode "username=tlsverify" \
    --data-urlencode "password=${PASS}" -o /dev/null
  grep -i '^set-cookie:' "$WORK/headers.txt" | head -1
}

home_code() {  # $1=协议；用 cookie 访问首页，返回状态码
  local proto="$1" scheme="http"; [[ "$proto" == "https" ]] && scheme="https"
  curl -sk -b "$WORK/cookies.txt" -o /dev/null -w '%{http_code}' "${scheme}://127.0.0.1:${PORT}/"
}

write_tls_json() {  # $1=enabled $2=cert $3=key
  cat >"$WORK/admin-tls.json" <<EOF
{
  "enabled": $1,
  "cert_path": "$2",
  "key_path": "$3"
}
EOF
  chmod 600 "$WORK/admin-tls.json"
}

echo "=========================================="
echo " 管理界面 HTTPS 实机全流程验证"
echo " 二进制: $BIN"
echo " 隔离实例: 端口 ${PORT}，systemd 单元 ${UNIT}"
echo "=========================================="

# ---------- 1. 初始状态：HTTP ----------
echo
info "1/6 初始状态（未启用 HTTPS）"
wait_up http || { echo "实例未启动"; journalctl -u "$UNIT" -n 20 --no-pager; exit 1; }
ok "服务以 HTTP 启动"
COOKIE="$(login_cookie http)"
[[ "$COOKIE" == *"Secure"* ]] && fail "HTTP 下 Cookie 不应带 Secure" || ok "HTTP 下 Cookie 不带 Secure"
[[ "$(home_code http)" == "200" ]] && ok "HTTP 下登录态可用" || fail "HTTP 下登录态不可用"

# ---------- 2. 启用 HTTPS 并重启 ----------
echo
info "2/6 启用 HTTPS（写入 admin-tls.json 后重启生效）"
write_tls_json true "$WORK/admin.crt" "$WORK/admin.key"
restart_unit
if wait_up https; then ok "重启后已监听 HTTPS"; else fail "重启后未监听 HTTPS"; fi
curl -sf -m 2 "http://127.0.0.1:${PORT}/health" >/dev/null 2>&1 \
  && fail "启用后 HTTP 端口不应再响应" || ok "启用后 HTTP 端口不再响应"
COOKIE="$(login_cookie https)"
[[ "$COOKIE" == *"Secure"* ]] && ok "HTTPS 下 Cookie 带 Secure" || fail "HTTPS 下 Cookie 应带 Secure"
[[ "$(home_code https)" == "200" ]] && ok "HTTPS 下登录态可用" || fail "HTTPS 下登录态不可用"

# ---------- 3. 证书失效 → 回退 HTTP ----------
echo
info "3/6 证书失效（证书文件被删）→ 应回退 HTTP"
# 起点取在重启之前（留 2 秒余量防同秒截断），只认本轮之后的日志。
LOG_SINCE="$(( $(date +%s) - 2 ))"
mv "$WORK/admin.crt" "$WORK/admin.crt.bak"
restart_unit
wait_up http || { echo "回退后实例未启动"; journalctl -u "$UNIT" -n 20 --no-pager; exit 1; }
wait_log '回退为 HTTP' "$LOG_SINCE" && ok "日志记录了「回退为 HTTP」" || fail "日志没有回退记录"
COOKIE="$(login_cookie http)"
[[ "$COOKIE" == *"Secure"* ]] \
  && fail "回退 HTTP 后 Cookie 仍带 Secure —— 明文下浏览器不发送该 Cookie，登录态必然失效" \
  || ok "回退 HTTP 后 Cookie 不带 Secure"
[[ "$(home_code http)" == "200" ]] \
  && ok "回退 HTTP 后登录态可用（安全阀有效）" \
  || fail "回退 HTTP 后登录态不可用 —— 安全阀失效，运维入口被锁死"
mv "$WORK/admin.crt.bak" "$WORK/admin.crt"

# ---------- 4. 证书恢复 → 回到 HTTPS ----------
echo
info "4/6 证书恢复 → 应回到 HTTPS"
restart_unit
wait_up https && ok "证书恢复后重新监听 HTTPS" || fail "证书恢复后未监听 HTTPS"

# ---------- 5. 关闭 HTTPS ----------
echo
info "5/6 关闭 HTTPS"
write_tls_json false "" ""
restart_unit
wait_up http && ok "关闭后回到 HTTP" || fail "关闭后未回到 HTTP"
COOKIE="$(login_cookie http)"
[[ "$COOKIE" == *"Secure"* ]] && fail "关闭后 Cookie 不应带 Secure" || ok "关闭后 Cookie 不带 Secure"
[[ "$(home_code http)" == "200" ]] && ok "关闭后登录态可用" || fail "关闭后登录态不可用"

# ---------- 6. reuse 分支：复用 headscale 手动证书 ----------
echo
info "6/6 复用 headscale 手动证书（reuse 分支）"
CERT_PATH="$(grep -E '^tls_cert_path:' "$WORK/config.yaml" | head -1 | sed 's/^tls_cert_path:[[:space:]]*//;s/^"//;s/"$//')"
KEY_PATH="$(grep -E '^tls_key_path:' "$WORK/config.yaml" | head -1 | sed 's/^tls_key_path:[[:space:]]*//;s/^"//;s/"$//')"
if [[ -n "$CERT_PATH" && -s "$CERT_PATH" && -n "$KEY_PATH" && -s "$KEY_PATH" ]]; then
  write_tls_json true "$CERT_PATH" "$KEY_PATH"
  restart_unit
  wait_up https && ok "复用 headscale 证书后监听 HTTPS" || fail "复用 headscale 证书后未监听 HTTPS"
  # 光「监听了 HTTPS」不够：还要确认对外提供的就是 headscale 那张手动证书。
  served_fp="$(echo | openssl s_client -connect "127.0.0.1:${PORT}" 2>/dev/null | openssl x509 -noout -fingerprint -sha256 2>/dev/null | sed 's/.*=//')"
  expect_fp="$(openssl x509 -in "$CERT_PATH" -noout -fingerprint -sha256 2>/dev/null | sed 's/.*=//')"
  [[ -n "$served_fp" && "$served_fp" == "$expect_fp" ]] \
    && ok "对外提供的正是 headscale 手动证书（指纹一致）" \
    || fail "对外证书与 headscale 手动证书不一致：served=${served_fp:-空} expect=${expect_fp:-空}"
else
  info "生产配置里没有可用的手动证书（tls_cert_path=${CERT_PATH:-空}），跳过 reuse 分支"
fi

echo
echo "=========================================="
if [[ "$FAILS" -eq 0 ]]; then
  echo " 管理界面 HTTPS 全流程验证：全部通过"
  echo "=========================================="
else
  echo " 管理界面 HTTPS 全流程验证：${FAILS} 项失败"
  echo "=========================================="
fi
exit "$FAILS"
