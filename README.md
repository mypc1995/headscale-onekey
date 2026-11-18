# headscale-onekey

一键部署私有 [Headscale](https://github.com/juanfont/headscale) 控制服务器，附带自研的全量管理界面 Headscale Admin（Go 单二进制，支持中英文、多套配色、实时网速与网页端配置管理）。

面向 Ubuntu / Debian / CentOS / RHEL / Rocky / AlmaLinux / Fedora，支持 x86_64 与 aarch64。已在 WSL Ubuntu 与 AlmaLinux 9 实机验证；当前脚本按 Headscale v0.29.7 的配置格式编写。

## 功能

- 下载官方二进制、创建系统用户与目录、生成 systemd 服务、配置防火墙。
- 三种接入模式：
  - `http://`：内网明文模式，监听端口从 URL 自动提取。
  - `https://` + `--tls-mode acme`：Headscale 内置 ACME，自动申请与续期证书。
  - `https://` + `--tls-mode certbot`：外部证书 + systemd timer 定时续期，续期后自动重启服务。
- 自动生成 MagicDNS `base_domain`，避免与 `server_url` 域名冲突。
- 内置 `configtest` 配置自检，失败即终止；成功后修正数据目录属主。
- 自动创建默认用户与 24 小时预授权密钥。
- 部署 Headscale Admin 管理界面：中英文一键切换、14 套配色主题、实时网卡流量曲线、完整账号与权限体系，域名/IP/SSL 证书可直接在网页端配置。

## 目录结构

| 文件 | 说明 |
| --- | --- |
| `deploy-headscale.sh` | Headscale 一键部署 |
| `deploy-headscale-admin.sh` | 全量管理界面一键部署（默认 3001） |
| `headscale-admin/` | 全量管理界面 Go 源码、模板与静态资源 |
| `remove-headcontrol.sh` | 从旧版本升级时清理 HeadControl（3000），释放端口 |
| `test-headscale-cli.sh` | Headscale CLI / 配置键只读沙盒验证 |
| `test-headscale-admin.sh` | 全量管理界面编译、登录、双语、主题、网速、配置校验与 CSRF 验证 |

## 快速开始

先克隆仓库并进入目录：

```bash
git clone https://github.com/mypc1995/headscale-onekey.git
cd headscale-onekey
```

### 1. 部署 Headscale

内网 HTTP 模式：

```bash
sudo bash deploy-headscale.sh -u http://192.168.1.10:18080
```

公网 HTTPS 模式，使用内置 ACME：

```bash
sudo bash deploy-headscale.sh -u https://hs.example.com -e admin@example.com --derp
```

公网 HTTPS 模式，使用 certbot：

```bash
sudo bash deploy-headscale.sh -u https://hs.example.com \
  --tls-mode certbot -e admin@example.com --derp
```

主要参数：

| 参数 | 说明 |
| --- | --- |
| `-u, --url URL` | 客户端访问地址，必须以 `http://` 或 `https://` 开头 |
| `-e, --email MAIL` | ACME 注册邮箱；certbot 模式必填 |
| `--tls-mode MODE` | `acme`（HTTPS 默认）、`certbot`、`none`（HTTP 默认） |
| `--base-domain DOM` | MagicDNS 根域；默认自动派生，通常无需手动指定 |
| `--derp` | 启用内置 DERP 中继，公网环境建议开启 |
| `-v, --version VER` | 指定 Headscale 版本，默认 `0.29.7` |

部署后可用 `headscale-cert status|renew|reload` 查看和管理证书。注意：v0.29.x 的 SIGHUP 只重载 ACL，不重载 TLS 证书；证书更新请使用 `renew` 或重启服务。

### 2. 部署管理界面（Headscale Admin）

Go 单二进制，中英文界面，默认监听 3001，内置账号体系，不需要额外装 Node 或 Python。

```bash
sudo bash deploy-headscale-admin.sh --port 3001
```

首次部署会生成随机管理员密码并打印一次；也可指定：

```bash
sudo bash deploy-headscale-admin.sh --port 3001 \
  --admin-user admin --admin-pass 'YourStrongPassword'
```

重新部署默认保留已有密码；需要重置时加 `--reset-admin`。限制来源用 `--allow-cidr`，不修改防火墙用 `--no-firewall`。

功能覆盖：

- 总览：用户数、节点数、在线数、路由数、密钥数、服务状态、证书到期。
- 节点：列表、重命名、过期、删除、标签、路由审批。
- 用户：创建、重命名、删除，支持显示名与邮箱。
- 密钥与接入：预授权密钥生成/失效/删除，API key 生成/失效/删除，客户端一键接入命令。有效期改为下拉选择：1 小时、24 小时、7 天、30 天、90 天、1 年、长期（100 年，等效永不过期）。
  - 一键接入卡片：`server_url` 自动读取当前配置（绑定 IP/域名并保存后即生效），新生成的预授权密钥自动填入 `key`，页面实时拼出 `tailscale up --login-server=<server_url> --authkey=<key>`，一键复制。地址或密钥未就绪时命令显示占位提示且复制按钮禁用，避免复制到半截命令。
  - 密钥默认打码显示，点眼睛图标切换显示/隐藏；列表中每条密钥可单独复制，或点「填入命令」直接用于接入卡片。新密钥只展示一次（按会话一次性暂存，刷新后不再带出），密钥不出现在 URL 与日志中。
- 网络与策略：ACL 策略编辑、DNS/MagicDNS、DERP 开关、完整配置编辑；保存前自动备份并做 `configtest` 校验。
- 运维与监控：本机 CPU/内存/负载/运行时长（3 秒采样、3 分钟曲线）、节点在线状态与握手/版本/到期/路由（15 秒采样）、阈值告警、服务状态、版本、证书到期、最近日志、重启服务，以及每 3 秒刷新的实时网速（读取 `/proc/net/dev`，含上下行速率、累计流量、各网卡明细与近期曲线；非 Linux 自动降级为不可用提示）。
- 管理员：管理员增删改、角色（admin/readonly）、修改密码。
- 配置管理：域名、端口与网段、HTTPS 证书、证书自动续期集中在网页端，见下节。

#### 配置管理页（`/settings`）

把 Headscale 常用配置集中到网页端，避免手改 YAML。页面分四块，每块独立保存、互不影响。表单上用通俗名称描述用途，名称旁边的小标签是原始配置键，方便对照 YAML 和官方文档；键名本身不做任何改动。

**域名与访问地址**

| 页面名称 | 配置键 | 校验规则 |
| --- | --- | --- |
| 对外访问地址 | `server_url` | 必填；只允许 `http://` / `https://`；不能带路径、参数、锚点或结尾 `/`；协议必须与证书方式匹配 |
| 设备域名后缀 | `dns.base_domain` | 必填；至少两级标签；不能是纯 IP；不能与 `server_url` 域名相同 |
| 启用设备名互访（MagicDNS） | `dns.magic_dns` | 开关；勾选后设备可直接用设备名互访 |

**端口与网段**

| 页面名称 | 配置键 | 校验规则 |
| --- | --- | --- |
| 客户端连接端口 | `listen_addr` | 必填；`IP:端口`，端口 1–65535 |
| 本机管理端口 | `grpc_listen_addr` | 必填；格式同上 |
| 监控数据端口 | `metrics_listen_addr` | 可留空；格式同上 |
| 设备 IPv4 网段 | `prefixes.v4` | 必填；CIDR 且必须位于 `100.64.0.0/10` 内 |
| 设备 IPv6 网段 | `prefixes.v6` | 可留空；CIDR，掩码不能大于 /64 |

同一表单内端口重复会被直接拦截。`server_url` 端口与监听端口不一致时给出警告，避免改端口时两边互相锁死。

**HTTPS 证书**

三种方式，切换后只写入当前方式需要的键：

- **自动申请（Let's Encrypt）**：证书域名（必填 FQDN）、证书到期通知邮箱（可选，填了必须合法）、域名验证方式（HTTP-01 / TLS-ALPN-01）、验证时临时监听的地址、证书缓存目录。
- **手动粘贴已有证书**：证书和私钥都以 PEM 文本粘贴，不涉及文件上传，也不需要填写服务器路径。保存时先解析两段内容、用 `tls.X509KeyPair` 校验配对，通过后写入 `/etc/headscale/certs/manual.crt` 与 `manual.key`（0600，属主 `headscale`），再把这两个路径写进 `tls_cert_path` / `tls_key_path`。已经配好、这次不更换时两项留空即可沿用原证书；粘贴的内容不会回显到页面。
- **不启用 HTTPS**：清空 ACME 域名与证书路径。

页面顶部解析当前证书，显示来源（自动申请 / 手动粘贴）、CN、颁发者、生效与到期时间、剩余天数、SAN、私钥是否匹配。到期 30 天内或私钥不匹配会显示警告。

**证书自动更新**

- 内置 ACME：显示由 headscale 进程自动续期，不需要额外定时任务。
- certbot：显示 `certbot.timer` 的启用与运行状态、上次/下次执行时间、证书名；可启停定时器、设置每日检查时间（写入 `certbot.timer.d/override.conf` 后 `daemon-reload`），也可立即续期（执行 `certbot renew` 后重启 headscale）。
- 未安装 certbot 时显示安装提示，不展示不可用的按钮。

**保存与应用流程**

1. 服务端逐字段校验；失败时原样回显并标红出错字段，不写任何文件。
2. 基于当前 YAML 生成候选配置，写入 `/etc/headscale/.config-candidate.yaml`。
3. 执行 `headscale configtest -c` 校验；失败则删除候选文件并返回完整错误，原配置保持不动。
4. 备份为 `config.yaml.bak.<时间戳>`，再原子替换正式配置。
5. 勾选「保存后立即重启」时执行 `systemctl restart headscale`，等待进程 active 且监听端口可连接；失败自动回滚到备份并再次重启。
6. 成功后页面顶部提示结果与备份文件名。

手动粘贴证书时，证书文件先落盘再写入配置；若后面的 `configtest` 或重启失败，证书文件会随配置一起还原成上一份。

修改监听端口或 `server_url` 会短暂中断客户端连接；切换 HTTP/HTTPS 需要两侧配置配套修改，页面会在不一致时给出警告。

界面支持中英文切换：顶部地球图标可切换语言，选择保存在 Cookie 中，登录页也能切换。主题下拉提供 14 套配色（浅色、深色、赛博朋克、海洋、日落、森林、午夜、德古拉、北欧、玫瑰、咖啡、终端、柔和、合成波），选择保存在浏览器本地，界面采用暖色底、粗边框、硬阴影的新粗野主义风格，图标全部内联，不依赖外部 CDN。

认证采用独立账号体系：密码用 PBKDF2-HMAC-SHA256（12 万次迭代）加盐保存，会话使用 HttpOnly Cookie，所有表单带 CSRF token，只读角色无法提交修改。

管理员账号与会话都存放在 SQLite（`/var/lib/headscale-admin/headscale-admin.db`，权限 0600），服务重启后登录状态仍然保留。首次启动时如果检测到旧版 `admins.json`，会自动迁移并把原文件改名为 `admins.json.migrated`，原密码继续可用。SQLite 驱动为纯 Go 实现（`modernc.org/sqlite`），编译不需要 gcc 或 CGO。

源码目录为 `headscale-admin/`，不依赖任何未声明许可证的第三方项目。

#### 运维监控页（`/ops`）

**本机资源**：每 3 秒读取 `/proc/stat`、`/proc/meminfo`、`/proc/loadavg`、`/proc/uptime`，展示 CPU 使用率、内存使用率（已用/总量）、系统负载（1/5/15 分钟）、运行时长和最近 3 分钟的 CPU/内存曲线。CPU 使用率由两次采样的累计 tick 差值算出，首次采样显示 0；内存使用率按 `MemTotal - MemAvailable` 计算，老内核没有 `MemAvailable` 时退化为 `MemFree + Buffers + Cached`。非 Linux 环境没有 `/proc`，页面显示不可用提示，不影响其他功能。

**节点监控**：后台每 15 秒执行一次 `headscale nodes list -o json`，缓存结果并展示节点总数、在线、离线、密钥过期数量，以及每个节点的名称、所属用户、虚拟 IP、在线状态、实时速度（下行/上行）、最后握手时间、客户端版本、系统、密钥到期、标签与路由数量。状态分在线、离线、密钥过期三种，密钥过期优先显示。CLI 读取失败时保留上一次成功的数据，并在卡片顶部提示「显示的是上一次成功采集的结果」。表格底部标注数据更新时间与采样周期。

**节点实时速度**：节点间流量走 WireGuard 直连或 DERP，不经过控制服务器，headscale 的 CLI 与 Prometheus 指标都没有按节点的流量计数器，因此网速数据源是可选的——配置环境变量 `HSA_NODE_SPEED_FILE` 指向一个计数器 JSON 文件，由节点侧采集代理定期写入累计收发字节数，服务端按两次采样差值换算速率（与 CPU 利用率的 tick 差值同一思路），单位自动按 B/s、KB/s、MB/s 展示。文件格式按节点 ID 或节点名索引：

```json
{
  "12": {"rx_bytes": 154600, "tx_bytes": 31220},
  "laptop": {"rx_bytes": 1000, "tx_bytes": 500}
}
```

未配置该变量、文件不存在或内容无法解析时，每个节点的速度列显示占位「—」，鼠标悬停可见原因说明，其余监控数据不受影响。计数器回退（节点或代理重启）时当轮速率按 0 处理，不会出现负值。速率口径与实时网速面板一致，均为 KB/s 基准。

节点侧能采集到的只有控制面指标。Tailscale 客户端不会把 CPU、内存等主机资源上报给 headscale，因此各节点的 CPU/内存不在这个页面的能力范围内；如果确有需要，应在节点上部署采集 agent，把数据推到新增接口，按下面的扩展方式接入。

**告警**：内置 7 条规则——headscale 服务不是 active、CPU 超阈值、内存超阈值、1 分钟负载超过核数×倍率、节点离线、节点密钥过期、节点数据采集失败。阈值可用环境变量覆盖：

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `HSA_ALERT_CPU` | 85 | CPU 使用率百分比阈值 |
| `HSA_ALERT_MEM` | 90 | 内存使用率百分比阈值 |
| `HSA_ALERT_LOAD` | 2 | 1 分钟负载上限 = CPU 核数 × 该倍率 |

**接口**：`GET /api/monitor` 需要登录，返回 `host`、`nodes`、`alerts`、`thresholds` 四部分，页面每 5 秒轮询一次；节点数据复用服务端 15 秒缓存，多开页面不会频繁调用 CLI。

**扩展方式**：

- 新增本机指标：在 `hostmon.go` 的 `HostSnapshot` 加字段并在 `sample()` 里填充（例如读 `/proc/mounts` 统计磁盘占用），`newHostView` 补展示文案，前端加一个进度条。
- 新增节点指标：在 `nodemont.go` 的 `nodeStatFromMap` 里从 CLI JSON 取字段并加入 `NodeStat`，模板与 `static/app.js` 的 `nodeRow` 各加一列；需要差值换算的计数器类指标（如网速）仿照 `nodeSpeedRates` 增加数据源即可。
- 新增告警规则：在 `alerts.go` 实现 `func(alertInput, AlertThresholds) []MonitorAlert`，注册到 `alertRules` 切片，页面与接口输出不需要改动；阈值加到 `AlertThresholds` 并在 `defaultAlertThresholds` 里读环境变量。
- 接通知渠道：`MonitorAlert` 的 `Key` 与 `Level` 已固定，可在规则求值后按 key 去重，再推送到邮件或 Webhook。

## 验证

在任意 Linux（包括 WSL）上可运行只读验证，不会创建用户、写 `/etc` 或启动服务：

```bash
bash test-headscale-cli.sh        # 验证 CLI 命令、配置键与 configtest
bash test-headscale-admin.sh      # 验证管理界面编译、登录、双语、主题、网速、本机与节点监控、配置校验与 CSRF（需 go/curl）
(cd headscale-admin && go test ./...)   # 解析、节点映射、双语展示与告警规则单元测试
```

## 安全提示

- 管理界面走独立账号体系，密码经 PBKDF2 加盐摘要保存，登录态为 HttpOnly Cookie；HTTP 模式下传输仍是明文，公网使用请配合 HTTPS 或 `--allow-cidr` 限制来源。
- HTTP 模式为明文，较新的 Tailscale 客户端可能需要设置 `TS_ALLOW_HTTP_CONTROL_SERVER=1`。
- HTTPS 模式需要域名正确解析到本机，并确保 80（证书验证）和 443（服务）端口可达。
- 启用内置 DERP 时需放行 UDP/3478。

## 从旧版本升级

早期版本额外部署过社区项目 HeadControl（3000 端口）。现在管理功能已全部由 Headscale Admin 覆盖，不需要再保留两套界面：

```bash
sudo bash remove-headcontrol.sh          # 停服务、删程序/数据、放开 3000、清理防火墙规则
sudo bash remove-headcontrol.sh --keep-data   # 保留 /var/lib/headcontrol
```

脚本只清理 HeadControl，不动 Headscale、Headscale Admin 与 `/etc/headscale/config.yaml`。历史版本仍可在 git 提交记录中找回。

## 许可证

本仓库中的脚本、文档与管理界面源码采用 [MIT License](LICENSE)。
