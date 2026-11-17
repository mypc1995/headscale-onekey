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
HSA_DATA_FILE="$TMPDIR/app.db" HSA_LEGACY_JSON="$TMPDIR/admins.json" "$TMPDIR/hsa" --init-only --admin-user admin --admin-pass testpass123 >"$TMPDIR/init.log" 2>&1 || { fail "初始化失败"; cat "$TMPDIR/init.log"; exit 1; }
[[ -s "$TMPDIR/app.db" ]] && ok "SQLite 数据库已生成" || fail "SQLite 数据库未生成"

cat >"$TMPDIR/config.yaml" <<'YAML'
server_url: http://127.0.0.1:8080
listen_addr: 0.0.0.0:8080
metrics_listen_addr: ""
grpc_listen_addr: 127.0.0.1:50443
prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
acme_url: https://acme-v02.api.letsencrypt.org/directory
acme_email: ""
tls_letsencrypt_hostname: ""
tls_letsencrypt_cache_dir: /var/lib/headscale/cache
tls_letsencrypt_challenge_type: HTTP-01
tls_letsencrypt_listen: ":http"
tls_cert_path: ""
tls_key_path: ""
dns:
  magic_dns: true
  base_domain: hs.example.com
policy:
  path: ""
YAML

PORT=$((39000 + RANDOM % 1000))
HSA_HEADSCALE_BIN="$TMPDIR/fake-hs" HSA_CONFIG_FILE="$TMPDIR/config.yaml" "$TMPDIR/hsa" \
  -port "$PORT" -data "$TMPDIR/app.db" -legacy-json "$TMPDIR/admins.json" -templates "$SRC_ROOT/templates" -static "$SRC_ROOT/static" \
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
curl -s -X POST "$BASE/login" -d 'username=admin&password=wrong' | grep -c '用户名或密码错误' >/dev/null && ok "错误密码被拒绝" || fail "错误密码未被拒绝"
[[ "$(curl -s -c "$JAR" -X POST "$BASE/login" -d 'username=admin&password=testpass123' -o /dev/null -w '%{http_code}')" == "303" ]] && ok "正确凭据登录成功" || fail "登录失败"

info "验证中文页面与新 UI"
for p in / /nodes /users /keys /network /settings /ops /admins; do
  code="$(curl -s -b "$JAR" -o "$TMPDIR/page.html" -w '%{http_code}' "$BASE$p")"
  if [[ "$code" == "200" ]] && grep -q 'class="topbar-title"' "$TMPDIR/page.html"; then
    ok "页面 $p 渲染正常"
  else
    fail "页面 $p 返回 ${code} 或缺少标题"
  fi
done
curl -s -b "$JAR" "$BASE/ops" | grep -c 'section.netmon\|实时网速' >/dev/null && ok "运维页包含实时网速面板" || fail "运维页缺少网速面板"
curl -s -b "$JAR" "$BASE/ops" | grep -c 'spark-down' >/dev/null && ok "网速曲线元素存在" || fail "网速曲线缺失"
curl -s -b "$JAR" "$BASE/api/netmon" | grep -c '"ifaces"' >/dev/null && ok "网速数据接口可用" || fail "网速数据接口异常"

info "验证本机资源与节点监控"
curl -s -b "$JAR" "$BASE/ops" -o "$TMPDIR/ops.html"
for kw in '本机资源' '节点监控' 'CPU 使用率' '内存使用率' '节点总数' '告警'; do
  grep -q "$kw" "$TMPDIR/ops.html" && ok "运维页包含「$kw」" || fail "运维页缺少「$kw」"
