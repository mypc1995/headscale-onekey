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
# 部署版验证：HSA_DEPLOY_BIN 指向线上真实二进制，HSA_DEPLOY_ASSETS 指向线上
# 真实资源目录（如 /opt/headscale-admin）。此时不编译、不需要 Go，
# 验的就是线上正在跑的那一份，而不是本地重新编出来的副本。
DEPLOY_MODE=0
if [[ -n "${HSA_DEPLOY_BIN:-}" ]]; then
  DEPLOY_MODE=1
  SRC_ROOT="${HSA_DEPLOY_ASSETS:-$SCRIPT_DIR}"
elif [[ ! -f "$SRC_ROOT/main.go" ]]; then
  SRC_ROOT="$SCRIPT_DIR/headscale-admin"
fi
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
if [[ "$DEPLOY_MODE" == "1" ]]; then
  command -v curl >/dev/null 2>&1 || { fail "缺少依赖: curl"; exit 1; }
  [[ -x "$HSA_DEPLOY_BIN" ]] || { fail "线上二进制不存在或不可执行: $HSA_DEPLOY_BIN"; exit 1; }
  [[ -d "$SRC_ROOT/templates" ]] || { fail "线上资源目录缺失: $SRC_ROOT/templates"; exit 1; }
  ok "部署版模式：使用线上二进制 $HSA_DEPLOY_BIN 与线上资源 $SRC_ROOT"
else
  for c in go curl; do
    command -v "$c" >/dev/null 2>&1 || { fail "缺少依赖: $c"; exit 1; }
  done
  [[ -f "$SRC_ROOT/main.go" ]] || { fail "未找到源码: $SRC_ROOT/main.go"; exit 1; }
  ok "依赖与源码就绪"
fi

info "编译与静态检查"
if [[ "$DEPLOY_MODE" == "1" ]]; then
  # 不在验证机上重新编译：用的就是线上那一份二进制。
  cp "$HSA_DEPLOY_BIN" "$TMPDIR/hsa"
  ok "已复制线上二进制（不在本地重新编译）"
else
  cd "$SRC_ROOT"
  go vet ./... >"$TMPDIR/vet.log" 2>&1 || { fail "go vet 失败"; tail -20 "$TMPDIR/vet.log"; exit 1; }
  go build -o "$TMPDIR/hsa" . >"$TMPDIR/build.log" 2>&1 || { fail "编译失败"; tail -30 "$TMPDIR/build.log"; exit 1; }
  ok "go vet 与编译通过"
fi

grep -q 'data-theme="dark"' "$SRC_ROOT/static/app.css" && ok "多主题样式已内置" || fail "缺少主题样式"
grep -q 'fetch(' "$SRC_ROOT/static/app.js" && ok "实时网速刷新脚本就绪" || fail "缺少网速刷新脚本"

cat >"$TMPDIR/fake-hs" <<'EOF'
#!/bin/sh
# 从参数里取 -c / -f 后面的路径值
argval() {
  want="$1"; shift
  prev=""
  for a in "$@"; do
    if [ "$prev" = "$want" ]; then printf '%s' "$a"; return 0; fi
    prev="$a"
  done
  return 1
}
# 记录每次调用的参数：断言 CLI 到底收到了什么（路由审批要校验 -r 的取值）。
if [ -n "${FAKE_CMD_LOG:-}" ]; then
  printf '%s\n' "$*" >>"$FAKE_CMD_LOG"
fi
case "$*" in
  *"list-routes"*) echo '[]' ;;
  *"users list"*)
    # 卡死模式：标记文件存在时，users list 一直不返回。
    # 用来验证「headscale 卡住时管理界面不会跟着挂死」。
    if [ -n "${FAKE_HANG_USERS:-}" ] && [ -f "$FAKE_HANG_USERS" ]; then
      sleep 30
    fi
    echo '[{"id":1,"name":"default","created_at":{"seconds":1794889542,"nanos":0}}]' ;;
  *"nodes list"*) [ -s "$FAKE_NODES" ] && cat "$FAKE_NODES" || echo 'null' ;;
  *"preauthkeys list"*) [ -s "$FAKE_STATE" ] && cat "$FAKE_STATE" || echo '[]' ;;
  *"preauthkeys create"*)
    echo '[{"id":5,"user":{"name":"default"},"key":"hskey-test-ab12-***","reusable":true,"expiration":{"seconds":1894975942,"nanos":0},"created_at":{"seconds":1794889542,"nanos":0}}]' > "$FAKE_STATE"
    echo '{"id":"5","key":"hskey-test-123"}' ;;
  *"apikeys list"*) echo '[]' ;;
  *"apikeys create"*) echo '"hsapi-test-456"' ;;
  *"version"*) echo 'headscale version v0.29.7' ;;
  # 模拟 headscale configtest：文件不存在或缺少 server_url 就报错退出 1。
  # 真 headscale 也是这个行为——配置校验不过就不会被接受。
  *"configtest"*)
    f="$(argval -c "$@")"
    if [ -z "$f" ] || [ ! -f "$f" ]; then echo "config file not found" >&2; exit 1; fi
    if ! grep -q '^server_url:' "$f"; then echo "invalid config: missing server_url" >&2; exit 1; fi
    echo "Configuration is valid" ;;
  # 模拟 headscale policy check：策略必须是一条完整的规则。
  *"policy check"*)
    f="$(argval -f "$@")"
    if [ -z "$f" ] || [ ! -f "$f" ]; then echo "policy file not found" >&2; exit 1; fi
    if ! grep -q '"action"' "$f" || ! grep -q '}]' "$f"; then echo "invalid policy" >&2; exit 1; fi
    echo "Policy is valid" ;;
  *) echo '{}' ;;
esac
EOF
chmod +x "$TMPDIR/fake-hs"

# 假 headscale 的调用方式：Linux 下直接跑这个 sh 脚本；Windows 下 Go 的 exec
# 不能执行没有扩展名的文件（会直接报 exec 错误），列表类命令会全部失败、
# 页面拿不到任何数据，所以额外放一个 .cmd 转发给 sh。
HS_BIN="$TMPDIR/fake-hs"
if [[ "$(uname -s)" != "Linux" ]]; then
  HS_BIN="$TMPDIR/fake-hs.cmd"
  printf '@echo off\r\ncd /d "%%~dp0"\r\n"%s" fake-hs %%*\r\n' "$(cygpath -w "$(command -v sh)")" >"$HS_BIN"
fi

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

  # Strategy used for allocation of IPs to nodes.
  allocation: sequential
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
  # 故意放一个与 prefixes 块同名的键：块定位一旦越界（正则加了 (?s) 让 . 匹配换行），
  # 写 v6 会改到这里、删 v6 会连这里一起删掉，而 configtest 完全看不出来。
  v6: keepme-under-dns
policy:
  path: ""
YAML

# 给策略文件配一个真实路径：ACL 的「校验通过才落盘」链路要靠它。
# 注意路径形态：写进配置里的路径不会被 MSYS 转成 Windows 形式，
# Go 在 Windows 上会把 /tmp/xxx 当成 C:\tmp\xxx，跟脚本看到的不是同一个目录。
# 所以这里统一用 Windows 原生的正斜杠路径（cygpath -m），Linux 下保持原样。
POLICY_DIR="$TMPDIR"
if [[ "$(uname -s)" != "Linux" ]]; then POLICY_DIR="$(cygpath -m "$TMPDIR")"; fi
POLICY_PATH="${POLICY_DIR}/policy.hujson"
# 用 awk 而不是 sed：配置内容里带 / 的地址很多，sed 的分隔符会被内容截断。
awk -v p="$POLICY_PATH" '
  /^policy:/{print; print "  path: \"" p "\""; skip=1; next}
  skip==1 && /^  path:/{next}
  {print}
' "$TMPDIR/config.yaml" >"$TMPDIR/config.yaml.new" && mv "$TMPDIR/config.yaml.new" "$TMPDIR/config.yaml"
printf '%s\n' '{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}' >"$TMPDIR/policy.hujson"

PORT=$((39000 + RANDOM % 1000))
: >"$TMPDIR/hs-calls.log"
HSA_HEADSCALE_BIN="$HS_BIN" HSA_CONFIG_FILE="$TMPDIR/config.yaml" FAKE_STATE="$TMPDIR/preauth-state.json" FAKE_NODES="$TMPDIR/nodes.json" FAKE_CMD_LOG="$TMPDIR/hs-calls.log" FAKE_HANG_USERS="$TMPDIR/hang-users" HSA_HS_CALL_TIMEOUT=3 "$TMPDIR/hsa" \
  -port "$PORT" -data "$TMPDIR/app.db" -legacy-json "$TMPDIR/admins.json" -templates "$SRC_ROOT/templates" -static "$SRC_ROOT/static" \
  -node-speed-token test-speed-token \
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

info "验证图形验证码"
# 回环来源（本机 e2e、单机部署、运维脚本）不该被验证码拦住。
curl -s "$BASE/login" | grep -q 'name="captcha"' && fail "回环来源的登录页仍要求验证码" || ok "回环来源免验证码"
# 同机反代的公网请求：来源还是回环，但 Host 是公网域名——这种必须有验证码，
# 否则一部署反代，验证码就形同虚设。
curl -s -H 'Host: ts.example.com' "$BASE/login" | grep -q 'name="captcha"' && ok "反代公网请求仍要求验证码" || fail "反代公网请求未要求验证码"
# 验证码接口：返回 PNG，并下发令牌 Cookie。
cap_code="$(curl -s -D "$TMPDIR/cap.head" -o "$TMPDIR/cap.png" -c "$TMPDIR/cap.jar" -w '%{http_code}' "$BASE/login/captcha.png")"
[[ "$cap_code" == "200" ]] && ok "验证码图片接口返回 200" || fail "验证码图片接口返回 ${cap_code}"
grep -qi 'content-type: image/png' "$TMPDIR/cap.head" && ok "验证码图片类型为 image/png" || fail "验证码图片类型不对"
grep -q 'hsa_cap' "$TMPDIR/cap.jar" && ok "验证码令牌 Cookie 已下发" || fail "验证码令牌 Cookie 未下发"
head -c 4 "$TMPDIR/cap.png" | od -An -tx1 | tr -d ' \n' | grep -q '89504e47' && ok "验证码是合法 PNG" || fail "验证码不是合法 PNG"
# 缺验证码的登录必须被拒（用独立用户名，不去扰动 admin 账号的失败计数）。
code="$(curl -s -o "$TMPDIR/cap-login.html" -w '%{http_code}' -H 'Host: ts.example.com' -X POST "$BASE/login" \
  -d 'username=captcha-probe&password=testpass123')"
