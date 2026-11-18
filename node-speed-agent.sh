#!/usr/bin/env bash
# 节点侧网速采集代理
#
# 节点间流量走 WireGuard 直连或 DERP，不经过控制服务器，headscale 的 CLI 与
# Prometheus 指标都没有按节点的流量计数器。所以每个节点自己采集 Tailscale 网卡
# 的累计收发字节数，回连管理界面上报；管理界面按两次上报的差值换算速率，在
# 「运维监控 → 节点监控」的速度列显示。
#
# 用法：
#   HSA_URL=http://<服务器tailnet地址>:3001/api/speed \
#   HSA_TOKEN=<配置项页显示的上报令牌> \
#   bash node-speed-agent.sh
#
# 可选环境变量：
#   HSA_IFACE     采集的网卡，默认自动找 tailscale* 接口
#   HSA_INTERVAL  上报间隔秒数，默认 10（建议不超过 15，与管理界面采样周期匹配）
#   HSA_NODE      节点标识，默认用本机 tailnet IP（管理界面按 IP 匹配节点）
#   HSA_ONCE      设为 1 时只上报一次就退出（用于自检）
#
# 建议以 systemd 常驻：
#   [Unit]
#   Description=Headscale node speed reporter
#   After=network-online.target tailscaled.service
#   [Service]
#   Environment=HSA_URL=http://<服务器tailnet地址>:3001/api/speed
#   Environment=HSA_TOKEN=<上报令牌>
#   EnvironmentFile=-/etc/headscale-admin/node-speed-agent.env
#   ExecStart=/usr/local/bin/node-speed-agent.sh
#   Restart=always
#   RestartSec=5
#   [Install]
#   WantedBy=multi-user.target

set -uo pipefail

URL="${HSA_URL:-}"
TOKEN="${HSA_TOKEN:-}"
IFACE="${HSA_IFACE:-}"
INTERVAL="${HSA_INTERVAL:-10}"
NODE="${HSA_NODE:-}"
ONCE="${HSA_ONCE:-0}"

die() { echo "$*" >&2; exit 1; }

[[ -n "$URL" ]] || die "需要设置 HSA_URL（上报地址，如 http://100.64.0.1:3001/api/speed）"
[[ -n "$TOKEN" ]] || die "需要设置 HSA_TOKEN（上报令牌，在管理界面配置项页可见）"
command -v curl >/dev/null 2>&1 || die "需要 curl"

if ! [[ "$INTERVAL" =~ ^[0-9]+$ ]] || [[ "$INTERVAL" -lt 1 ]]; then
  die "HSA_INTERVAL 必须是正整数秒"
fi

# detect_iface 自动找 tailscale* 网卡；安装路径与网卡名都可能不同，
# 所以不写死名字，按前缀匹配。
detect_iface() {
  awk -F: 'NR>2 { gsub(/[ \t]/, "", $1); if ($1 ~ /^tailscale/) print $1 }' /proc/net/dev | head -1
}

if [[ -z "$IFACE" ]]; then
  IFACE="$(detect_iface)"
  [[ -n "$IFACE" ]] || die "未找到 tailscale* 网卡，请用 HSA_IFACE 指定采集的网卡"
fi
[[ -f /proc/net/dev ]] || die "需要 /proc/net/dev（Linux）"

# detect_node 取本机 tailnet IP 作为节点标识。管理界面会按节点 ID、节点名、
# tailnet IP 三种方式匹配，用 IP 最省事也最稳定。
detect_node() {
  if command -v tailscale >/dev/null 2>&1; then
    local ip
    ip="$(tailscale ip -4 2>/dev/null | head -1)"
    if [[ -n "$ip" ]]; then
      echo "$ip"
      return 0
    fi
  fi
  if command -v ip >/dev/null 2>&1; then
    ip -4 -o addr show dev "$IFACE" 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1
  fi
}

if [[ -z "$NODE" ]]; then
  NODE="$(detect_node)"
  [[ -n "$NODE" ]] || die "无法确定节点标识，请用 HSA_NODE 指定（节点 ID、名称或 tailnet IP）"
fi
# 节点标识会拼进 JSON，只允许不含引号与反斜杠的字符。
[[ "$NODE" =~ ^[A-Za-z0-9._:-]+$ ]] || die "HSA_NODE 只能包含字母、数字与 . _ : -"

# read_counters 输出该网卡的累计收发字节数。
# /proc/net/dev 每行的字段：网卡名 rx字节 rx包 ... rx组播 tx字节 ...
read_counters() {
  awk -v dev="$IFACE" '
    {
      line = $0
      sub(/^[ \t]+/, "", line)
      idx = index(line, ":")
      if (idx == 0) next
      name = substr(line, 1, idx - 1)
      if (name != dev) next
      n = split(line, f, /[ \t]+/)
      if (n >= 10) { print f[2], f[10]; exit }
    }
  ' /proc/net/dev
}

report() {
  local rx="$1" tx="$2" code
  code="$(curl -sS -m 10 -o /dev/null -w '%{http_code}' \
    -H "X-Node-Speed-Token: ${TOKEN}" \
    -H 'Content-Type: application/json' \
    --data-binary "{\"node\":\"${NODE}\",\"rx_bytes\":${rx},\"tx_bytes\":${tx}}" \
    "$URL" 2>/dev/null)"
  if [[ "$code" != "200" ]]; then
    echo "上报失败（HTTP ${code:-000}）" >&2
    return 1
  fi
  return 0
}

echo "节点网速采集已启动：网卡=${IFACE} 节点=${NODE} 间隔=${INTERVAL}s 地址=${URL}"

while true; do
  counters="$(read_counters)"
  if [[ -n "$counters" ]]; then
    read -r rx tx <<<"$counters"
    if [[ -n "${rx:-}" && -n "${tx:-}" ]]; then
      report "$rx" "$tx" || true
    fi
  else
    echo "读取 ${IFACE} 计数器失败" >&2
  fi
  [[ "$ONCE" == "1" ]] && break
  sleep "$INTERVAL"
done
