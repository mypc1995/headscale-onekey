#!/usr/bin/env bash
#
# Headscale 部署脚本的「只读沙盒」验证脚本
# 用途：在 WSL / 任意 Linux 上验证 deploy-headscale.sh 依赖的 CLI 命令与配置键是否真实有效
# 特点：不创建系统用户、不改 systemd、不碰防火墙、不启动服务、不写 /etc
#       所有文件都在临时目录，退出时自动清理
#
# 用法: bash test-headscale-cli.sh [版本号，默认 0.29.7]
#
set -uo pipefail

VERSION="${1:-0.29.7}"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

C_RESET=$'\033[0m'; C_GREEN=$'\033[1;32m'; C_RED=$'\033[1;31m'; C_YELLOW=$'\033[1;33m'
ok()   { printf '%s[PASS]%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
fail() { printf '%s[FAIL]%s %s\n' "$C_RED" "$C_RESET" "$*"; FAILS=$((FAILS+1)); }
info() { printf '%s[INFO]%s %s\n' "$C_YELLOW" "$C_RESET" "$*"; }
FAILS=0

echo "=========================================="
echo " Headscale CLI 沙盒验证 (v${VERSION})"
echo " 临时目录: $TMPDIR"
echo "=========================================="

# ---------- 1. 平台与依赖 ----------
info "检查平台与依赖"
uname -m | grep -qE 'x86_64|aarch64' || fail "不支持的架构: $(uname -m)"
command -v curl >/dev/null || { fail "缺少 curl"; exit 1; }
command -v tar  >/dev/null || { fail "缺少 tar"; exit 1; }
ok "平台: $(uname -s) $(uname -m)"

# ---------- 2. 下载二进制 ----------
ARCH="$(uname -m)"
case "$ARCH" in x86_64) A=amd64;; aarch64) A=arm64;; esac
URL="https://github.com/juanfont/headscale/releases/download/v${VERSION}/headscale_${VERSION}_linux_${A}"
info "下载二进制: $URL"
curl -fsSL --retry 2 "$URL" -o "$TMPDIR/headscale" || { fail "二进制下载失败"; exit 1; }
chmod +x "$TMPDIR/headscale"
HS="$TMPDIR/headscale"
"$HS" version | head -5
ok "二进制可运行"

# ---------- 3. 验证 CLI 子命令存在 ----------
info "验证关键子命令是否存在"
HELP="$("$HS" --help 2>&1)"
for cmd in serve configtest users preauthkeys version; do
  echo "$HELP" | grep -qw "$cmd" && ok "子命令存在: $cmd" || fail "子命令缺失: $cmd"
done

# ---------- 4. 下载并修改配置（模拟部署脚本的 sed）----------
info "下载 v${VERSION} 官方配置示例"
CFG="$TMPDIR/config.yaml"
curl -fsSL "https://raw.githubusercontent.com/juanfont/headscale/v${VERSION}/config-example.yaml" -o "$CFG" || { fail "配置示例下载失败"; exit 1; }

# 校验脚本依赖的扁平键确实存在
info "校验脚本依赖的配置键"
for k in server_url listen_addr metrics_listen_addr disable_check_updates \
         tls_letsencrypt_hostname tls_letsencrypt_challenge_type tls_letsencrypt_listen \
         tls_letsencrypt_cache_dir acme_email tls_cert_path tls_key_path; do
  grep -q "^${k}:" "$CFG" && ok "键存在: $k" || fail "键缺失: $k"
done
for k in enabled region_id region_code region_name stun_listen_addr private_key_path ipv4; do
  grep -q "^    ${k}:" "$CFG" && ok "键存在: derp.$k" || fail "键缺失: derp.$k"
done

# configtest 会真实初始化服务（创建 Noise 私钥、打开 SQLite 库），
# 沙盒是普通用户、无权写 /var/lib/headscale，必须把写盘路径重定向到临时目录。
sandboxify(){ local f="$1"
  sed -i -E "s|^  private_key_path:.*|  private_key_path: ${TMPDIR}/noise_private.key|" "$f"  # noise 块
  sed -i -E "s|^    path: /var/lib/headscale/db.sqlite|    path: ${TMPDIR}/db.sqlite|" "$f"  # database.sqlite
  sed -i -E "s|^    private_key_path:.*|    private_key_path: ${TMPDIR}/derp_private.key|" "$f"  # derp 块
  sed -i -E "s|^  path:.*|  path: ${TMPDIR}/policy.hujson|" "$f"                            # policy 块
}
mkdir -p "$TMPDIR/cache"
printf '{}\n' > "$TMPDIR/policy.hujson"

