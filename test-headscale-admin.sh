#!/usr/bin/env bash
#
# Headscale Admin 本地只读验证脚本
# 用途：编译检查 + 临时启动服务，验证登录、CSRF、双语、主题与网速面板
# 特点：所有文件在临时目录；使用假的 headscale 命令；不写 /etc、不启动 systemd
#
# 用法: bash test-headscale-admin.sh
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_ROOT="$SCRIPT_DIR"
[[ -f "$SRC_ROOT/main.go" ]] || SRC_ROOT="$SCRIPT_DIR/headscale-admin"
TMPDIR="$(mktemp -d)"
SERVER_PID=""
trap 'if [[ -n "$SERVER_PID" ]]; then kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; fi; rm -rf "$TMPDIR"' EXIT

C_RESET=$'\033[0m'; C_GREEN=$'\033[1;32m'; C_RED=$'\033[1;31m'; C_YELLOW=$'\033[1;33m'
ok()   { printf '%s[PASS]%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
fail() { printf '%s[FAIL]%s %s\n' "$C_RED"    "$C_RESET" "$*"; FAILS=$((FAILS+1)); }
info() { printf '%s[INFO]%s %s\n' "$C_YELLOW" "$C_RESET" "$*"; }
FAILS=0

echo "=========================================="
echo " Headscale Admin 验证（双语 + 新 UI）"
echo " 临时目录: $TMPDIR"
echo "=========================================="

info "检查依赖与源码"
for c in go curl; do
  command -v "$c" >/dev/null 2>&1 || { fail "缺少依赖: $c"; exit 1; }
done
[[ -f "$SRC_ROOT/main.go" ]] || { fail "未找到源码: $SRC_ROOT/main.go"; exit 1; }
ok "依赖与源码就绪"

info "编译与静态检查"
cd "$SRC_ROOT"
go vet ./... >"$TMPDIR/vet.log" 2>&1 || { fail "go vet 失败"; tail -20 "$TMPDIR/vet.log"; exit 1; }
go build -o "$TMPDIR/hsa" . >"$TMPDIR/build.log" 2>&1 || { fail "编译失败"; tail -30 "$TMPDIR/build.log"; exit 1; }
ok "go vet 与编译通过"

grep -q 'data-theme="dark"' "$SRC_ROOT/static/app.css" && ok "多主题样式已内置" || fail "缺少主题样式"
grep -q 'fetch(' "$SRC_ROOT/static/app.js" && ok "实时网速刷新脚本就绪" || fail "缺少网速刷新脚本"

cat >"$TMPDIR/fake-hs" <<'EOF'
#!/bin/sh
case "$*" in
  *"list-routes"*) echo '[]' ;;
  *"users list"*) echo '[{"id":1,"name":"default","created_at":{"seconds":1794889542,"nanos":0}}]' ;;
  *"nodes list"*) echo 'null' ;;
  *"preauthkeys list"*) echo '[]' ;;
  *"apikeys list"*) echo '[]' ;;
  *"version"*) echo 'headscale version v0.29.7' ;;
  *) echo '{}' ;;
esac
EOF
chmod +x "$TMPDIR/fake-hs"

info "初始化测试管理员"
HSA_DATA_FILE="$TMPDIR/admins.json" "$TMPDIR/hsa" --init-only --admin-user admin --admin-pass testpass123 >"$TMPDIR/init.log" 2>&1 || { fail "初始化失败"; cat "$TMPDIR/init.log"; exit 1; }
[[ -f "$TMPDIR/admins.json" ]] && ok "管理员文件已生成" || fail "管理员文件未生成"

PORT=$((39000 + RANDOM % 1000))
HSA_HEADSCALE_BIN="$TMPDIR/fake-hs" "$TMPDIR/hsa" \
  -port "$PORT" -data "$TMPDIR/admins.json" -templates "$SRC_ROOT/templates" -static "$SRC_ROOT/static" \
  >"$TMPDIR/server.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 40); do
  curl -s --max-time 1 "http://127.0.0.1:${PORT}/health" >/dev/null && break
  sleep 0.2