done
grep -q 'id="host-cpu-bar"' "$TMPDIR/ops.html" && ok "本机 CPU 进度条存在" || fail "缺少 CPU 进度条"
grep -q 'id="host-mem-bar"' "$TMPDIR/ops.html" && ok "本机内存进度条存在" || fail "缺少内存进度条"
grep -q 'id="node-rows"' "$TMPDIR/ops.html" && ok "节点监控表格存在" || fail "缺少节点监控表格"
curl -s -b "$JAR" "$BASE/api/monitor" -o "$TMPDIR/monitor.json"
for kw in '"host"' '"cpu_percent"' '"mem_percent"' '"nodes"' '"thresholds"' '"alerts"'; do
  grep -q "$kw" "$TMPDIR/monitor.json" && ok "监控接口包含 $kw" || fail "监控接口缺少 $kw"
done
if [[ "$(uname -s)" == "Linux" ]]; then
  grep -q '"available":true' "$TMPDIR/monitor.json" && ok "本机资源采集可用" || fail "本机资源采集不可用"
  grep -q '"cpu_count":[1-9]' "$TMPDIR/monitor.json" && ok "CPU 核数已采集" || fail "CPU 核数缺失"
  grep -q '"load1"' "$TMPDIR/monitor.json" && ok "系统负载已采集" || fail "系统负载缺失"
else
  info "非 Linux 环境没有 /proc，跳过本机资源采集断言"
fi
curl -s -b "$JAR" "$BASE/" | grep -c 'id="theme-dropdown"' >/dev/null && ok "主题切换控件存在" || fail "主题切换控件缺失"

info "验证 Headscale 配置页"
curl -s -b "$JAR" "$BASE/settings" -o "$TMPDIR/settings.html"
for kw in '域名与访问地址' '端口与网段' 'HTTPS 证书' '证书自动更新' '客户端连接端口' '手动粘贴已有证书'; do
  grep -q "$kw" "$TMPDIR/settings.html" && ok "配置页包含「$kw」" || fail "配置页缺少「$kw」"