# ---------- 5. 用真实二进制执行 configtest（最关键验证）----------
info "用真实二进制验证 ACME 模式配置 (configtest)"
set_key(){ local k="$1" v="$2" f="$CFG"; if grep -qE "^${k}:" "$f"; then sed -i -E "s|^${k}:.*|${k}: ${v}|" "$f"; else printf '%s: %s\n' "$k" "$v" >>"$f"; fi; }
set_key "server_url" "https://hs.example.com"
set_key "metrics_listen_addr" '""'
set_key "disable_check_updates" "true"
set_key "listen_addr" "0.0.0.0:443"
set_key "tls_letsencrypt_hostname" "hs.example.com"
set_key "tls_letsencrypt_challenge_type" "HTTP-01"
set_key "tls_letsencrypt_listen" '":http"'
set_key "tls_letsencrypt_cache_dir" "${TMPDIR}/cache"
set_key "acme_email" "admin@example.com"
# base_domain 是 dns: 块下的嵌套键：部署脚本派生为 hs.${DOMAIN}（server 域的子域），这里同步模拟
sed -i -E "s|^  base_domain:.*|  base_domain: hs.hs.example.com|" "$CFG"
sandboxify "$CFG"

if "$HS" configtest -c "$CFG" >"$TMPDIR/ct.log" 2>&1; then
  ok "ACME 模式 configtest 通过"
else
  fail "ACME 模式 configtest 失败:"
  sed 's/^/        /' "$TMPDIR/ct.log"
fi

# ---------- 6. 验证 certbot 模式配置 ----------
info "用真实二进制验证 certbot 模式配置 (configtest)"
CFG2="$TMPDIR/config-certbot.yaml"
curl -fsSL "https://raw.githubusercontent.com/juanfont/headscale/v${VERSION}/config-example.yaml" -o "$CFG2"
set_key2(){ local k="$1" v="$2" f="$CFG2"; if grep -qE "^${k}:" "$f"; then sed -i -E "s|^${k}:.*|${k}: ${v}|" "$f"; else printf '%s: %s\n' "$k" "$v" >>"$f"; fi; }
set_key2 "server_url" "https://hs.example.com"
set_key2 "listen_addr" "0.0.0.0:443"
set_key2 "tls_cert_path" "${TMPDIR}/fullchain.pem"
set_key2 "tls_key_path" "${TMPDIR}/privkey.pem"
set_key2 "tls_letsencrypt_hostname" '""'
sed -i -E "s|^  base_domain:.*|  base_domain: hs.hs.example.com|" "$CFG2"
sandboxify "$CFG2"
if "$HS" configtest -c "$CFG2" >"$TMPDIR/ct2.log" 2>&1; then
  ok "certbot 模式 configtest 通过（configtest 会初始化服务但不校验证书文件是否存在）"
else
  fail "certbot 模式 configtest 失败:"
  sed 's/^/        /' "$TMPDIR/ct2.log"
fi

# ---------- 7. 验证 users/preauthkeys 用法 ----------
info "验证 users / preauthkeys 命令用法"
"$HS" users --help 2>&1 | grep -qw 'create' && ok "users create 存在" || fail "users create 缺失"
"$HS" preauthkeys create --help 2>&1 | grep -q -- '--user' && ok "preauthkeys --user 存在" || fail "preauthkeys --user 缺失"
"$HS" preauthkeys create --help 2>&1 | grep -qi 'uint\|ID' && ok "--user 接受数字 ID" || info "需人工确认 --user 参数类型"

# ---------- 总结 ----------
echo
echo "=========================================="
if [[ $FAILS -eq 0 ]]; then
  ok "全部检查通过 —— deploy-headscale.sh 的命令与配置假设成立"
else
  fail "有 $FAILS 项失败 —— 请根据上述输出修正 deploy-headscale.sh"
fi
echo "=========================================="
exit $FAILS