done
[[ "$(curl -s "http://127.0.0.1:${PORT}/health")" == "ok" ]] && ok "健康检查通过" || { fail "服务未启动"; tail -20 "$TMPDIR/server.log"; exit 1; }

BASE="http://127.0.0.1:${PORT}"
JAR="$TMPDIR/cookies"
info "验证登录与访问控制"
[[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/")" == "303" ]] && ok "未登录访问被重定向" || fail "未登录访问未重定向"
curl -s -X POST "$BASE/login" -d 'username=admin&password=wrong' | grep -q '用户名或密码错误' && ok "错误密码被拒绝" || fail "错误密码未被拒绝"
[[ "$(curl -s -c "$JAR" -X POST "$BASE/login" -d 'username=admin&password=testpass123' -o /dev/null -w '%{http_code}')" == "303" ]] && ok "正确凭据登录成功" || fail "登录失败"

info "验证中文页面与新 UI"
for p in / /nodes /users /keys /network /ops /admins; do
  code="$(curl -s -b "$JAR" -o "$TMPDIR/page.html" -w '%{http_code}' "$BASE$p")"
  if [[ "$code" == "200" ]] && grep -q 'class="topbar-title"' "$TMPDIR/page.html"; then
    ok "页面 $p 渲染正常"
  else
    fail "页面 $p 返回 ${code} 或缺少标题"
  fi
done
curl -s -b "$JAR" "$BASE/ops" | grep -q 'section.netmon\|实时网速' && ok "运维页包含实时网速面板" || fail "运维页缺少网速面板"
curl -s -b "$JAR" "$BASE/ops" | grep -q 'spark-down' && ok "网速曲线元素存在" || fail "网速曲线缺失"
curl -s -b "$JAR" "$BASE/api/netmon" | grep -q '"ifaces"' && ok "网速数据接口可用" || fail "网速数据接口异常"
curl -s -b "$JAR" "$BASE/" | grep -q 'id="theme-dropdown"' && ok "主题切换控件存在" || fail "主题切换控件缺失"

info "验证中英文切换"
curl -s -b "$JAR" -c "$JAR" "$BASE/lang?lang=en&next=/" -o /dev/null -w ''
curl -s -b "$JAR" "$BASE/" | grep -q 'Dashboard' && ok "英文界面标题生效" || fail "英文界面未生效"
curl -s -b "$JAR" "$BASE/ops" | grep -q 'Live network speed' && ok "英文网速文案生效" || fail "英文网速文案缺失"
curl -s -b "$JAR" "$BASE/users" | grep -q 'New user' && ok "英文表单文案生效" || fail "英文表单文案缺失"
curl -s -b "$JAR" -c "$JAR" "$BASE/lang?lang=zh&next=/" -o /dev/null
curl -s -b "$JAR" "$BASE/" | grep -q '总览' && ok "切回中文生效" || fail "切回中文失败"

info "验证 CSRF"
curl -s -b "$JAR" "$BASE/users" -o "$TMPDIR/users.html"
CSRF="$(grep -o 'name="csrf" value="[^"]*"' "$TMPDIR/users.html" | head -1 | sed 's/.*value="//;s/"//')"
[[ ${#CSRF} -ge 32 ]] && ok "CSRF token 已下发" || fail "未找到 CSRF token"
[[ "$(curl -s -b "$JAR" -X POST "$BASE/users/create" -d 'name=x' -o /dev/null -w '%{http_code}')" == "400" ]] && ok "缺少 CSRF 的请求被拒绝" || fail "缺少 CSRF 未被拒绝"
[[ "$(curl -s -b "$JAR" -X POST "$BASE/users/create" -d "csrf=${CSRF}&name=u1" -o /dev/null -w '%{http_code}')" == "303" ]] && ok "携带 CSRF 的请求通过" || fail "CSRF 校验异常"

kill "$SERVER_PID" 2>/dev/null || true
wait "$SERVER_PID" 2>/dev/null || true
SERVER_PID=""

echo
echo "=========================================="
if [[ $FAILS -eq 0 ]]; then
  ok "全部检查通过"
else
  fail "有 $FAILS 项失败"
fi
echo "=========================================="
exit $FAILS