[[ "$code" != "303" ]] && ok "缺验证码的登录被拒（${code}）" || fail "缺验证码的登录被放行"
grep -q '验证码错误' "$TMPDIR/cap-login.html" && ok "缺验证码时提示验证码错误" || fail "缺验证码时未提示验证码错误"
# 静态资源必须每次回源校验：样式和脚本的 URL 不带版本号，浏览器会拿启发式缓存
# 把旧样式留上十几个小时，上线新版样式后用户看到的还是旧界面。
curl -s -D "$TMPDIR/static.head" -o /dev/null "$BASE/static/app.css"
grep -qi 'cache-control: no-cache' "$TMPDIR/static.head" && ok "静态资源不长期缓存" || fail "静态资源缓存策略不对"
# 静态资源 URL 要带版本号：老浏览器已经在缓存里的旧样式，URL 不变就一直用旧的。
curl -s "$BASE/login" | grep -q 'app.css?v=' && ok "静态资源 URL 带版本号" || fail "静态资源 URL 没有版本号"
# 登录页必须加载自己的脚本：验证码「点图换一张」的逻辑在 login.js 里。
# 之前这段逻辑放在 app.js，而登录页压根没加载 app.js，点图片毫无反应。
curl -s "$BASE/login" | grep -q 'login.js?v=' && ok "登录页加载了专用脚本" || fail "登录页没有加载专用脚本"
curl -s "$BASE/login" | grep -q 'app.js?v=' && fail "登录页仍在加载整包 app.js" || ok "登录页不加载整包 app.js"

info "验证安全响应头"
curl -s -D "$TMPDIR/hdr.txt" -o /dev/null "$BASE/login"
grep -qi '^x-frame-options: DENY' "$TMPDIR/hdr.txt" && ok "已设置 X-Frame-Options（防点击劫持）" || fail "缺少 X-Frame-Options"
grep -qi '^x-content-type-options: nosniff' "$TMPDIR/hdr.txt" && ok "已设置 nosniff" || fail "缺少 nosniff"
grep -qi '^referrer-policy: same-origin' "$TMPDIR/hdr.txt" && ok "已设置 Referrer-Policy" || fail "缺少 Referrer-Policy"
grep -qi "content-security-policy:.*default-src 'self'" "$TMPDIR/hdr.txt" && ok "已设置 CSP" || fail "缺少 CSP"
grep -qi "content-security-policy:.*script-src 'self'" "$TMPDIR/hdr.txt" && ok "CSP 禁止内联脚本" || fail "CSP 的 script-src 不对"
# CSP 不放开内联脚本：模板里出现内联脚本会被浏览器直接挡掉，功能静默失效。
curl -s -b "$JAR" "$BASE/settings" -o "$TMPDIR/settings2.html"
grep -q '<script>' "$TMPDIR/settings2.html" && fail "页面里有内联脚本（CSP 会挡掉）" || ok "页面里没有内联脚本"

info "验证总览页不再反复起 headscale 进程"
# 总览页原本每次刷新要起 5 个 headscale 进程（version/cert/users/nodes/keys）。
# 现在计数走 5 秒缓存、版本与证书走长缓存，重复刷新不该再起进程。
curl -s -b "$JAR" "$BASE/" -o /dev/null
: >"$TMPDIR/hs-calls.log"
for _ in 1 2 3 4 5; do
  curl -s -b "$JAR" "$BASE/" -o /dev/null
done
calls="$(wc -l <"$TMPDIR/hs-calls.log" | tr -d ' ')"
# 允许 1 次：节点监控每 15 秒自己采一次，可能正好落在窗口里。
[[ "$calls" -le 1 ]] && ok "连续 5 次刷新总览页只起了 ${calls} 个 headscale 进程" || fail "连续 5 次刷新起了 ${calls} 个 headscale 进程（缓存没生效）"

info "验证 headscale 卡死时界面不挂死"
# headscale 卡住时，管理界面必须在超时内返回，不能一直挂着。
# 这条守着「一次卡死把整个界面拖死」这个故障：旧实现没有超时，
# 而且卡住的那个调用把闸门一直攥着，后面所有页面全堵死。
: >"$TMPDIR/hang-users"
code="$(curl -s --max-time 20 -o "$TMPDIR/hang.html" -w '%{http_code}' -b "$JAR" "$BASE/users")"
rm -f "$TMPDIR/hang-users"
[[ "$code" == "200" ]] && ok "headscale 卡死时页面仍然返回（${code}）" || fail "headscale 卡死时页面没返回（${code}）"
# 超时之后闸门必须归还：下一次请求要能正常拿到数据。
curl -s --max-time 10 -b "$JAR" "$BASE/users" | grep -q 'default' && ok "卡死超时后闸门已归还，页面恢复正常" || fail "卡死超时后闸门没归还，页面仍异常"

info "验证跳转目标与登录限速"
# 语言切换的跳转目标必须是站内地址：/\evil.com 会被浏览器归一化成 //evil.com。
loc="$(curl -s -o /dev/null -w '%{redirect_url}' "$BASE/lang?lang=zh&next=/%5Cevil.com")"
case "$loc" in
  *evil.com*) fail "语言切换存在开放重定向: $loc" ;;
  *) ok "语言切换的跳转目标已校验" ;;
esac
# 连续失败要被计数，但正确口令仍然能登录（账号维度只加延迟，不做永久锁定）。
for _ in 1 2 3; do
  curl -s -o /dev/null -X POST "$BASE/login" -d 'username=bogus-user&password=wrong'
done
[[ "$(curl -s -c "$JAR" -X POST "$BASE/login" -d 'username=admin&password=testpass123' -o /dev/null -w '%{http_code}')" == "303" ]] && ok "失败尝试后正确口令仍可登录" || fail "失败尝试后无法登录"

info "验证登录防滥用"
# 请求体上限：登录表单正常不到 1KB，超过 64KB 一律拒掉，不让它被读进内存。
# 大 body 必须走文件：200KB 塞进命令行参数会超出 Windows 的命令行长度上限
# （实测报 "Argument list too long"，curl 根本没起来）。
head -c 200000 /dev/zero | tr '\0' 'x' >"$TMPDIR/big-body.bin"
code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/login" \
  -H 'Content-Type: application/x-www-form-urlencoded' --data-binary "@$TMPDIR/big-body.bin")"
[[ "$code" == "413" ]] && ok "超大登录请求体被拒（413）" || fail "超大登录请求体未被拒（${code}）"
# 正常大小的表单不能被误拒。
code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/login" \
  --data-urlencode 'username=admin' --data-urlencode 'password=short')"
[[ "$code" != "413" ]] && ok "正常大小的登录表单未被误拒" || fail "正常大小的登录表单被误拒"
# 并发登录：口令校验有并发闸门（防止几十个并发把 CPU 打满），
# 但闸门不能把正常登录也拒掉。
# 注意：这里不能写裸 wait——那会把后台的服务进程也一起等住，脚本永远不结束。
# 必须只等这几个 curl 子进程。
: >"$TMPDIR/concurrent.txt"
cpids=""
for _ in $(seq 1 8); do
  curl -s -o /dev/null -w '%{http_code}\n' -X POST "$BASE/login" \
    -d 'username=admin&password=testpass123' >>"$TMPDIR/concurrent.txt" &
  cpids="$cpids $!"
done
for p in $cpids; do wait "$p"; done
bad="$(grep -c -v '^303$' "$TMPDIR/concurrent.txt" | tr -d ' ')"
[[ "$bad" == "0" ]] && ok "8 个并发登录全部成功" || fail "并发登录有 ${bad} 个未成功"