done
grep -q 'class="key">listen_addr<' "$TMPDIR/settings.html" && ok "字段旁标注原始配置键" || fail "缺少原始配置键标注"
grep -q 'class="form-grid cols-3"' "$TMPDIR/settings.html" && ok "三个端口按三列排布" || fail "端口字段未使用三列布局"
grep -q 'class="check-line"' "$TMPDIR/settings.html" && ok "MagicDNS 为带说明的开关行" || fail "MagicDNS 未使用开关行"
grep -q '设备 IPv4 网段' "$TMPDIR/settings.html" && ok "网段字段使用通俗名称" || fail "网段字段名称未更新"
grep -q 'name="tls_cert_pem"' "$TMPDIR/settings.html" && ok "手动证书为粘贴文本框" || fail "手动证书不是文本框"
grep -q 'name="tls_cert_path"' "$TMPDIR/settings.html" && fail "手动证书仍在填写路径" || ok "手动证书不再要求填写路径"
grep -q 'value="http://127.0.0.1:8080"' "$TMPDIR/settings.html" && ok "配置页回显 server_url" || fail "配置页未回显 server_url"
CSRF="$(grep -o 'name="csrf" value="[^"]*"' "$TMPDIR/settings.html" | head -1 | sed 's/.*value="//;s/"//')"

curl -s -b "$JAR" -X POST "$BASE/settings/domain" \
  -d "csrf=${CSRF}&server_url=ftp://x&base_domain=hs.example.com&apply_now=" -o "$TMPDIR/e1.html"
grep -q '只支持 http:// 或 https://' "$TMPDIR/e1.html" && ok "非法协议被拦截" || fail "非法协议未被拦截"

curl -s -b "$JAR" -X POST "$BASE/settings/network" \
  -d "csrf=${CSRF}&listen_addr=0.0.0.0:8080&grpc_listen_addr=127.0.0.1:50443&metrics_listen_addr=&prefix_v4=10.0.0.0/8&prefix_v6=" -o "$TMPDIR/e2.html"
grep -q '100.64.0.0/10' "$TMPDIR/e2.html" && ok "越界 IPv4 段被拦截" || fail "越界 IPv4 段未被拦截"

curl -s -b "$JAR" -X POST "$BASE/settings/tls" \
  -d "csrf=${CSRF}&tls_mode=letsencrypt&tls_letsencrypt_hostname=&acme_email=bad&tls_letsencrypt_challenge_type=HTTP-01&tls_letsencrypt_listen=:http&tls_letsencrypt_cache_dir=/var/lib/headscale/cache" -o "$TMPDIR/e3.html"
grep -q '证书域名不能为空' "$TMPDIR/e3.html" && ok "缺少证书域名被拦截" || fail "缺少证书域名未被拦截"
grep -q '邮箱格式不正确' "$TMPDIR/e3.html" && ok "非法邮箱被拦截" || fail "非法邮箱未被拦截"

curl -s -b "$JAR" -X POST "$BASE/settings/tls" \
  --data-urlencode "csrf=${CSRF}" --data-urlencode "tls_mode=manual" \
  --data-urlencode "tls_cert_pem=这是一个证书" --data-urlencode "tls_key_pem=这是一个私钥" \
  -o "$TMPDIR/e4.html"
grep -q '不像 PEM 文本' "$TMPDIR/e4.html" && ok "非 PEM 的粘贴内容被拦截" || fail "非 PEM 内容未被拦截"

curl -s -b "$JAR" -X POST "$BASE/settings/tls" \
  --data-urlencode "csrf=${CSRF}" --data-urlencode "tls_mode=manual" \
  --data-urlencode "tls_cert_pem=-----BEGIN CERTIFICATE-----
MIIB
-----END CERTIFICATE-----" --data-urlencode "tls_key_pem=" -o "$TMPDIR/e5.html"
grep -q '成对粘贴' "$TMPDIR/e5.html" && ok "证书私钥缺一被拦截" || fail "缺一项未被拦截"

if [[ "$(uname -s)" == "Linux" ]]; then
  code="$(curl -s -b "$JAR" -X POST "$BASE/settings/domain" \
    -d "csrf=${CSRF}&server_url=http://127.0.0.1:8080&base_domain=hs.example.com&apply_now=" -o /dev/null -w '%{http_code}')"
  [[ "$code" == "303" ]] && ok "合法域名配置保存成功" || fail "合法配置保存失败 (${code})"
  grep -q '^server_url: http://127.0.0.1:8080' "$TMPDIR/config.yaml" && ok "配置已写入文件" || fail "配置未写入"
  ls "$TMPDIR"/config.yaml.bak.* >/dev/null 2>&1 && ok "原配置已备份" || fail "未生成备份"
else
  info "非 Linux 环境无法执行 headscale configtest，跳过写入链路（由远程测试覆盖）"
fi

info "验证中英文切换"
curl -sL -b "$JAR" -c "$JAR" "$BASE/lang?lang=en&next=/" -o /dev/null
curl -s -b "$JAR" "$BASE/" | grep -c 'Dashboard' >/dev/null && ok "英文界面标题生效" || fail "英文界面未生效"
curl -s -b "$JAR" "$BASE/ops" | grep -c 'Live network speed' >/dev/null && ok "英文网速文案生效" || fail "英文网速文案缺失"
curl -s -b "$JAR" "$BASE/ops" | grep -c 'Host resources' >/dev/null && ok "英文本机资源文案生效" || fail "英文本机资源文案缺失"
curl -s -b "$JAR" "$BASE/ops" | grep -c 'Node monitoring' >/dev/null && ok "英文节点监控文案生效" || fail "英文节点监控文案缺失"
curl -s -b "$JAR" "$BASE/users" | grep -c 'New user' >/dev/null && ok "英文表单文案生效" || fail "英文表单文案缺失"
curl -s -b "$JAR" "$BASE/settings" | grep -c 'Certificate auto-renewal' >/dev/null && ok "英文配置页生效" || fail "英文配置页缺失"
curl -sL -b "$JAR" -c "$JAR" "$BASE/lang?lang=zh&next=/" -o /dev/null
curl -s -b "$JAR" "$BASE/" | grep -c '总览' >/dev/null && ok "切回中文生效" || fail "切回中文失败"

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
