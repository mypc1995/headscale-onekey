#!/usr/bin/env bash
#
# HeadControl 汉化补丁的只读验证脚本
# 用途：在临时目录拉取固定上游提交、应用补丁、编译，并确认中文资源生效
# 特点：不安装系统包、不写 /opt、不碰 systemd、不创建服务
#
# 用法: bash test-headcontrol-patch.sh
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PATCH_FILE="${SCRIPT_DIR}/headcontrol-zh.patch"
HC_REPO="https://github.com/ahmadzip/HeadControl.git"
HC_COMMIT="cfdfc63c766af3658851ef766bd84ab678fe005a"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

C_RESET=$'\033[0m'; C_GREEN=$'\033[1;32m'; C_RED=$'\033[1;31m'; C_YELLOW=$'\033[1;33m'
ok()   { printf '%s[PASS]%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
fail() { printf '%s[FAIL]%s %s\n' "$C_RED"    "$C_RESET" "$*"; FAILS=$((FAILS+1)); }
info() { printf '%s[INFO]%s %s\n' "$C_YELLOW" "$C_RESET" "$*"; }
FAILS=0

echo "=========================================="
echo " HeadControl 汉化补丁验证"
echo " 临时目录: $TMPDIR"
echo "=========================================="

info "检查依赖"
for c in git go gcc; do
  command -v "$c" >/dev/null 2>&1 || { fail "缺少依赖: $c"; exit 1; }
done
[[ -f "$PATCH_FILE" ]] || { fail "未找到补丁: $PATCH_FILE"; exit 1; }
ok "依赖与补丁文件就绪"

info "拉取上游固定提交 ${HC_COMMIT:0:12}"
git init -q "$TMPDIR/src" || { fail "git init 失败"; exit 1; }
git -C "$TMPDIR/src" remote add origin "$HC_REPO"
if git -C "$TMPDIR/src" fetch -q --depth 1 origin "$HC_COMMIT"; then
  git -C "$TMPDIR/src" checkout -q FETCH_HEAD || { fail "checkout 失败"; exit 1; }
  ok "源码就绪"
else
  fail "拉取失败，请检查网络或上游提交是否仍可访问"
  exit 1
fi

info "应用汉化补丁"
cd "$TMPDIR/src"
if git apply --check "$PATCH_FILE" && git apply "$PATCH_FILE"; then
  ok "补丁应用成功"
else
  fail "补丁无法应用（上游提交可能已变动）"
  exit 1
fi

info "编译验证（CGO + go-sqlite3）"
if go mod tidy >"$TMPDIR/tidy.log" 2>&1 && CGO_ENABLED=1 go build -o "$TMPDIR/headcontrol" . >"$TMPDIR/build.log" 2>&1; then
  ok "编译成功"
else
  fail "编译失败"
  tail -30 "$TMPDIR/tidy.log" "$TMPDIR/build.log" 2>/dev/null | sed 's/^/        /'
  exit 1
fi

info "检查中文资源"
grep -q '总览' templates/layout/layout.html && ok "导航已汉化" || fail "导航未汉化"
grep -q '用户总数' templates/pages/dashboard.html && ok "仪表盘已汉化" || fail "仪表盘未汉化"
grep -q '初始化设置' templates/pages/setup.html && ok "初始化页面已汉化" || fail "初始化页面未汉化"
grep -q '连接' internal/headscale/client.go && ok "后端提示已汉化" || fail "后端提示未汉化"

echo
echo "=========================================="
if [[ $FAILS -eq 0 ]]; then
  ok "全部检查通过"
else
  fail "有 $FAILS 项失败"
fi
echo "=========================================="
exit $FAILS