info "验证只读账号与撤权生效"
CSRF="$(curl -s -b "$JAR" "$BASE/admins" | grep -o 'name="csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')"
[[ ${#CSRF} -ge 32 ]] && ok "管理员页 CSRF 已下发" || fail "管理员页缺少 CSRF"
# 短口令必须被拒（创建路径要和改密路径用同一套长度要求）。
[[ "$(curl -s -b "$JAR" -X POST "$BASE/admins/create" -d "csrf=${CSRF}&username=weakpwd&password=short&role=readonly" -o /dev/null -w '%{http_code}')" == "303" ]] && ok "创建管理员的短口令被拦下" || fail "短口令未被拦下"
curl -s -b "$JAR" "$BASE/admins" | grep -c 'weakpwd' >/dev/null && fail "短口令账号仍被创建" || ok "短口令账号未被创建"
# 建一个只读账号并登录。
curl -s -b "$JAR" -X POST "$BASE/admins/create" -d "csrf=${CSRF}&username=ro-user&password=readonly123&role=readonly" -o /dev/null
RO_JAR="$TMPDIR/ro-cookies"
[[ "$(curl -s -c "$RO_JAR" -X POST "$BASE/login" -d 'username=ro-user&password=readonly123' -o /dev/null -w '%{http_code}')" == "303" ]] && ok "只读账号登录成功" || fail "只读账号登录失败"
[[ "$(curl -s -b "$RO_JAR" -o /dev/null -w '%{http_code}' "$BASE/admins")" == "200" ]] && ok "只读账号可以访问管理页" || fail "只读账号无法访问管理页"
# 只读账号的写操作要被拦。
RO_CSRF="$(curl -s -b "$RO_JAR" "$BASE/admins" | grep -o 'name="csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')"
[[ "$(curl -s -b "$RO_JAR" -X POST "$BASE/admins/create" -d "csrf=${RO_CSRF}&username=ro-made&password=readonly123&role=admin" -o /dev/null -w '%{http_code}')" == "303" ]] && ok "只读账号的写操作被拦下" || fail "只读账号的写操作未被拦下"
curl -s -b "$RO_JAR" "$BASE/admins" | grep -c 'ro-made' >/dev/null && fail "只读账号仍然创建出了管理员" || ok "只读账号没有创建出管理员"
# 只读账号必须能登出（登出只校验 CSRF，不该被角色判定挡住）。
[[ "$(curl -s -b "$RO_JAR" -c "$RO_JAR" -X POST "$BASE/logout" -d "csrf=${RO_CSRF}" -o /dev/null -w '%{http_code}')" == "303" ]] && ok "只读账号可以登出" || fail "只读账号无法登出"
[[ "$(curl -s -b "$RO_JAR" -o /dev/null -w '%{http_code}' "$BASE/")" == "303" ]] && ok "登出后会话已失效" || fail "登出后会话仍然有效"
# 降权必须立刻生效：被降为只读的账号，手里的旧会话不能再写。
curl -s -b "$JAR" -X POST "$BASE/admins/create" -d "csrf=${CSRF}&username=demote-user&password=demote123&role=admin" -o /dev/null
DEMOTE_JAR="$TMPDIR/demote-cookies"
curl -s -c "$DEMOTE_JAR" -X POST "$BASE/login" -d 'username=demote-user&password=demote123' -o /dev/null
DEMOTE_CSRF="$(curl -s -b "$DEMOTE_JAR" "$BASE/admins" | grep -o 'name="csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')"
[[ "$(curl -s -b "$DEMOTE_JAR" -X POST "$BASE/admins/create" -d "csrf=${DEMOTE_CSRF}&username=before-demote&password=demote123&role=readonly" -o /dev/null -w '%{http_code}')" == "303" ]] && ok "降权前该账号是管理员" || fail "降权前该账号无法写入"
curl -s -b "$JAR" -X POST "$BASE/admins/role" -d "csrf=${CSRF}&username=demote-user&role=readonly" -o /dev/null
[[ "$(curl -s -b "$DEMOTE_JAR" -o /dev/null -w '%{http_code}' "$BASE/")" == "303" ]] && ok "降权后旧会话立即失效" || fail "降权后旧会话仍然有效"

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
if [[ "$(uname -s)" == "Linux" ]]; then
  for kw in 'CPU 使用率' '内存使用率'; do
    grep -q "$kw" "$TMPDIR/ops.html" && ok "运维页包含「$kw」" || fail "运维页缺少「$kw」"
  done
  grep -q 'id="host-cpu-bar"' "$TMPDIR/ops.html" && ok "本机 CPU 进度条存在" || fail "缺少 CPU 进度条"
  grep -q 'id="host-mem-bar"' "$TMPDIR/ops.html" && ok "本机内存进度条存在" || fail "缺少内存进度条"
else
  info "非 Linux 环境没有 /proc，跳过本机资源面板断言（页面显示不可用占位，属预期）"
fi
for kw in '本机资源' '节点监控' '节点总数' '告警'; do
  grep -q "$kw" "$TMPDIR/ops.html" && ok "运维页包含「$kw」" || fail "运维页缺少「$kw」"
done
grep -q 'id="node-rows"' "$TMPDIR/ops.html" && ok "节点监控表格存在" || fail "缺少节点监控表格"
grep -q '实时速度' "$TMPDIR/ops.html" && ok "节点监控包含实时速度列" || fail "缺少实时速度列"
grep -q 'id="node-updated"' "$TMPDIR/ops.html" && ok "节点监控标注数据更新时间" || fail "缺少数据更新时间"
grep -q 'nodeMon.speedNA\|控制面拿不到节点流量' "$TMPDIR/ops.html" && ok "网速不可用时说明文案就绪" || fail "缺少网速不可用说明"
curl -s -b "$JAR" "$BASE/api/monitor" -o "$TMPDIR/monitor.json"
for kw in '"host"' '"cpu_percent"' '"mem_percent"' '"nodes"' '"thresholds"' '"alerts"' '"speed_source"'; do
  grep -q "$kw" "$TMPDIR/monitor.json" && ok "监控接口包含 $kw" || fail "监控接口缺少 $kw"
done
grep -q '"speed_source":false' "$TMPDIR/monitor.json" && ok "未配置网速源时明确标记不可用" || fail "网速源状态标记异常"
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
for kw in '域名与访问地址' '端口与网段' 'HTTPS 证书' '证书自动更新' '客户端连接端口' '手动上传已有证书'; do
  grep -q "$kw" "$TMPDIR/settings.html" && ok "配置页包含「$kw」" || fail "配置页缺少「$kw」"
done
grep -q 'class="key">listen_addr<' "$TMPDIR/settings.html" && ok "字段旁标注原始配置键" || fail "缺少原始配置键标注"
grep -q 'class="form-grid cols-3"' "$TMPDIR/settings.html" && ok "三个端口按三列排布" || fail "端口字段未使用三列布局"
grep -q 'class="check-line"' "$TMPDIR/settings.html" && ok "MagicDNS 为带说明的开关行" || fail "MagicDNS 未使用开关行"
grep -q '设备 IPv4 网段' "$TMPDIR/settings.html" && ok "网段字段使用通俗名称" || fail "网段字段名称未更新"
grep -q 'name="tls_cert_pem"' "$TMPDIR/settings.html" && fail "手动证书仍留着粘贴文本框" || ok "手动证书不再提供粘贴文本框"
grep -q 'name="tls_cert_file"' "$TMPDIR/settings.html" && ok "手动证书支持上传文件" || fail "手动证书没有上传入口"
grep -q 'name="tls_chain_file"' "$TMPDIR/settings.html" && ok "手动证书有中间证书上传位" || fail "手动证书缺中间证书上传位"
grep -q 'name="tls_key_file"' "$TMPDIR/settings.html" && ok "手动证书有私钥上传位" || fail "手动证书缺私钥上传位"
grep -q 'enctype="multipart/form-data"' "$TMPDIR/settings.html" && ok "证书表单按 multipart 提交" || fail "证书表单未按 multipart 提交"
grep -q 'name="tls_cert_path"' "$TMPDIR/settings.html" && fail "手动证书仍在填写路径" || ok "手动证书不再要求填写路径"
grep -q 'value="http://127.0.0.1:8080"' "$TMPDIR/settings.html" && ok "配置页回显 server_url" || fail "配置页未回显 server_url"
CSRF="$(grep -o 'name="csrf" value="[^"]*"' "$TMPDIR/settings.html" | head -1 | sed 's/.*value="//;s/"//')"

curl -s -b "$JAR" -X POST "$BASE/settings/domain" \
  -d "csrf=${CSRF}&server_url=ftp://x&base_domain=hs.example.com&apply_now=" -o "$TMPDIR/e1.html"
grep -q '只支持 http:// 或 https://' "$TMPDIR/e1.html" && ok "非法协议被拦截" || fail "非法协议未被拦截"

curl -s -b "$JAR" -X POST "$BASE/settings/network" \
  -d "csrf=${CSRF}&listen_addr=0.0.0.0:8080&grpc_listen_addr=127.0.0.1:50443&metrics_listen_addr=&prefix_v4=999.0.0.0/8&prefix_v6=" -o "$TMPDIR/e2.html"
grep -q 'IPv4 段格式错误' "$TMPDIR/e2.html" && ok "非法 IPv4 段被拦截" || fail "非法 IPv4 段未被拦截"

info "验证网段不写死 100.64.0.0/10"
# 100.64.0.0/10 不再是硬性约束：自定义网段（如云厂商占用的 10.99.0.0/24）应被接受，
# 只校验 CIDR 格式，合法性交给 headscale configtest 兜底。
curl -s -b "$JAR" -X POST "$BASE/settings/network" \
  -d "csrf=${CSRF}&listen_addr=0.0.0.0:8080&grpc_listen_addr=127.0.0.1:50443&metrics_listen_addr=&prefix_v4=10.99.0.0/24&prefix_v6=" -o "$TMPDIR/e2b.html"
grep -q 'IPv4 段格式错误' "$TMPDIR/e2b.html" && fail "自定义 IPv4 段被误判为非法" || ok "自定义 IPv4 段不再被 100.64 范围限制"
grep -q '必须位于' "$TMPDIR/e2b.html" && fail "仍提示必须位于 100.64.0.0/10 内" || ok "已移除必须位于 100.64.0.0/10 的限制"

info "验证 IPv6 开关"
grep -q 'name="ipv6_enabled"' "$TMPDIR/settings.html" && ok "配置页提供 IPv6 开关" || fail "缺少 IPv6 开关"
grep -q 'id="prefix-v6-wrap"' "$TMPDIR/settings.html" && ok "IPv6 网段输入框可联动显隐" || fail "缺少 IPv6 网段联动容器"
# 勾选 IPv6 但留空网段应被拦截。
curl -s -b "$JAR" -X POST "$BASE/settings/network" \
  -d "csrf=${CSRF}&listen_addr=0.0.0.0:8080&grpc_listen_addr=127.0.0.1:50443&metrics_listen_addr=&prefix_v4=100.64.0.0/10&ipv6_enabled=on&prefix_v6=" -o "$TMPDIR/e2c.html"
grep -q '已勾选启用 IPv6' "$TMPDIR/e2c.html" && ok "勾选 IPv6 却留空被拦截" || fail "勾选 IPv6 留空未被拦截"

curl -s -b "$JAR" -X POST "$BASE/settings/tls" \
  -F "csrf=${CSRF}" -F "tls_mode=letsencrypt" -F "tls_letsencrypt_hostname=" \
  -F "tls_letsencrypt_challenge_type=HTTP-01" -F "tls_letsencrypt_listen=:http" \
  -F "tls_letsencrypt_cache_dir=/var/lib/headscale/cache" -o "$TMPDIR/e3.html"
grep -q '证书域名不能为空' "$TMPDIR/e3.html" && ok "缺少证书域名被拦截" || fail "缺少证书域名未被拦截"
# 到期通知邮箱已下线：配置页不该再有这个字段（headscale 的 acme_email 由人工维护证书时用不到）。
grep -q 'name="acme_email"' "$TMPDIR/settings.html" && fail "配置页仍保留到期通知邮箱字段" || ok "配置页已移除到期通知邮箱字段"

# 多张链里第 3 张及以后接错也必须被拦下（线上事故的升级版：链能连到中间 CA，
# 但最后一张贴错，只有一部分客户端建不出链，症状更隐蔽）。
# 这里造「CA → 叶子」两级链，再在后面追加一张无关的 CA：
# 前两张是好的，旧实现只查前两张会放行，然后就把证书写进 /etc/headscale/certs。
# 所以测试前后要对比证书文件是否被改动，被改了就立刻还原。
CERT_FILE=/etc/headscale/certs/manual.crt
CERT_BAK="$TMPDIR/manual.crt.before"
[[ -f "$CERT_FILE" ]] && cp -a "$CERT_FILE" "$CERT_BAK"
# 进到临时目录再用相对文件名生成：Git Bash 自带的 openssl 是 Windows 版，
# 认不了 POSIX 绝对路径（会把 /tmp/x 当成 Git 安装目录下的路径）。
# MSYS2_ARG_CONV_EXCL="*" 是为了不让 MSYS 把 -subj "/CN=..." 当成路径去转换
# （它会转成 "D:/Program Files/Git/CN=..."，openssl 直接报 subject 格式错）。
# Linux 上这两个变量无副作用。
(
  cd "$TMPDIR" || exit 1
  MSYS2_ARG_CONV_EXCL="*" openssl req -x509 -newkey rsa:2048 -nodes -days 1 -keyout e2e-ca.key \
    -out e2e-ca.crt -subj "/CN=hsa-e2e-root" >/dev/null 2>&1
  MSYS2_ARG_CONV_EXCL="*" openssl req -newkey rsa:2048 -nodes -keyout e2e-leaf.key \
    -out e2e-leaf.csr -subj "/CN=hsa-e2e-leaf" >/dev/null 2>&1
  MSYS2_ARG_CONV_EXCL="*" openssl x509 -req -in e2e-leaf.csr -CA e2e-ca.crt \
    -CAkey e2e-ca.key -CAcreateserial -days 1 -out e2e-leaf.crt >/dev/null 2>&1
  MSYS2_ARG_CONV_EXCL="*" openssl req -x509 -newkey rsa:2048 -nodes -days 1 -keyout e2e-other.key \
    -out e2e-other.crt -subj "/CN=hsa-e2e-other" >/dev/null 2>&1
)
if [[ -s "$TMPDIR/e2e-leaf.crt" && -s "$TMPDIR/e2e-ca.crt" && -s "$TMPDIR/e2e-other.crt" ]]; then
  # curl 可能是 Windows 原生程序，不认 /tmp/... 路径，要用 cygpath 转成原生形式。
  LEAF_PATH="$(cygpath -m "$TMPDIR/e2e-leaf.crt" 2>/dev/null || echo "$TMPDIR/e2e-leaf.crt")"
  LEAFKEY_PATH="$(cygpath -m "$TMPDIR/e2e-leaf.key" 2>/dev/null || echo "$TMPDIR/e2e-leaf.key")"
  # 叶子 + 中间 CA + 一张无关的 CA 拼在一起上传：链上有一张接错就必须被拦下。
  cat "$TMPDIR/e2e-leaf.crt" "$TMPDIR/e2e-ca.crt" "$TMPDIR/e2e-other.crt" > "$TMPDIR/e2e-chain3.crt"
  CHAIN3_PATH="$(cygpath -m "$TMPDIR/e2e-chain3.crt" 2>/dev/null || echo "$TMPDIR/e2e-chain3.crt")"
  curl -s -b "$JAR" -X POST "$BASE/settings/tls" \
    -F "csrf=${CSRF}" -F "tls_mode=manual" \
    -F "tls_cert_file=@${LEAF_PATH}" \
    -F "tls_chain_file=@${CHAIN3_PATH}" \
    -F "tls_key_file=@${LEAFKEY_PATH}" \
    -o "$TMPDIR/e6.html"
  grep -q '证书链接不上' "$TMPDIR/e6.html" && ok "链上第 3 张接错被拦截" || fail "第 3 张接错未被拦截"
  if [[ -f "$CERT_FILE" && -f "$CERT_BAK" ]]; then
    if cmp -s "$CERT_FILE" "$CERT_BAK"; then
      ok "校验不通过时证书文件未被改动"
    else
      fail "校验不通过却写了证书文件（已还原）"
      cp -a "$CERT_BAK" "$CERT_FILE"
    fi
  fi
else
  info "无法生成测试用证书链，跳过「第 3 张接错」用例"
fi

# 证书上传：公钥证书 / 中间证书 / 私钥三个文件。
# 上传不配对的私钥必须被拦下，且服务器上的证书文件不能被改动。
# 上传成功会写 /etc/headscale/certs，隔离实例不能测这条路径——
# 成功路径由 Go 单测（临时目录落盘 + 回滚）与生产环境实测覆盖。
if [[ -s "$TMPDIR/e2e-leaf.crt" && -s "$TMPDIR/e2e-ca.crt" && -s "$TMPDIR/e2e-other.key" ]]; then
  # curl 可能是 Windows 原生程序，不认 /tmp/... 路径，要用 cygpath 转成原生形式。
  LEAF_PATH="$(cygpath -m "$TMPDIR/e2e-leaf.crt" 2>/dev/null || echo "$TMPDIR/e2e-leaf.crt")"
  CA_PATH="$(cygpath -m "$TMPDIR/e2e-ca.crt" 2>/dev/null || echo "$TMPDIR/e2e-ca.crt")"
  OTHERKEY_PATH="$(cygpath -m "$TMPDIR/e2e-other.key" 2>/dev/null || echo "$TMPDIR/e2e-other.key")"
  curl -s -b "$JAR" -X POST "$BASE/settings/tls" \
    -F "csrf=${CSRF}" -F "tls_mode=manual" \
    -F "tls_cert_file=@${LEAF_PATH}" \
    -F "tls_chain_file=@${CA_PATH}" \
    -F "tls_key_file=@${OTHERKEY_PATH}" \
    -o "$TMPDIR/e7.html"
  grep -q '证书与私钥不匹配' "$TMPDIR/e7.html" && ok "上传的私钥与证书不配对被拦截" || fail "上传不配对未被拦截"

  curl -s -b "$JAR" -X POST "$BASE/settings/tls" \
    -F "csrf=${CSRF}" -F "tls_mode=manual" \
    -F "tls_cert_file=@${LEAF_PATH}" \
    -o "$TMPDIR/e8.html"
  grep -q '没有选择私钥文件' "$TMPDIR/e8.html" && ok "上传缺私钥被拦截" || fail "上传缺私钥未被拦截"

  # 上传的根本不是证书文件：必须被拦下。
  printf 'this is not a certificate\n' > "$TMPDIR/e2e-notcert.txt"
  NOTCERT_PATH="$(cygpath -m "$TMPDIR/e2e-notcert.txt" 2>/dev/null || echo "$TMPDIR/e2e-notcert.txt")"
  curl -s -b "$JAR" -X POST "$BASE/settings/tls" \
    -F "csrf=${CSRF}" -F "tls_mode=manual" \
    -F "tls_cert_file=@${NOTCERT_PATH}" \
    -F "tls_key_file=@${OTHERKEY_PATH}" \
    -o "$TMPDIR/e9.html"
  grep -q '没有可解析的证书' "$TMPDIR/e9.html" && ok "上传非证书文件被拦截" || fail "上传非证书文件未被拦截"

  if [[ -f "$CERT_FILE" && -f "$CERT_BAK" ]]; then
    if cmp -s "$CERT_FILE" "$CERT_BAK"; then
      ok "上传校验失败时证书文件未被改动"
    else
      fail "上传校验失败却写了证书文件（已还原）"
      cp -a "$CERT_BAK" "$CERT_FILE"
    fi
  fi
else
  info "无法生成测试用证书，跳过「证书上传」用例"
fi

if [[ "$(uname -s)" == "Linux" ]]; then
  # 表单里带 magic_dns=on：设置页的 MagicDNS 复选框默认是勾上的，浏览器提交时
  # 一定会带上这个字段。漏传等于模拟"用户偷偷取消了勾选"，会把开关写成 false。
  code="$(curl -s -b "$JAR" -X POST "$BASE/settings/domain" \
    -d "csrf=${CSRF}&server_url=http://127.0.0.1:8080&base_domain=hs.example.com&magic_dns=on&apply_now=" -o /dev/null -w '%{http_code}')"
  [[ "$code" == "303" ]] && ok "合法域名配置保存成功" || fail "合法配置保存失败 (${code})"
  grep -q '^server_url: http://127.0.0.1:8080' "$TMPDIR/config.yaml" && ok "配置已写入文件" || fail "配置未写入"
  grep -q '^  magic_dns: true$' "$TMPDIR/config.yaml" && ok "保存域名后 MagicDNS 开关保持开启" || fail "保存域名后 MagicDNS 开关被意外关闭"
  ls "$TMPDIR"/config.yaml.bak.* >/dev/null 2>&1 && ok "原配置已备份" || fail "未生成备份"

  info "验证 IPv6 开关真的作用到配置文件"
  # 打开开关：配置里应写入 prefixes.v6。
  curl -s -b "$JAR" -X POST "$BASE/settings/network" \
    -d "csrf=${CSRF}&listen_addr=0.0.0.0:8080&grpc_listen_addr=127.0.0.1:50443&metrics_listen_addr=&prefix_v4=100.64.0.0/10&ipv6_enabled=on&prefix_v6=fd7a:115c:a1e0::/48" -o /dev/null
  grep -q '^  v6: fd7a:115c:a1e0::/48$' "$TMPDIR/config.yaml" && ok "打开开关后 prefixes.v6 已写入配置文件" || fail "打开开关后 prefixes.v6 未写入配置文件"
  # 存一份快照，后面用来比对开关来回切换后配置能不能还原。
  cp "$TMPDIR/config.yaml" "$TMPDIR/config.yaml.snapshot"
  # 关掉开关：配置里的 prefixes.v6 必须消失，而不是留着旧值继续分配 IPv6。
  curl -s -b "$JAR" -X POST "$BASE/settings/network" \
    -d "csrf=${CSRF}&listen_addr=0.0.0.0:8080&grpc_listen_addr=127.0.0.1:50443&metrics_listen_addr=&prefix_v4=100.64.0.0/10&prefix_v6=" -o /dev/null
  grep -q 'fd7a:115c:a1e0::/48' "$TMPDIR/config.yaml" && fail "关掉开关后 prefixes.v6 仍留在配置文件里（开关无效）" || ok "关掉开关后 prefixes.v6 已从配置文件移除"
  grep -q '^  v4: 100.64.0.0/10$' "$TMPDIR/config.yaml" && ok "关掉 IPv6 没有损坏 IPv4 网段" || fail "关掉 IPv6 时 IPv4 网段被弄坏"
  # 页面回读：开关勾选状态要跟着配置文件走。
  curl -s -b "$JAR" "$BASE/settings" -o "$TMPDIR/settings2.html"
  grep 'id="ipv6_enabled"' "$TMPDIR/settings2.html" | grep -c 'checked' >/dev/null && fail "关掉开关后页面仍显示已勾选" || ok "关掉开关后页面显示未勾选"
  # 重新打开开关：v6 要写回，而且要落在 prefixes 块内。
  curl -s -b "$JAR" -X POST "$BASE/settings/network" \
    -d "csrf=${CSRF}&listen_addr=0.0.0.0:8080&grpc_listen_addr=127.0.0.1:50443&metrics_listen_addr=&prefix_v4=100.64.0.0/10&ipv6_enabled=on&prefix_v6=fd7a:115c:a1e0::/48" -o /dev/null
  grep -q '^  v6: fd7a:115c:a1e0::/48$' "$TMPDIR/config.yaml" && ok "重新打开开关后 prefixes.v6 已写回配置" || fail "重新打开开关后 prefixes.v6 没写回"
  # 位置断言：v4 和 v6 都必须落在 `prefixes:` 与下一个顶层键之间，且 v4 在 v6 之前。
  #
  # 光断言「存在一行 v6」是不够的：块定位正则一旦贪婪吞到文件末尾，
  # 新键会被追加到**文件末尾**（仍以两个空格开头，照样能 grep 到），
  # 而 headscale configtest 容忍未知键、照样通过，看着像成功其实没生效。
  # 也不能只比对「开关前后两份文件」——两份由同一段代码写出，错得一样就永远一致。
  pfxline="$(grep -n '^prefixes:' "$TMPDIR/config.yaml" | cut -d: -f1)"
  dnsline="$(grep -n '^dns:' "$TMPDIR/config.yaml" | cut -d: -f1)"
  v4line="$(grep -n '^  v4:' "$TMPDIR/config.yaml" | cut -d: -f1)"
  # 只取 prefixes 块内那一行 v6（dns 块里也有一个同名键，不能混进来）。
  v6line="$(grep -n '^  v6: fd7a:115c:a1e0::/48$' "$TMPDIR/config.yaml" | cut -d: -f1)"
  lastline="$(wc -l <"$TMPDIR/config.yaml")"
  if [[ -n "$pfxline" && -n "$dnsline" && -n "$v4line" && -n "$v6line" \
        && "$v4line" -gt "$pfxline" && "$v6line" -gt "$v4line" && "$v6line" -lt "$dnsline" ]]; then
    ok "prefixes.v6 落在 prefixes 块内、且在 v4 之后（行 ${pfxline} < ${v4line} < ${v6line} < ${dnsline}）"
  else
    fail "prefixes.v6 位置不对：prefixes=${pfxline:-?} v4=${v4line:-?} v6=${v6line:-?} dns=${dnsline:-?}"
  fi
  [[ -n "$v6line" && "$v6line" -ne "$lastline" ]] \
    && ok "prefixes.v6 没有被追加到文件末尾" \
    || fail "prefixes.v6 落在文件最后一行（块定位跑到文件末尾了）"
  # 别的块里的同名键必须毫发无伤：块定位越界时，写 v6 会改掉这里、删 v6 会连这里一起删。
  grep -q '^  v6: keepme-under-dns$' "$TMPDIR/config.yaml" \
    && ok "dns 块里的同名键未被误改/误删" \
    || { fail "dns 块里的同名键被误伤（块定位越界）"; grep -n '  v6:' "$TMPDIR/config.yaml" | sed 's/^/      /'; }
  if diff -q "$TMPDIR/config.yaml.snapshot" "$TMPDIR/config.yaml" >/dev/null 2>&1; then
    ok "开关来回切换后配置字节级还原"
  else
    fail "开关来回切换后配置顺序变了"
    diff "$TMPDIR/config.yaml.snapshot" "$TMPDIR/config.yaml" | sed 's/^/      /' || true
  fi
else
  info "非 Linux 环境无法执行 headscale configtest，跳过写入链路（由远程测试覆盖）"
fi

info "验证配置与策略「校验不通过就不落盘」"
# 旧实现是「先落盘、再校验」：校验没过的话坏配置已经躺在生产配置上了，
# 界面还只提示一句「已保存但校验失败」。新流程必须是校验不过就不写。
NETCSRF="$(curl -s -b "$JAR" "$BASE/network" | grep -o 'name="csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')"
[[ ${#NETCSRF} -ge 32 ]] && ok "网络页 CSRF 已下发" || fail "网络页缺少 CSRF"
CFG_BEFORE="$(sha256sum "$TMPDIR/config.yaml" | cut -d' ' -f1)"
curl -s -b "$JAR" -X POST "$BASE/network/config" \
  --data-urlencode "csrf=${NETCSRF}" \
  --data-urlencode 'config=listen_addr: 0.0.0.0:8080
# 故意缺少 server_url：configtest 必然不通过' -o /dev/null
CFG_AFTER="$(sha256sum "$TMPDIR/config.yaml" | cut -d' ' -f1)"
[[ "$CFG_BEFORE" == "$CFG_AFTER" ]] && ok "坏配置校验不通过时原配置一字未动" || fail "坏配置被写进了配置文件"
curl -s -b "$JAR" "$BASE/network" -o "$TMPDIR/net-badcfg.html"
grep -q '校验未通过' "$TMPDIR/net-badcfg.html" && ok "页面提示「校验未通过」" || fail "页面没有提示校验未通过"
grep -q '已保存并通过校验' "$TMPDIR/net-badcfg.html" && fail "坏配置却提示已保存" || ok "没有把坏配置说成已保存"
# 候选文件必须被清掉，不能在配置目录里留垃圾。
if ls "$TMPDIR"/.config-candidate.yaml >/dev/null 2>&1; then
  fail "候选配置文件没有被清理"
else
  ok "候选配置文件已清理"
fi

# 策略：坏策略不落盘，好策略才写入。
POL_BEFORE="$(sha256sum "$TMPDIR/policy.hujson" | cut -d' ' -f1)"
curl -s -b "$JAR" -X POST "$BASE/network/acl" \
  --data-urlencode "csrf=${NETCSRF}" \
  --data-urlencode 'acl={"acls":[{"action":"accept"}' -o /dev/null
POL_AFTER="$(sha256sum "$TMPDIR/policy.hujson" | cut -d' ' -f1)"
[[ "$POL_BEFORE" == "$POL_AFTER" ]] && ok "坏策略校验不通过时原策略一字未动" || fail "坏策略被写进了策略文件"
curl -s -b "$JAR" "$BASE/network" -o "$TMPDIR/net-badacl.html"
grep -q '策略校验未通过' "$TMPDIR/net-badacl.html" && ok "页面提示「策略校验未通过」" || fail "页面没有提示策略校验未通过"
curl -s -b "$JAR" -X POST "$BASE/network/acl" \
  --data-urlencode "csrf=${NETCSRF}" \
  --data-urlencode 'acl={"acls":[{"action":"accept","src":["10.0.0.0/8"],"dst":["10.0.0.1:22"]}]}' -o /dev/null
grep -q '10.0.0.1:22' "$TMPDIR/policy.hujson" && ok "合法策略已写入策略文件" || fail "合法策略没有写入策略文件"

info "验证网络与策略页（图形化表单）"
curl -s -b "$JAR" "$BASE/network" -o "$TMPDIR/network.html"
for kw in 'DNS 设置' 'DERP 中继' 'ACL 策略' '规则向导' 'DNS 服务器' '搜索域' '中继区域编号'; do
  grep -q "$kw" "$TMPDIR/network.html" && ok "网络页包含「$kw」" || fail "网络页缺少「$kw」"
done
# 动态行：DNS 服务器 / 搜索域各有一组可增删的行。
grep -q 'id="ns-rows"' "$TMPDIR/network.html" && ok "DNS 服务器动态行容器存在" || fail "缺少 DNS 服务器动态行容器"
grep -q 'id="sd-rows"' "$TMPDIR/network.html" && ok "搜索域动态行容器存在" || fail "缺少搜索域动态行容器"
grep -q 'name="ns"' "$TMPDIR/network.html" && ok "DNS 服务器输入框存在" || fail "缺少 DNS 服务器输入框"
grep -q 'name="search"' "$TMPDIR/network.html" && ok "搜索域输入框存在" || fail "缺少搜索域输入框"
grep -q 'name="override_local_dns"' "$TMPDIR/network.html" && ok "覆盖本地 DNS 开关存在" || fail "缺少覆盖本地 DNS 开关"
grep -q 'name="region_id"' "$TMPDIR/network.html" && ok "DERP 区域编号输入框存在" || fail "缺少 DERP 区域编号输入框"
grep -q 'name="region_name"' "$TMPDIR/network.html" && ok "DERP 区域名称输入框存在" || fail "缺少 DERP 区域名称输入框"
# ACL 规则向导：来源 / 目标 / 端口 + 生成按钮 + JSON 编辑框。
for kw in 'id="acl-src"' 'id="acl-dst"' 'id="acl-port"' 'id="acl-add"' 'id="acl-editor"'; do
  grep -q "$kw" "$TMPDIR/network.html" && ok "ACL 向导包含「$kw」" || fail "ACL 向导缺少「$kw」"
done
grep -q 'acl-add' "$SRC_ROOT/static/app.js" && ok "ACL 向导脚本就绪" || fail "缺少 ACL 向导脚本"

info "验证网络表单校验与提示消息"
NCSRF="$(grep -o 'name="csrf" value="[^"]*"' "$TMPDIR/network.html" | head -1 | sed 's/.*value="//;s/"//')"
[[ ${#NCSRF} -ge 32 ]] && ok "网络页 CSRF token 已下发" || fail "网络页未下发 CSRF token"
# flash 提示必须真的显示出来（SQLite 的 UPDATE..RETURNING 语义曾导致提示恒为空）。
curl -s -b "$JAR" -X POST "$BASE/network/dns" \
  --data-urlencode "csrf=${NCSRF}" --data-urlencode "base_domain=hs.example.com" \
  --data-urlencode "ns=not-an-ip" --data-urlencode "search=" \
  -o /dev/null -w '%{http_code}' | grep -c '303' >/dev/null && ok "DNS 表单提交已重定向" || fail "DNS 表单提交未重定向"
curl -s -b "$JAR" "$BASE/network" -o "$TMPDIR/n1.html"
grep -q 'DNS 服务器格式不正确' "$TMPDIR/n1.html" && ok "非法 DNS 服务器被拦截并提示" || fail "非法 DNS 服务器未提示"
# 同一条提示只能显示一次，刷新后应消失。
curl -s -b "$JAR" "$BASE/network" -o "$TMPDIR/n2.html"
grep -q 'DNS 服务器格式不正确' "$TMPDIR/n2.html" && fail "flash 提示重复显示" || ok "flash 提示只显示一次"

curl -s -b "$JAR" -X POST "$BASE/network/dns" \
  --data-urlencode "csrf=${NCSRF}" --data-urlencode "base_domain=hs.example.com" \
  --data-urlencode "ns=223.5.5.5" --data-urlencode "search=bad domain!" \
  -o /dev/null
curl -s -b "$JAR" "$BASE/network" -o "$TMPDIR/n3.html"
grep -q '搜索域格式不正确' "$TMPDIR/n3.html" && ok "非法搜索域被拦截并提示" || fail "非法搜索域未提示"

curl -s -b "$JAR" -X POST "$BASE/network/derp" \
  --data-urlencode "csrf=${NCSRF}" --data-urlencode "enabled=on" \
  --data-urlencode "region_id=99999" --data-urlencode "region_name=test" \
  -o /dev/null
curl -s -b "$JAR" "$BASE/network" -o "$TMPDIR/n4.html"
grep -q '中继区域编号应为 0-65535 的数字' "$TMPDIR/n4.html" && ok "非法 DERP 区域编号被拦截并提示" || fail "非法 DERP 区域编号未提示"

curl -s -b "$JAR" -X POST "$BASE/network/acl" \
  --data-urlencode "csrf=${NCSRF}" --data-urlencode "acl=" -o /dev/null
curl -s -b "$JAR" "$BASE/network" -o "$TMPDIR/n5.html"
grep -q 'ACL 策略不能为空' "$TMPDIR/n5.html" && ok "空 ACL 被拦截并提示" || fail "空 ACL 未提示"

info "验证整份配置覆盖保存（模拟浏览器 textarea 的 CRLF 提交）"
# 浏览器的 textarea 按 HTML 规范用 CRLF 提交，服务端原样落盘就会把配置写成 CRLF。
# headscale 自己的 YAML 解析器能接受 CRLF，但本项目的行解析器（blockBody 要求
# 块头后面紧跟 \n）认不出来，于是 dns 块里的值全被读成空：页面上域名是空的、
# 开关是未勾选的，用户再点一次「保存 DNS」，空值就被写回配置。
# 覆盖前先记下 dns.magic_dns 的真值：断言要对着实际状态比对，不能写死「应已勾选」。
# 浏览器在复选框勾选时提交 magic_dns=on、不勾选时根本不提交这个字段，所以「保存域名」
# 这类请求会把开关真实地写成 false —— 那之后正确的表现就是「未勾选」。
PRE_MAGIC="$(grep -m1 -E '^[[:space:]]*magic_dns:' "$TMPDIR/config.yaml" | sed 's/^[^:]*:[[:space:]]*//' | tr -d '\r')"
PRE_MAGIC="${PRE_MAGIC:-false}"
CRLF_CONFIG="$(sed 's/$/\r/' "$TMPDIR/config.yaml")"
curl -s -b "$JAR" -X POST "$BASE/network/config" \
  --data-urlencode "csrf=${NCSRF}" --data-urlencode "config=${CRLF_CONFIG}" -o /dev/null
# 用 tr 数 CR 字节，不能用 grep：这个环境的 grep 压根不认 \r（实测在已知 CRLF
# 文件上返回 0），拿 grep 判定「有没有 CR」会永远得到「没有」——断言假绿。
CR_BYTES="$(tr -cd '\r' < "$TMPDIR/config.yaml" | wc -c | tr -d ' ')"
if [[ "${CR_BYTES}" != "0" ]]; then
  fail "CRLF 提交后配置文件里仍有 ${CR_BYTES} 个 CR 字节（配置被写成了 CRLF）"
else
  ok "CRLF 提交后配置文件已归一化为 LF"
fi
# 页面回读：dns 块的值必须还在。CRLF 被原样落盘时这里会读成空。
curl -s -b "$JAR" "$BASE/network" -o "$TMPDIR/n6.html"
grep -q 'name="base_domain" value="hs.example.com"' "$TMPDIR/n6.html" && ok "覆盖保存后域名仍能读回" || fail "覆盖保存后域名读不回来了"
if grep 'name="magic_dns"' "$TMPDIR/n6.html" | grep -c 'checked' >/dev/null; then
  SHOWN_MAGIC="true"
else
  SHOWN_MAGIC="false"
fi
if [[ "$SHOWN_MAGIC" == "$PRE_MAGIC" ]]; then
  ok "覆盖保存后 MagicDNS 开关状态与配置一致（${PRE_MAGIC}）"
else
  fail "覆盖保存后 MagicDNS 开关状态与配置不一致：配置是 ${PRE_MAGIC}，页面显示 ${SHOWN_MAGIC}"
fi

info "验证管理界面 HTTPS 卡片"
for kw in '管理界面 HTTPS' 'id="admin-tls-enabled"' 'id="admin-tls-body"' 'name="admin_cert_pem"' 'name="admin_key_pem"'; do
  grep -q "$kw" "$TMPDIR/settings.html" && ok "配置页包含「$kw」" || fail "配置页缺少「$kw」"
done
grep -q 'admin-tls-enabled' "$SRC_ROOT/static/app.js" && ok "管理界面 HTTPS 开关联动脚本就绪" || fail "缺少管理界面 HTTPS 联动脚本"
# 粘贴的 PEM 不成对时必须被拒绝，且不落盘。
curl -s -b "$JAR" -X POST "$BASE/settings/admin-tls" \
  --data-urlencode "csrf=${CSRF}" --data-urlencode "enabled=on" --data-urlencode "source=paste" \
  --data-urlencode "admin_cert_pem=garbage" --data-urlencode "admin_key_pem=garbage" \
  -o "$TMPDIR/t1.html"
grep -q '证书或私钥无效' "$TMPDIR/t1.html" && ok "无效 PEM 被拒绝并提示" || fail "无效 PEM 未被拒绝"
# headscale 没有手动证书时不允许复用。
curl -s -b "$JAR" -X POST "$BASE/settings/admin-tls" \
  --data-urlencode "csrf=${CSRF}" --data-urlencode "enabled=on" --data-urlencode "source=reuse" \
  -o "$TMPDIR/t2.html"
grep -q '没有手动证书可复用' "$TMPDIR/t2.html" && ok "无可复用证书时被拦截并提示" || fail "无可复用证书时未被拦截"
# systemd 单元必须禁用启动限流：设置页保存 HTTPS 配置会触发 systemctl restart，
# 连续保存几次撞上默认限流（10 秒 5 次）就会 start-limit-hit，服务起不来、管理界面被锁死。
if [[ -f "$SCRIPT_DIR/deploy-headscale-admin.sh" ]]; then
  grep -q 'StartLimitIntervalSec=0' "$SCRIPT_DIR/deploy-headscale-admin.sh" \
    && ok "systemd 单元已禁用启动限流（start-limit-hit 不会锁死管理界面）" \
    || fail "systemd 单元未禁用启动限流，连续保存 HTTPS 设置会导致服务起不来"
fi

info "验证客户端一键接入"
curl -s -b "$JAR" "$BASE/keys" -o "$TMPDIR/keys.html"
for kw in '客户端一键接入' 'id="join-cmd"' 'id="join-copy"' 'data-server-url="http://127.0.0.1:8080"'; do
  grep -q "$kw" "$TMPDIR/keys.html" && ok "密钥页包含「$kw」" || fail "密钥页缺少「$kw」"
done
grep -q '尚未创建密钥' "$TMPDIR/keys.html" && ok "未创建密钥时有占位提示" || fail "缺少密钥占位提示"
grep -q 'id="join-copy" disabled' "$TMPDIR/keys.html" && ok "命令不完整时复制按钮禁用" || fail "复制按钮未禁用"
if [[ "$(uname -s)" == "Linux" ]]; then
  KEY_CSRF="$(grep -o 'name="csrf" value="[^"]*"' "$TMPDIR/keys.html" | head -1 | sed 's/.*value="//;s/"//')"
  [[ "$(curl -s -b "$JAR" -X POST "$BASE/keys/preauth/create" -d "csrf=${KEY_CSRF}&user_id=1&expiration=24h&reusable=on&note=%E6%B5%8B%E8%AF%95%E6%9C%BA%E6%88%BF" -o /dev/null -w '%{http_code}')" == "303" ]] && ok "创建预授权密钥成功" || fail "创建预授权密钥失败"
  curl -s -b "$JAR" "$BASE/keys" -o "$TMPDIR/keys2.html"
  grep -q 'data-join-key="hskey-test-123"' "$TMPDIR/keys2.html" && ok "新密钥自动填入接入卡片" || fail "新密钥未自动填入"
  grep -q 'tailscale up --login-server=http://127.0.0.1:8080 --authkey=hskey-test-123' "$TMPDIR/keys2.html" && ok "接入命令自动拼接完成" || fail "接入命令未拼接"
  grep -q '自动填入上方' "$TMPDIR/keys2.html" && ok "生成提示指向接入卡片" || fail "生成提示未更新"
  curl -s -b "$JAR" "$BASE/keys" -o "$TMPDIR/keys3.html"
  grep -q 'data-join-key=""' "$TMPDIR/keys3.html" && ok "密钥仅显示一次，刷新后不再带出" || fail "密钥被重复展示"

  info "验证密钥类型标识与备注"
  grep -q '>类型</th>' "$TMPDIR/keys2.html" && ok "列表包含类型列" || fail "缺少类型列"
  grep -q '>备注</th>' "$TMPDIR/keys2.html" && ok "列表包含备注列" || fail "缺少备注列"
  grep -q '可复用' "$TMPDIR/keys2.html" && ok "可复用标识正常" || fail "可复用标识缺失"
  grep -q '未使用' "$TMPDIR/keys2.html" && ok "未使用标识正常" || fail "未使用标识缺失"
  grep -q '测试机房' "$TMPDIR/keys2.html" && ok "创建时备注已保存并显示" || fail "创建时备注未显示"
  grep -q 'hskey-test-ab12-\*\*\*' "$TMPDIR/keys2.html" && ok "打码密钥按原样展示" || fail "打码密钥展示异常"
  grep -q '仅创建时可见完整值' "$TMPDIR/keys2.html" && ok "打码密钥有说明文案" || fail "打码密钥缺少说明"
  NOTE_CSRF="$(grep -o 'name="csrf" value="[^"]*"' "$TMPDIR/keys2.html" | head -1 | sed 's/.*value="//;s/"//')"
  [[ "$(curl -s -b "$JAR" -X POST "$BASE/keys/preauth/note" -d "csrf=${NOTE_CSRF}&id=5&note=%E5%8A%9E%E5%85%AC%E7%94%B5%E8%84%91" -o /dev/null -w '%{http_code}')" == "303" ]] && ok "备注修改接口正常" || fail "备注修改接口失败"
  curl -s -b "$JAR" "$BASE/keys" -o "$TMPDIR/keys4.html"
  grep -q '办公电脑' "$TMPDIR/keys4.html" && ok "修改后的备注已显示" || fail "修改后的备注未显示"
  grep -q '测试机房' "$TMPDIR/keys4.html" && fail "旧备注未覆盖" || ok "旧备注已被覆盖"

  info "验证 API key 一次性展示"
  API_CSRF="$(grep -o 'name="csrf" value="[^"]*"' "$TMPDIR/keys4.html" | head -1 | sed 's/.*value="//;s/"//')"
  [[ "$(curl -s -b "$JAR" -X POST "$BASE/keys/api/create" -d "csrf=${API_CSRF}&expiration=90d" -o /dev/null -w '%{http_code}')" == "303" ]] && ok "创建 API key 成功" || fail "创建 API key 失败"
  curl -s -b "$JAR" "$BASE/keys" -o "$TMPDIR/keys5.html"
  grep -q 'id="api-once-key">hsapi-test-456<' "$TMPDIR/keys5.html" && ok "新 API key 醒目展示完整值" || fail "新 API key 未展示完整值"
  grep -q 'id="api-once-copy"' "$TMPDIR/keys5.html" && ok "API key 卡片带复制按钮" || fail "API key 卡片缺少复制按钮"
  curl -s -b "$JAR" "$BASE/keys" -o "$TMPDIR/keys6.html"
  grep -q 'hsapi-test-456' "$TMPDIR/keys6.html" && fail "API key 被重复展示" || ok "API key 仅显示一次，刷新后消失"
else
  info "非 Linux 环境无法执行 fake headscale，跳过密钥创建链路（由远程测试覆盖）"
fi

info "验证中英文切换"
curl -sL -b "$JAR" -c "$JAR" "$BASE/lang?lang=en&next=/" -o /dev/null
curl -s -b "$JAR" "$BASE/" | grep -c 'Dashboard' >/dev/null && ok "英文界面标题生效" || fail "英文界面未生效"
curl -s -b "$JAR" "$BASE/ops" | grep -c 'Live network speed' >/dev/null && ok "英文网速文案生效" || fail "英文网速文案缺失"
curl -s -b "$JAR" "$BASE/ops" | grep -c 'Host resources' >/dev/null && ok "英文本机资源文案生效" || fail "英文本机资源文案缺失"
curl -s -b "$JAR" "$BASE/ops" | grep -c 'Node monitoring' >/dev/null && ok "英文节点监控文案生效" || fail "英文节点监控文案缺失"
curl -s -b "$JAR" "$BASE/ops" | grep -c 'Live speed' >/dev/null && ok "英文实时速度文案生效" || fail "英文实时速度文案缺失"
curl -s -b "$JAR" "$BASE/users" | grep -c 'New user' >/dev/null && ok "英文表单文案生效" || fail "英文表单文案缺失"
curl -s -b "$JAR" "$BASE/settings" | grep -c 'Certificate auto-renewal' >/dev/null && ok "英文配置页生效" || fail "英文配置页缺失"
curl -s -b "$JAR" "$BASE/keys" | grep -c 'One-click client join' >/dev/null && ok "英文一键接入文案生效" || fail "英文一键接入文案缺失"
curl -sL -b "$JAR" -c "$JAR" "$BASE/lang?lang=zh&next=/" -o /dev/null
curl -s -b "$JAR" "$BASE/" | grep -c '总览' >/dev/null && ok "切回中文生效" || fail "切回中文失败"

info "验证 CSRF"
curl -s -b "$JAR" "$BASE/users" -o "$TMPDIR/users.html"
CSRF="$(grep -o 'name="csrf" value="[^"]*"' "$TMPDIR/users.html" | head -1 | sed 's/.*value="//;s/"//')"
[[ ${#CSRF} -ge 32 ]] && ok "CSRF token 已下发" || fail "未找到 CSRF token"
[[ "$(curl -s -b "$JAR" -X POST "$BASE/users/create" -d 'name=x' -o /dev/null -w '%{http_code}')" == "400" ]] && ok "缺少 CSRF 的请求被拒绝" || fail "缺少 CSRF 未被拒绝"
[[ "$(curl -s -b "$JAR" -X POST "$BASE/users/create" -d "csrf=${CSRF}&name=u1" -o /dev/null -w '%{http_code}')" == "303" ]] && ok "携带 CSRF 的请求通过" || fail "CSRF 校验异常"

info "验证操作提示文案"
# headscale CLI 的输出是 JSON（假 headscale 统一回 {}），不能原样当成提示文案，
# 否则页面上会出现「操作成功：{}」这种没人看得懂的东西。
curl -s -b "$JAR" "$BASE/users" -o "$TMPDIR/users2.html"
grep -q '操作成功' "$TMPDIR/users2.html" && ok "操作成功提示已显示" || fail "操作成功提示未显示"
grep -q '操作成功：{}' "$TMPDIR/users2.html" && fail "提示里混进了 CLI 的原始 JSON（操作成功：{}）" || ok "提示里没有 CLI 原始 JSON"

info "验证确认弹窗"
# 原生 confirm() 由浏览器绘制，位置不可控（常浮在页面顶部），改成页内模态框。
# 用网络页断言：它的「保存 ACL」「覆盖保存」表单是固定渲染的，与有没有数据无关。
curl -s -b "$JAR" "$BASE/network" -o "$TMPDIR/net2.html"
grep -q 'id="hsa-modal"' "$TMPDIR/net2.html" && ok "确认弹窗骨架已下发" || fail "缺少确认弹窗骨架"
grep -q 'data-confirm=' "$TMPDIR/net2.html" && ok "危险操作带 data-confirm 声明" || fail "危险操作缺少 data-confirm"
if grep -rq "onsubmit=\"return confirm(" "$SRC_ROOT/templates"; then
  fail "仍有模板使用浏览器原生 confirm()"
else
  ok "模板已全部改用页内确认弹窗"
fi
grep -q 'hsa-modal' "$SRC_ROOT/static/app.js" && ok "确认弹窗逻辑已就绪" || fail "缺少确认弹窗逻辑"
grep -q 'hsa.scrollY' "$SRC_ROOT/static/app.js" && ok "提交后恢复滚动位置的逻辑已就绪" || fail "缺少滚动位置恢复逻辑"
# 滚动补偿必须按「提示条高度差」算：只加新提示条的高度，在「页面上本来就有
# 提示条」时会多算一次，落点偏出一个提示条的高度（实测偏 68px）。
grep -q 'hsa.flashH' "$SRC_ROOT/static/app.js" && ok "滚动补偿按提示条高度差计算" || fail "滚动补偿没有按提示条高度差计算"

info "验证节点网速上报通道"
SPEED_TOKEN="test-speed-token"
# 探针用的节点标识不能与后面假节点的 ID/名称/IP 撞上，
# 否则 lookupRate 会优先匹配到它、把真实速率顶掉。
sp_code() { curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/speed" -H 'Content-Type: application/json' "$@"; }
[[ "$(sp_code -d '{"node":"auth-probe","rx_bytes":1,"tx_bytes":2}')" == "401" ]] && ok "缺少令牌的上报被拒绝" || fail "缺少令牌的上报未被拒绝"
[[ "$(sp_code -H 'X-Node-Speed-Token: wrong-token' -d '{"node":"auth-probe","rx_bytes":1,"tx_bytes":2}')" == "401" ]] && ok "错误令牌的上报被拒绝" || fail "错误令牌的上报未被拒绝"
[[ "$(sp_code -H "X-Node-Speed-Token: $SPEED_TOKEN" -d 'not json')" == "400" ]] && ok "非法请求体被拒绝" || fail "非法请求体未被拒绝"
[[ "$(sp_code -H "X-Node-Speed-Token: $SPEED_TOKEN" -d '{"rx_bytes":1,"tx_bytes":2}')" == "400" ]] && ok "缺少节点标识的上报被拒绝" || fail "缺少节点标识的上报未被拒绝"
[[ "$(sp_code -H "X-Node-Speed-Token: $SPEED_TOKEN" -d '{"node":"auth-probe","rx_bytes":1,"tx_bytes":2}')" == "200" ]] && ok "正确令牌的上报被接受" || fail "正确令牌的上报未被接受"
# 通道状态与 headscale CLI 无关，任何平台都要能读出来。
curl -s -b "$JAR" "$BASE/api/monitor" -o "$TMPDIR/monitor-ch.json"
grep -q '"speed_enabled":true' "$TMPDIR/monitor-ch.json" && ok "上报通道状态已下发" || fail "上报通道状态缺失"

curl -s -b "$JAR" "$BASE/settings" -o "$TMPDIR/settings-speed.html"
grep -q 'id="speed-endpoint"' "$TMPDIR/settings-speed.html" && ok "配置页展示上报接口地址" || fail "配置页缺少上报接口地址"
grep -q 'id="speed-token"' "$TMPDIR/settings-speed.html" && ok "配置页展示上报令牌" || fail "配置页缺少上报令牌"
grep -q 'id="speed-channel-state"' "$TMPDIR/settings-speed.html" && ok "配置页展示上报通道状态" || fail "配置页缺少上报通道状态"
# 运维页只保留紧凑的通道状态，上报地址与令牌已经挪到配置页。
curl -s -b "$JAR" "$BASE/ops" -o "$TMPDIR/ops-speed.html"
grep -q 'id="speed-channel-state"' "$TMPDIR/ops-speed.html" && ok "运维页展示上报通道状态" || fail "运维页缺少上报通道状态"
grep -q 'id="speed-token"' "$TMPDIR/ops-speed.html" && fail "运维页仍在展示上报令牌" || ok "运维页不再展示上报令牌"
grep -q 'id="speed-endpoint"' "$TMPDIR/ops-speed.html" && fail "运维页仍在展示上报接口" || ok "运维页不再展示上报接口"

if [[ "$(uname -s)" == "Linux" ]]; then
  info "验证上报后节点速度真正显示出来"
  # 造一个节点出来，上报的数据才能挂到具体节点上。
  cat >"$TMPDIR/nodes.json" <<'JSON'
[{"id":12,"name":"laptop","given_name":"laptop","user":{"name":"default"},"ip_addresses":["100.64.0.5"],"online":true,"last_seen":{"seconds":1794889542,"nanos":0},"expiration":{"seconds":1894975942,"nanos":0},"register_method":"1","client_version":"1.60.0","host_info":{"os":"linux"},"tags":[],"approved_routes":[],"available_routes":[]}]
JSON
  # 连报两次：第一次建立基准，第二次才有速率差值。
  sp_code -H "X-Node-Speed-Token: $SPEED_TOKEN" -d '{"node":"100.64.0.5","rx_bytes":1000,"tx_bytes":500}' >/dev/null
  sleep 2
  sp_code -H "X-Node-Speed-Token: $SPEED_TOKEN" -d '{"node":"100.64.0.5","rx_bytes":61440,"tx_bytes":30720}' >/dev/null
  # 轮询直到速率真正被换算出来。注意不能一见 speed_source=true 就收工：
  # 采样点可能正好落在两次上报之间，那时数据源已可用但速率仍是 0。
  found=0
  for _ in $(seq 1 60); do
    curl -s -b "$JAR" "$BASE/api/monitor" -o "$TMPDIR/monitor-speed.json"
    rx="$(grep -o '"rx_rate":[0-9.]*' "$TMPDIR/monitor-speed.json" | head -1 | cut -d: -f2)"
    if [[ -n "$rx" ]] && awk -v v="$rx" 'BEGIN{exit !(v>5 && v<200)}'; then found=1; break; fi
    sleep 0.5
  done
  [[ "$found" == "1" ]] && ok "上报后节点速率已换算为非零值（${rx:-0} KB/s）" || fail "上报后节点速率未换算"
  grep -q '"speed_source":true' "$TMPDIR/monitor-speed.json" && ok "上报后网速数据源变为可用" || fail "上报后网速数据源仍不可用"
  grep -q '"speed_channel":"api"' "$TMPDIR/monitor-speed.json" && ok "数据来源标记为节点上报" || fail "数据来源标记异常"
else
  info "非 Linux 环境无法执行 fake headscale，节点速度展示断言由远程测试覆盖"
fi

info "验证路由审批（待审批集合 + 全量覆盖语义）"
# headscale 的 available_routes 含已批准的路由，所以「待审批」必须做差集：
# 节点 21 有一条已批准（10.0.0.0/8）和一条待批准（192.168.1.0/24）；
# 节点 22 全部已批准，不该再出现在审批队列里。
cat >"$TMPDIR/nodes.json" <<'JSON'
[{"id":21,"name":"router","given_name":"office-router","user":{"name":"default"},"ip_addresses":["100.64.0.9"],"online":true,"last_seen":{"seconds":1794889542,"nanos":0},"tags":[],"approved_routes":["10.0.0.0/8"],"available_routes":["10.0.0.0/8","192.168.1.0/24"]},{"id":22,"name":"router2","given_name":"router2","user":{"name":"default"},"ip_addresses":["100.64.0.10"],"online":true,"last_seen":{"seconds":1794889542,"nanos":0},"tags":[],"approved_routes":["192.168.2.0/24"],"available_routes":["192.168.2.0/24"]}]
JSON
curl -s -b "$JAR" "$BASE/nodes" -o "$TMPDIR/nodes-page.html"
# 路由审批卡片是页面最后一个 section，从它的标题往后截取，
# 免得把节点列表里的同名内容误判成审批队列里的内容。
awk '/路由审批/{f=1} f' "$TMPDIR/nodes-page.html" >"$TMPDIR/route-section.html"
grep -q '192.168.1.0/24' "$TMPDIR/route-section.html" && ok "待批准的路由出现在审批队列" || fail "待批准的路由没有出现在审批队列"
grep -q '192.168.2.0/24' "$TMPDIR/route-section.html" && fail "全部已批准的节点仍留在审批队列" || ok "全部已批准的节点已移出审批队列"
grep -q 'name="routes"[^>]*value="192.168.1.0/24"' "$TMPDIR/route-section.html" && ok "审批表单预填的是待批准路由" || fail "审批表单预填的不是待批准路由"
grep -q 'name="routes"[^>]*value="10.0.0.0/8"' "$TMPDIR/route-section.html" && fail "审批表单误填了已批准的路由" || ok "审批表单没有误填已批准的路由"

# 审批：CLI 必须收到「原有已批准 ∪ 本次提交」。headscale 的 approve-routes
# 是全量覆盖语义，只提交新路由会把之前批准的抹掉。
: >"$TMPDIR/hs-calls.log"
CSRF_N="$(curl -s -b "$JAR" "$BASE/nodes" | grep -o 'name="csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')"
curl -s -b "$JAR" -X POST "$BASE/nodes/approve" -d "csrf=${CSRF_N}&id=21&routes=192.168.1.0/24" -o /dev/null
grep -q -- '-r 10.0.0.0/8,192.168.1.0/24' "$TMPDIR/hs-calls.log" && ok "审批带上原有已批准路由（全量覆盖语义）" || fail "审批没有带上原有已批准路由"

# 审批生效后（模拟 headscale 把路由挪进 approved），该路由必须从队列里消失。
cat >"$TMPDIR/nodes.json" <<'JSON'
[{"id":21,"name":"router","given_name":"office-router","user":{"name":"default"},"ip_addresses":["100.64.0.9"],"online":true,"last_seen":{"seconds":1794889542,"nanos":0},"tags":[],"approved_routes":["10.0.0.0/8","192.168.1.0/24"],"available_routes":["10.0.0.0/8","192.168.1.0/24"]}]
JSON
curl -s -b "$JAR" "$BASE/nodes" -o "$TMPDIR/nodes-page2.html"
awk '/路由审批/{f=1} f' "$TMPDIR/nodes-page2.html" >"$TMPDIR/route-section2.html"
grep -q '192.168.1.0/24' "$TMPDIR/route-section2.html" && fail "批准后该路由仍留在审批队列" || ok "批准后该路由已从审批队列移除"
grep -q '没有待审批的路由' "$TMPDIR/route-section2.html" && ok "队列清空后给出空状态提示" || fail "队列清空后缺少空状态提示"

info "验证节点改名"
# headscale v0.29 的 `nodes rename` 改的是 given_name（受管名 / MagicDNS 名），
# 节点 JSON 里的 name 是客户端上报的主机名。页面必须显示并回显 given_name，
# 否则改完名列表纹丝不动，用户只会看到「改了没生效」。
curl -s -b "$JAR" "$BASE/nodes" -o "$TMPDIR/nodes-rename.html"
grep -q 'office-router' "$TMPDIR/nodes-rename.html" && ok "节点列表显示受管名 given_name" || fail "节点列表没有显示受管名"
grep -q '客户端上报名：router' "$TMPDIR/nodes-rename.html" && ok "客户端上报名单独标注" || fail "缺少客户端上报名标注"
grep -q 'name="name" value="office-router"' "$TMPDIR/nodes-rename.html" && ok "改名输入框预填受管名" || fail "改名输入框没有预填受管名"
# 合法名：命令要带上新名字与节点 ID，页面要给出明确提示。
: >"$TMPDIR/hs-calls.log"
CSRF_N="$(curl -s -b "$JAR" "$BASE/nodes" | grep -o 'name="csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')"
curl -s -b "$JAR" -X POST "$BASE/nodes/rename" --data-urlencode "csrf=${CSRF_N}" --data-urlencode "id=21" --data-urlencode "name=nas-room" -o /dev/null
grep -q -- 'nodes rename nas-room -i 21 --force' "$TMPDIR/hs-calls.log" && ok "改名命令带上了新名字与节点 ID" || fail "改名命令参数不对"
curl -s -b "$JAR" "$BASE/nodes" -o "$TMPDIR/nodes-renamed.html"
grep -q '节点已重命名为 nas-room' "$TMPDIR/nodes-renamed.html" && ok "改名成功后给出明确提示" || fail "改名成功后缺少提示"
# 非法名（中文）：必须在调用 headscale 之前就挡下，不能等命令行报错。
: >"$TMPDIR/hs-calls.log"
CSRF_N="$(curl -s -b "$JAR" "$BASE/nodes" | grep -o 'name="csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')"
curl -s -b "$JAR" -X POST "$BASE/nodes/rename" --data-urlencode "csrf=${CSRF_N}" --data-urlencode "id=21" --data-urlencode "name=书房路由" -o /dev/null
grep -q -- 'nodes rename' "$TMPDIR/hs-calls.log" && fail "非法节点名仍然调用了 headscale" || ok "非法节点名在调用前被拦下"
curl -s -b "$JAR" "$BASE/nodes" -o "$TMPDIR/nodes-badname.html"
grep -q '节点名不合法' "$TMPDIR/nodes-badname.html" && ok "非法节点名给出可读提示" || fail "非法节点名缺少可读提示"

# 仪表盘那一格现在统计的是待审批路由数（原来统计的是有路由的节点数）。
curl -s -b "$JAR" "$BASE/" -o "$TMPDIR/dash.html"
grep -q '待审批路由</div><div class="stat-value">0</div>' "$TMPDIR/dash.html" && ok "仪表盘待审批路由数已归零" || fail "仪表盘待审批路由数不对"

info "验证来源 IP 锁定（放在最后：锁定期间后续登录都会被拒）"
# 每次换一个用户名：账号维度的延迟是按「来源 + 账号」记的，
# 换名字才不会每一步都先等上几秒，几十次失败能很快跑完。
locked=0
for i in $(seq 1 45); do
  code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/login" \
    -d "username=lock-probe-${i}&password=wrong")"
  if [[ "$code" == "429" ]]; then locked=1; break; fi
done
[[ "$locked" == "1" ]] && ok "同一来源累计失败过多后被锁定（429）" || fail "累计失败过多仍未锁定"
# 锁定期间连正确口令也要被拒：锁定必须真的挡住爆破，不能变成凭据探测器。
code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/login" \
  -d 'username=admin&password=testpass123')"
[[ "$code" == "429" ]] && ok "锁定期间正确口令同样被拒（不泄露凭据是否正确）" \
  || fail "锁定期间正确口令未被拒（${code}）"

kill "$SERVER_PID" 2>/dev/null || true
wait "$SERVER_PID" 2>/dev/null || true
SERVER_PID=""

echo
echo "=========================================="
if [[ $FAILS -eq 0 ]]; then
  ok "全部检查通过"
  echo "=========================================="
  exit 0
fi
# 这里不能调用 fail()：它会把 FAILS 再加一，导致退出码变成「失败数 + 1」
# （实测 1 项失败返回 2、3 项失败返回 4），退出码就不能直接当失败数用了。
printf '%s[FAIL]%s 有 %d 项失败\n' "$C_RED" "$C_RESET" "$FAILS"
echo "=========================================="
exit $FAILS
