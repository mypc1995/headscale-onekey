# headscale-onekey

一键部署私有 [Headscale](https://github.com/juanfont/headscale) 控制服务器，可选择部署社区项目 HeadControl，或本项目的全量管理界面 Headscale Admin（支持中英文、多套配色与实时网速）。

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
- 可选部署 HeadControl，默认直接开放 3000 端口，并自动完成 API key 初始化。
- HeadControl 默认启用 HTTP Basic Auth：未指定密码时自动生成，凭据以带盐摘要存入仅 root 可读的 systemd 环境文件。
- HeadControl 默认应用中文补丁：页面、菜单、按钮、前端提示、后端 toast 与错误信息均已汉化。
- 可选部署 Headscale Admin：中英文一键切换、14 套配色主题、实时网卡流量曲线、完整账号与权限体系。

## 目录结构

| 文件 | 说明 |
| --- | --- |
| `deploy-headscale.sh` | Headscale 一键部署 |
| `deploy-headcontrol.sh` | HeadControl 一键部署与汉化 |
| `headcontrol-zh.patch` | HeadControl 中文补丁，针对固定上游提交 |
| `test-headscale-cli.sh` | Headscale CLI / 配置键只读沙盒验证 |
| `deploy-headscale-admin.sh` | 全量管理界面一键部署（默认 3001） |
| `headscale-admin/` | 全量管理界面 Go 源码、模板与静态资源 |
| `test-headcontrol-patch.sh` | 汉化补丁、Basic Auth 应用与编译验证 |
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

### 2. 部署 HeadControl 中文管理界面

```bash
sudo bash deploy-headcontrol.sh --hs-url http://127.0.0.1:18080
```

默认直接放行 TCP/3000，浏览器访问 `http://<服务器IP>:3000` 会弹出登录框。未指定密码时会交互输入；非交互执行则自动生成随机密码并在结束时打印一次，同时保存到 `/root/.headcontrol-auth`（0600）。密码哈希与盐写入 `/etc/headcontrol/auth.env`（0600），明文不出现在 systemd unit 中。

自定义账号：

```bash
sudo bash deploy-headcontrol.sh --hs-url http://127.0.0.1:18080 \
  --auth-user ops --auth-pass 'YourStrongPassword'
```

如需限制来源：

```bash
sudo bash deploy-headcontrol.sh --hs-url http://127.0.0.1:18080 \
  --allow-cidr 192.168.1.0/24
```

可选参数：`--port 3000`、`--no-firewall`、`--no-zh`（使用原版英文界面）、`--no-auth`（关闭密码保护，不推荐）。

注意：HTTP Basic Auth 在明文 HTTP 下凭据只是 Base64 编码，公网使用应配合 HTTPS 或至少用 `--allow-cidr` 限制来源。

### 3. 部署全量管理界面（Headscale Admin）

HeadControl 比较轻量。如果需要完整管理界面，可部署本项目的 `headscale-admin`：Go 单二进制，中英文界面，默认跑在 3001，可与 3000 的 HeadControl 并行。

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
- 密钥与接入：预授权密钥生成/失效/删除，API key 生成/失效/删除，客户端接入命令。有效期改为下拉选择：1 小时、24 小时、7 天、30 天、90 天、1 年、长期（100 年，等效永不过期）。
- 网络与策略：ACL 策略编辑、DNS/MagicDNS、DERP 开关、完整配置编辑；保存前自动备份并做 `configtest` 校验。
- 运维与监控：服务状态、版本、证书到期、最近日志、重启服务，以及每 3 秒刷新的实时网速（读取 `/proc/net/dev`，含上下行速率、累计流量、各网卡明细与近期曲线；非 Linux 自动降级为不可用提示）。
- 管理员：管理员增删改、角色（admin/readonly）、修改密码。
- 配置管理：域名、IP 与监听、SSL 证书、证书自动续期集中在网页端，见下节。

#### 配置管理页（`/settings`）

把 Headscale 常用配置集中到网页端，避免手改 YAML。页面分四块，每块独立保存、互不影响。

**域名与接入**

| 字段 | 配置键 | 校验规则 |
| --- | --- | --- |
| 对外访问地址 | `server_url` | 必填；只允许 `http://` / `https://`；不能带路径、参数、锚点或结尾 `/`；协议必须与证书模式匹配 |
| MagicDNS 根域 | `dns.base_domain` | 必填；至少两级标签；不能是纯 IP；不能与 `server_url` 域名相同 |
| 启用 MagicDNS | `dns.magic_dns` | 开关 |

**IP 与监听**

| 字段 | 配置键 | 校验规则 |
| --- | --- | --- |
| HTTP 监听地址 | `listen_addr` | 必填；`IP:端口`，端口 1–65535 |
| 指标监听地址 | `metrics_listen_addr` | 可留空；格式同上 |
| gRPC 监听地址 | `grpc_listen_addr` | 必填；格式同上 |
| 客户端 IPv4 段 | `prefixes.v4` | 必填；CIDR 且必须位于 `100.64.0.0/10` 内 |
| 客户端 IPv6 段 | `prefixes.v6` | 可留空；CIDR，掩码不能大于 /64 |

同一表单内端口重复会被直接拦截。`server_url` 端口与监听端口不一致时给出警告，避免改端口时两边互相锁死。

**SSL 证书**

三种模式，切换时会自动清空不适用的键：

- **内置 ACME**：证书域名（必填 FQDN）、ACME 邮箱（可选，填了必须合法）、校验方式（HTTP-01 / TLS-ALPN-01）、HTTP-01 监听地址、证书缓存目录。
- **手动证书**：证书与私钥必须是绝对路径且文件存在，保存时用 `tls.LoadX509KeyPair` 校验两者配对。
- **仅 HTTP**：清空 ACME 域名与证书路径。

页面顶部解析当前证书，显示来源（ACME 缓存 / 手动）、CN、颁发者、生效与到期时间、剩余天数、SAN、私钥是否匹配。到期 30 天内或私钥不匹配会显示警告。

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

修改监听端口或 `server_url` 会短暂中断客户端连接；切换 HTTP/HTTPS 需要两侧配置配套修改，页面会在不一致时给出警告。

界面支持中英文切换：顶部地球图标可切换语言，选择保存在 Cookie 中，登录页也能切换。主题下拉提供 14 套配色（浅色、深色、赛博朋克、海洋、日落、森林、午夜、德古拉、北欧、玫瑰、咖啡、终端、柔和、合成波），选择保存在浏览器本地，界面采用暖色底、粗边框、硬阴影的新粗野主义风格，图标全部内联，不依赖外部 CDN。

认证采用独立账号体系：密码用 PBKDF2-HMAC-SHA256（12 万次迭代）加盐保存，会话使用 HttpOnly Cookie，所有表单带 CSRF token，只读角色无法提交修改。

管理员账号与会话都存放在 SQLite（`/var/lib/headscale-admin/headscale-admin.db`，权限 0600），服务重启后登录状态仍然保留。首次启动时如果检测到旧版 `admins.json`，会自动迁移并把原文件改名为 `admins.json.migrated`，原密码继续可用。SQLite 驱动为纯 Go 实现（`modernc.org/sqlite`），编译不需要 gcc 或 CGO。

源码目录为 `headscale-admin/`，不依赖任何未声明许可证的第三方项目。

## 验证

在任意 Linux（包括 WSL）上可运行只读验证，不会创建用户、写 `/etc` 或启动服务：

```bash
bash test-headscale-cli.sh        # 验证 CLI 命令、配置键与 configtest
bash test-headcontrol-patch.sh    # 验证汉化补丁、Basic Auth 与 Go 编译（需 git/go/gcc/curl/openssl）
bash test-headscale-admin.sh      # 验证管理界面编译、登录、双语、主题、网速、配置校验与 CSRF（需 go/curl）
```

## 安全提示

- HeadControl 默认启用 Basic Auth，但 HTTP 下凭据仍是 Base64 明文；生产环境建议配合 HTTPS、反向代理认证或网络 ACL。可用 `--no-auth` 显式关闭，但不推荐。
- HTTP 模式为明文，较新的 Tailscale 客户端可能需要设置 `TS_ALLOW_HTTP_CONTROL_SERVER=1`。
- HTTPS 模式需要域名正确解析到本机，并确保 80（证书验证）和 443（服务）端口可达。
- 启用内置 DERP 时需放行 UDP/3478。
- HeadControl 是社区项目，不是 Headscale 官方组件。

## 许可证

本仓库中的脚本与文档采用 [MIT License](LICENSE)。

`headcontrol-zh.patch` 是面向第三方项目 HeadControl 的本地化补丁。HeadControl 上游仓库目前未声明许可证，本仓库不分发其源码；是否使用该补丁由使用者自行判断，使用时请同时尊重上游项目的权利声明。详见 [NOTICE](NOTICE)。
