# headscale-onekey

<p align="center">
  <img src="docs/badges/license.svg" alt="License: MIT">
  <img src="docs/badges/headscale.svg" alt="Headscale v0.29.7">
  <img src="docs/badges/go.svg" alt="Go 1.26">
  <img src="docs/badges/platform.svg" alt="Platform: Linux and Windows Server">
  <img src="docs/badges/arch.svg" alt="x86_64 and aarch64">
  <img src="docs/badges/shell.svg" alt="Shell: Bash">
  <img src="docs/badges/sqlite.svg" alt="Pure Go SQLite">
  <img src="docs/badges/dependencies.svg" alt="No external dependencies">
  <img src="docs/badges/i18n.svg" alt="i18n">
</p>

<p align="center">
  <b>一条命令装好私有 Headscale，再配一个能管到底的网页控制台。</b><br>
  <sub>自带管理界面 · 零外部依赖 · 中文 / English · 14 套配色</sub>
</p>

<p align="center">
  <b>简体中文</b> · <a href="README_EN.md">English</a>
</p>

---

## 这是什么

`headscale-onekey` 把「部署私有 [Headscale](https://github.com/juanfont/headscale) 控制服务器」压缩成一条命令：下载官方二进制、创建系统用户与目录、生成 systemd 服务、配置防火墙、做配置自检（失败即终止，成功后修正数据目录属主），并自动派生 MagicDNS 根域（避免与 `server_url` 域名冲突）、创建默认用户与 24 小时预授权密钥。

配套的 **Headscale Admin** 是自研的全量管理界面——**Go 单二进制**，编译后拷过去就能跑，不需要 Node、Python、数据库服务或任何外部 CDN。节点、用户、密钥、DNS、DERP、ACL、证书、监控、端口网段，都能在网页上改，改之前自动备份并跑 `configtest` 兜底。

```bash
git clone https://github.com/mypc1995/headscale-onekey.git
cd headscale-onekey

sudo bash deploy-headscale.sh -u http://192.168.1.10:18080   # 1. 控制服务器
sudo bash deploy-headscale-admin.sh --port 3001             # 2. 管理界面
```

> 支持 Ubuntu / Debian / CentOS / RHEL / Rocky / AlmaLinux / Fedora，x86_64 与 aarch64；管理界面另有 Windows Server 版本（见「Windows Server 部署」）。
> 已在 WSL Ubuntu 与 AlmaLinux 9 实机验证；脚本按 Headscale v0.29.7 的配置格式编写。

## 特性

| | 模块与能力 |
| :---: | --- |
| 🚀 | **一键部署** — 官方二进制、系统用户、数据目录、systemd 服务、防火墙规则、`configtest` 自检，一条命令完成 |
| 🔐 | **三种接入模式** — `http://` 内网明文（端口从 URL 自动提取）· `https://` + `--tls-mode acme`（内置 ACME 自动续期）· `https://` + `--tls-mode certbot`（外部证书 + timer 续期后自动重启） |
| 📦 | **零依赖控制台** — Go 单二进制 + 内联图标 + 纯 Go SQLite 驱动，无 CGO、无外部 CDN |
| 🧩 | **全量网页配置** — 域名、端口、网段、DNS、DERP、ACL、TLS 证书、证书续期全部图形化，保存前备份 + `configtest` |
| 📈 | **实时监控** — 本机 CPU/内存/负载/运行时长 + 节点在线/握手/版本/到期/路由 + 每 3 秒网速曲线 + 7 条告警规则 |
| 🌐 | **节点级网速** — 节点侧 agent 上报累计流量，服务端按差值换算速率（控制面本身没有这个计数器） |
| 🛡️ | **安全基线** — PBKDF2 口令摘要、HttpOnly 会话、全表单 CSRF、登录限速、撤权即吊销会话、**公网登录图形验证码**、**全站安全响应头与 CSP** |
| ⚡ | **卡不死的控制面** — 外部命令全部带超时与可取消闸门，headscale 抽风时界面不会整体僵住 |
| 🎨 | **双语与主题** — 中文 / English 一键切换，14 套配色，选择保存在 Cookie / 浏览器本地 |

<details>
<summary><b>目录</b></summary>

- [这是什么](#这是什么)
- [特性](#特性)
- [仓库结构](#仓库结构)
- [快速开始](#快速开始)
  - [1. 部署 Headscale](#1-部署-headscale)
  - [2. 部署管理界面](#2-部署管理界面)
- [管理界面](#管理界面)
  - [页面一览](#页面一览)
  - [配置项](#配置项)
  - [网络与策略](#网络与策略)
  - [运维监控](#运维监控)
- [安全设计](#安全设计)
  - [登录安全](#登录安全)
  - [传输与响应头](#传输与响应头)
  - [共享出口与可信来源](#共享出口与可信来源)
- [可靠性设计](#可靠性设计)
- [节点网速上报](#节点网速上报)
- [验证](#验证)
- [安全提示](#安全提示)
- [许可证](#许可证)

</details>

## 仓库结构

| 文件 | 说明 |
| --- | --- |
| `deploy-headscale.sh` | Headscale 一键部署 |
| `deploy-headscale-admin.sh` | 管理界面一键部署（默认端口 3001） |
| `deploy-headscale-admin.ps1` | 管理界面一键部署（Windows Server 版，默认端口 3001） |
| `headscale-admin/` | 管理界面 Go 源码、模板与静态资源 |
| `node-speed-agent.sh` | 节点侧网速采集代理，把本机 Tailscale 流量计数上报到管理界面 |
| `test-headscale-cli.sh` | Headscale CLI / 配置键只读沙盒验证 |
| `test-headscale-admin.sh` | 管理界面编译、登录、验证码、双语、主题、网速、配置校验与 CSRF 验证 |
| `test-headscale-admin-tls.sh` | 管理界面 HTTPS 全流程验证（隔离实例，不碰生产服务） |

## 快速开始

### 1. 部署 Headscale

**内网 HTTP**

```bash
sudo bash deploy-headscale.sh -u http://192.168.1.10:18080
```

**公网 HTTPS，内置 ACME**

```bash
sudo bash deploy-headscale.sh -u https://hs.example.com -e admin@example.com --derp
```

**公网 HTTPS，certbot**

```bash
sudo bash deploy-headscale.sh -u https://hs.example.com \
  --tls-mode certbot -e admin@example.com --derp
```

**主要参数**

| 参数 | 说明 |
| --- | --- |
| `-u, --url URL` | 客户端访问地址，必须以 `http://` 或 `https://` 开头 |
| `-e, --email MAIL` | ACME 注册邮箱；certbot 模式必填 |
| `--tls-mode MODE` | `acme`（HTTPS 默认）、`certbot`、`none`（HTTP 默认） |
| `--base-domain DOM` | MagicDNS 根域；默认自动派生，通常无需手动指定 |
| `--derp` | 启用内置 DERP 中继，公网环境建议开启 |
| `-v, --version VER` | 指定 Headscale 版本，默认 `0.29.7` |
| `--prefix-v4 CIDR` | 虚拟网段，默认官方示例的 `100.64.0.0/10`，例如 `10.0.0.0/24` |
| `--public-ip IP` | 显式指定公网 IP；NAT 后面自动探测会拿到内网地址，内置 DERP 需要它 |
| `--binary PATH` | 用本地已有的 headscale 二进制，不从 GitHub 下载（离线部署） |
| `--config PATH` | 用本地已有的官方配置示例，不从 GitHub 下载（离线部署） |

部署后可用 `headscale-cert status|renew|reload` 查看和管理证书。

> **注意**：v0.29.x 的 SIGHUP 只重载 ACL，不重载 TLS 证书；证书更新请使用 `renew` 或重启服务。

**离线部署**（服务器连不上 GitHub 时——国内机器常见）：在能上网的机器上备好文件再上传。

```bash
# 在能上网的机器上
curl -fLO https://github.com/juanfont/headscale/releases/download/v0.29.7/headscale_0.29.7_linux_amd64
curl -fsSL https://raw.githubusercontent.com/juanfont/headscale/v0.29.7/config-example.yaml -o config-example.yaml
scp headscale_0.29.7_linux_amd64 config-example.yaml deploy-headscale.sh 服务器:~/

# 在服务器上（全程不访问 GitHub）
sudo bash deploy-headscale.sh -u http://<公网IP>:18080 --tls-mode none \
  --binary ~/headscale_0.29.7_linux_amd64 --config ~/config-example.yaml \
  --prefix-v4 10.0.0.0/24 --public-ip <公网IP>
```

### 2. 部署管理界面

默认监听 3001，内置账号体系。首次部署生成随机管理员密码并打印一次：

```bash
sudo bash deploy-headscale-admin.sh --port 3001
```

也可直接指定管理员：

```bash
sudo bash deploy-headscale-admin.sh --port 3001 \
  --admin-user admin --admin-pass 'YourStrongPassword'
```

| 场景 | 做法 |
| --- | --- |
| 重新部署 | 默认保留已有密码 |
| 重置密码 | 加 `--reset-admin` |
| 限制来源 | 加 `--allow-cidr 192.168.1.0/24` |
| 不改防火墙 | 加 `--no-firewall` |
| 服务器配置低 / 连不上 GitHub | 在别处编译好再传，用 `--binary ./headscale-admin` |

离线部署（管理界面是 Go 单二进制，可以只在别处编译一次）：

```bash
# 在能上网的机器上（需要仓库里的 headscale-admin 源码目录）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o headscale-admin .
scp headscale-admin deploy-headscale-admin.sh 服务器:~/
scp -r headscale-admin/templates headscale-admin/static 服务器:~/

# 在服务器上（不装 Go、不编译）
sudo bash deploy-headscale-admin.sh --binary ~/headscale-admin
```

## Windows Server 部署

> 本节随 `feature/windows` 分支提供。

管理界面（Headscale Admin）可以**原生跑在 Windows Server 上**，功能与 Linux 版一致：验证码、双语、主题、节点/用户/密钥管理、配置校验、运维监控一应俱全。

控制服务器（headscale）在 Windows 上也能跑，但**上游不发布官方 Windows 二进制**（v0.29.x 的发布包里只有 linux/darwin），需要自己从源码编译一次。

### 两边的能力差异

| 能力 | Linux | Windows Server | 说明 |
| --- | --- | --- | --- |
| 管理界面（Go 单二进制） | ✅ | ✅ | 功能一致 |
| headscale 控制服务器 | ✅ 官方二进制 | ⚠️ 需自行编译 | 上游没有 Windows 发布包 |
| 服务托管 | systemd | 计划任务 | headscale 是上游二进制，不会注册 SCM 控制处理器，`sc.exe` 注册必然报 1053 |
| 服务日志 | `journalctl -u headscale` | `C:\ProgramData\headscale\logs\headscale.log` | Windows 服务不写 stdout，部署脚本重定向到文件 |
| 证书自动续期 | certbot + systemd timer | ❌ 手工更换 | 没有 certbot 也没有 timer，界面会明确提示 |
| 主机 / 网卡监控 | `/proc` | `GetSystemTimes` / `GetIfTable2Ex` | 运维页的 CPU、内存、网卡流量都有真实数据 |

### 准备 headscale.exe

headscale 是纯 Go 程序，直接编译即可：

```bash
git clone --depth 1 --branch v0.29.7 https://github.com/juanfont/headscale.git
cd headscale
go build -o headscale.exe ./cmd/headscale
```

> 需要 Go 1.22+。首次编译要拉依赖，国内机器可设 `GOPROXY=https://goproxy.cn`。

### 部署

在**管理员** PowerShell 里执行：

```powershell
# 管理界面 + headscale 服务端
.\deploy-headscale-admin.ps1 -HeadscaleBinary C:\src\headscale.exe `
    -ServerUrl http://192.168.1.10:8080 -BaseDomain hs.example.com

# 只装管理界面（headscale 之后补）
.\deploy-headscale-admin.ps1 -SkipHeadscale

# 离线：在别处编译好二进制再拷过来
.\deploy-headscale-admin.ps1 -Binary .\headscale-admin.exe -SkipHeadscale
```

脚本做的事与 Linux 版一一对应：编译安装 → 初始化管理员 → 注册计划任务 → 防火墙 → 健康检查；改配置前跑 `configtest` 校验，校验不过直接终止、不带病启动。

### 主要参数

| 参数 | 说明 |
| --- | --- |
| `-Port` | 管理界面监听端口，默认 `3001` |
| `-AdminUser` / `-AdminPass` | 初始管理员；不给 `-AdminPass` 则交互输入 |
| `-ResetAdmin` | 重置管理员密码 |
| `-AllowCidr` | 只放行指定网段访问管理界面 |
| `-NoFirewall` | 不修改防火墙 |
| `-Binary` | 用本地已有的 `headscale-admin.exe`，跳过编译 |
| `-HeadscaleBinary` | 本地已有的 `headscale.exe` |
| `-HeadscaleConfig` | 用已有的 headscale 配置，不生成新的 |
| `-SkipHeadscale` | 只装管理界面，不碰 headscale 服务端 |
| `-ServerUrl` / `-BaseDomain` / `-PrefixV4` | 生成 headscale 配置时用 |

### 落点与运维

| 内容 | 位置 |
| --- | --- |
| 管理界面程序 | `C:\Program Files\headscale-admin` |
| 管理界面数据 | `C:\ProgramData\headscale-admin`（SQLite、会话、HTTPS 配置） |
| headscale 程序 | `C:\Program Files\headscale` |
| headscale 配置与数据 | `C:\ProgramData\headscale`（config.yaml、db.sqlite、noise_private.key） |
| headscale 日志 | `C:\ProgramData\headscale\logs\headscale.log` |

```powershell
schtasks /Run  /TN headscale-admin                    # 启动
schtasks /End  /TN headscale-admin                    # 停止
schtasks /Query /TN headscale-admin /V /FO LIST       # 状态
```

### Windows 上的两个坑

1. **配置里的路径分两类，不能混。** headscale 会把 `noise.private_key_path`、`database.sqlite.path`、`tls_letsencrypt_cache_dir`、`policy.path` 按「相对配置文件所在目录」解析——写 `C:\ProgramData\headscale\xxx` 会被拼成 `C:\ProgramData\headscale\C:\ProgramData\headscale\xxx` 而直接报错。要么写相对路径（`noise_private.key`），要么用部署脚本生成。
2. **计划任务的动作参数里只能有一个带引号的路径。** `cmd.exe /c` 有条引号剥离规则：参数里再出现一个带引号的路径（日志重定向）就会把整条命令行拼坏，cmd.exe 以 `0x80070001` 秒退、连日志文件都不会生成。部署脚本因此把重定向放进 `run.cmd` 内部，任务参数用双引号技巧只留一个路径。

## 管理界面

界面采用暖色底、粗边框、硬阴影的新粗野主义风格，图标全部内联，不依赖外部 CDN。顶部地球图标切换中英文（选择存 Cookie，登录页也能切），主题下拉提供 14 套配色——浅色、深色、赛博朋克、海洋、日落、森林、午夜、德古拉、北欧、玫瑰、咖啡、终端、柔和、合成波（选择存浏览器本地）。

### 页面一览

| 路径 | 页面 | 内容 |
| --- | --- | --- |
| `/` | 总览 | 用户数、节点数、在线数、待审批路由数、密钥数、服务状态、证书到期 |
| `/nodes` | 节点 | 列表、重命名、过期、删除、标签、路由审批（只列出尚未批准的路由，批准后自动出队） |
| `/users` | 用户 | 创建、重命名、删除，支持显示名与邮箱 |
| `/keys` | 密钥与接入 | 预授权密钥与 API key 的生成 / 失效 / 删除，客户端一键接入命令 |
| `/network` | 网络与策略 | DNS / MagicDNS、DERP、ACL 策略、完整配置编辑 |
| `/ops` | 运维监控 | 本机资源、节点状态、实时网速、告警、服务状态、日志、重启 |
| `/admins` | 管理员 | 增删改、角色（admin / readonly）、修改密码 |
| `/settings` | 配置项 | 域名、端口与网段、HTTPS 证书、证书自动续期、管理界面自身 HTTPS |

**密钥与接入**这一块的细节：

- **有效期下拉**：1 小时、24 小时、7 天、30 天、90 天、1 年、长期（100 年，等效永不过期）。
- **一键接入卡片**：`server_url` 自动读取当前配置（绑定 IP / 域名并保存后即生效），新生成的预授权密钥自动填入 `key`，页面实时拼出 `tailscale up --login-server=<server_url> --authkey=<key>`，一键复制。地址或密钥未就绪时命令显示占位提示且复制按钮禁用，避免复制到半截命令。
- **打码显示**：密钥默认打码，点眼睛图标切换；列表中每条可单独复制，或「填入命令」直接用于接入卡片。新密钥只展示一次（按会话一次性暂存，刷新后不再带出），密钥不出现在 URL 与日志中。
- **类型与备注**：列表带「类型」标识（一次性 / 可复用、已使用 / 未使用、已失效、临时节点徽章）和「备注」列。备注存在管理界面本地 SQLite（按密钥 ID 关联），删除密钥时一并清理。
- **API key**：创建后在醒目卡片展示完整值并附复制按钮（仅显示一次，刷新即消失），列表中只显示前缀。
- headscale 的 `preauthkeys list` 只返回打码密钥（`hskey-auth-xxxx-***`），打码条目不提供复制 / 填入命令，并标注「仅创建时可见完整值」。

### 配置项

路径 `/settings`。把 Headscale 常用配置集中到网页端，避免手改 YAML。**各块独立保存、互不影响**。表单上用通俗名称描述用途，名称旁边的小标签是原始配置键，方便对照 YAML 与官方文档；键名本身不做任何改动。

**域名与访问地址**

| 配置键 | 页面名称 | 校验规则 |
| --- | --- | --- |
| `server_url` | 对外访问地址 | 必填；只允许 `http://` / `https://`；不能带路径、参数、锚点或结尾 `/`；协议必须与证书方式匹配 |
| `dns.base_domain` | 设备域名后缀 | 必填；至少两级标签；不能是纯 IP；不能与 `server_url` 域名相同 |
| `dns.magic_dns` | 启用设备名互访（MagicDNS） | 开关；勾选后设备可直接用设备名互访 |

**端口与网段**

| 配置键 | 页面名称 | 校验规则 |
| --- | --- | --- |
| `listen_addr` | 客户端连接端口 | 必填；`IP:端口`，端口 1–65535 |
| `grpc_listen_addr` | 本机管理端口 | 必填；格式同上 |
| `metrics_listen_addr` | 监控数据端口 | 可留空；格式同上 |
| `prefixes.v4` | 设备 IPv4 网段 | 必填；合法 CIDR（不限定在 `100.64.0.0/10` 内） |
| `prefixes.v6` | 设备 IPv6 网段 | 勾选「启用 IPv6」后必填；合法 CIDR，掩码不能大于 /64 |

同一表单内端口重复会被直接拦截。`server_url` 端口与监听端口不一致时给出警告，避免改端口时两边互相锁死。

**IPv6 开关**默认关闭。未勾选时会把配置里已有的 `prefixes.v6` **整行删除**（而不是留着旧值让 headscale 继续按 IPv6 网段分配地址），勾选后写入指定网段。网段只校验 CIDR 格式，合法性最终交给 `headscale configtest` 兜底，避免管理界面比 headscale 更严格而拒绝合法配置。

> 部分云厂商（如阿里云 ECS）自身占用 100 段，与设备虚拟地址冲突，此时可改成任意合法网段（如 `10.99.0.0/24`）。
> 修改网段只影响**之后新加入**的设备，存量节点 IP 不变。

**HTTPS 证书**

三种方式，切换后只写入当前方式需要的键：

| 方式 | 做法 |
| --- | --- |
| 自动申请（Let's Encrypt） | 填证书域名（必填 FQDN）、验证方式（HTTP-01 / TLS-ALPN-01）、验证时临时监听地址、证书缓存目录 |
| 手动上传已有证书 | 上传三个文件：**公钥证书（叶子）**、**中间证书（可含多张）**、**私钥**。服务端先把文件内容归一化（去 BOM、CRLF 转 LF），把证书按签名关系排成完整链（CA 交付的 bundle 顺序颠倒也能用），再用 `tls.X509KeyPair` 校验证书与私钥配对、逐对验签确认链完整，全部通过才写入 `/etc/headscale/certs/manual.crt` 与 `manual.key`（0600，属主 `headscale`），再把路径写进 `tls_cert_path` / `tls_key_path` |
| 不启用 HTTPS | 清空 ACME 域名与证书路径 |

手动上传时，已经配好、这次不更换则三个框都留空即可沿用原证书；上传的私钥不会回显到页面。

页面顶部解析当前证书，显示来源、CN、颁发者、生效与到期时间、剩余天数、SAN、私钥是否匹配。到期 30 天内或私钥不匹配会显示警告。

**证书自动更新**

- **内置 ACME**：由 headscale 进程自动续期，不需要额外定时任务。
- **certbot**：显示 `certbot.timer` 的启用与运行状态、上次 / 下次执行时间、证书名；可启停定时器、设置每日检查时间（写入 `certbot.timer.d/override.conf` 后 `daemon-reload`），也可立即续期（执行 `certbot renew` 后重启 headscale）。
- 未安装 certbot 时显示安装提示，不展示不可用的按钮。

**管理界面 HTTPS**

页面底部的独立卡片，控制的是**你现在访问的这个管理网页**是否走 HTTPS，与管 headscale 服务的证书是两套独立配置：

- 开关 + 证书来源二选一：粘贴 PEM（校验配对后写入数据目录 `certs/admin.crt` / `admin.key`，权限 0600），或复用 headscale 手动模式下的证书文件路径。
- 启用后管理界面**只监听 HTTPS**，HTTP 端口不再响应；Cookie 的 `Secure` 标记随 HTTPS 自动开启。
- 证书不可用时记日志并回退 HTTP，避免一次错误配置把运维入口锁死。**回退时 Cookie 的 `Secure` 标记同步关闭**——明文下浏览器不会回传带 `Secure` 的 Cookie，若标记仍在，登录态会立刻失效，恰好把这个安全阀变成锁死入口。
- 保存后需要重启管理界面生效（Go 的 `http.Server` 无法在同一端口原地切换 TLS）。systemd 环境下自动重启，非 systemd 环境提示手动重启。连续保存不会触发 systemd 的启动限流（部署时已用 `StartLimitIntervalSec=0` 关闭），不会把服务重启到起不来的状态。
- 配置存放在数据目录的 `admin-tls.json`（权限 0600）。若因证书问题无法访问网页，删除该文件并重启服务即可恢复 HTTP。

**保存与应用流程**

1. 服务端逐字段校验；失败时原样回显并标红出错字段，不写任何文件。
2. 基于当前 YAML 生成候选配置，写入 `/etc/headscale/.config-candidate.yaml`。
3. 执行 `headscale configtest -c` 校验；失败则删除候选文件并返回完整错误，原配置保持不动。
4. 备份为 `config.yaml.bak.<时间戳>`，再原子替换正式配置。
5. 勾选「保存后立即重启」时执行 `systemctl restart headscale`，等待进程 active 且监听端口可连接；失败自动回滚到备份并再次重启。
6. 成功后页面顶部提示结果与备份文件名。

手动上传证书时，证书文件先落盘再写入配置；若后面的 `configtest` 或重启失败，证书文件会随配置一起还原成上一份。

> 修改监听端口或 `server_url` 会短暂中断客户端连接；切换 HTTP / HTTPS 需要两侧配置配套修改，页面会在不一致时给出警告。

### 网络与策略

路径 `/network`。除配置项页外，网络相关配置也可在这里以图形化表单维护，**每块独立保存**：

| 块 | 内容 |
| --- | --- |
| **DNS** | `base_domain`、MagicDNS 开关、`override_local_dns` 开关、DNS 服务器与搜索域的动态增删行（逐条校验：IP 或 `https://` / `tls://` / `quic://` 地址） |
| **DERP** | 内置中继开关、`region_id`（默认 900）、`region_name` |
| **ACL** | 规则向导填「来源 / 目标 / 端口」，生成单条 `accept` 规则插入编辑器末尾；主体仍是 JSON 编辑框 |
| **完整配置编辑** | 直接改写整份 YAML，作为兜底入口 |

四块保存都走同一条链路：校验 → `headscale configtest` → 备份 → 原子写入 → 可选重启。

### 运维监控

路径 `/ops`。

**本机资源** — 每 3 秒读取 `/proc/stat`、`/proc/meminfo`、`/proc/loadavg`、`/proc/uptime`，展示 CPU 使用率、内存使用率（已用 / 总量）、系统负载（1 / 5 / 15 分钟）、运行时长和最近 3 分钟的 CPU / 内存曲线。

- CPU 使用率由两次采样的累计 tick 差值算出，首次采样显示 0。
- 内存使用率按 `MemTotal - MemAvailable` 计算，老内核没有 `MemAvailable` 时退化为 `MemFree + Buffers + Cached`。
- 非 Linux 环境没有 `/proc`，页面显示不可用提示，不影响其他功能。

**节点监控** — 后台每 15 秒执行一次 `headscale nodes list -o json`，缓存结果并展示节点总数、在线、离线、密钥过期数量，以及每个节点的名称、所属用户、虚拟 IP、在线状态、实时速度（下行 / 上行）、最后握手时间、客户端版本、系统、密钥到期、标签与路由数量。状态分在线、离线、密钥过期三种，密钥过期优先显示。CLI 读取失败时保留上一次成功的数据，并在卡片顶部提示「显示的是上一次成功采集的结果」。表格底部标注数据更新时间与采样周期。

**节点实时速度** — 节点间流量走 WireGuard 直连或 DERP，不经过控制服务器，headscale 的 CLI 与 Prometheus 指标都没有按节点的流量计数器，因此网速数据源需要节点侧自己采集再回传。管理界面支持两条通道，**节点上报优先于计数器文件**：

| 通道 | 适用场景 | 配置 |
| --- | --- | --- |
| HTTP 上报（推荐） | 采集 agent 跑在节点上 | 配置上报令牌后开放 `POST /api/speed` |
| 计数器文件 | 采集 agent 与管理界面同机 | 环境变量 `HSA_NODE_SPEED_FILE` 指向 JSON 文件 |

未接入任何通道时，每个节点的速度列显示占位「—」，鼠标悬停可见原因说明，其余监控数据不受影响。计数器回退（节点或代理重启）时当轮速率按 0 处理，不会出现负值。速率口径与实时网速面板一致，均为 KB/s 基准。

**告警** — 内置 7 条规则：headscale 服务不是 active、CPU 超阈值、内存超阈值、1 分钟负载超过核数×倍率、节点离线、节点密钥过期、节点数据采集失败。

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `HSA_ALERT_CPU` | 85 | CPU 使用率百分比阈值 |
| `HSA_ALERT_MEM` | 90 | 内存使用率百分比阈值 |
| `HSA_ALERT_LOAD` | 2 | 1 分钟负载上限 = CPU 核数 × 该倍率 |

**接口** — `GET /api/monitor` 需要登录，返回 `host`、`nodes`、`alerts`、`thresholds` 四部分，页面每 5 秒轮询一次；节点数据复用服务端 15 秒缓存，多开页面不会频繁调用 CLI。

<details>
<summary><b>扩展方式：新增一个监控指标 / 告警规则</b></summary>

- **新增本机指标**：在 `hostmon.go` 的 `HostSnapshot` 加字段并在 `sample()` 里填充（例如读 `/proc/mounts` 统计磁盘占用），`newHostView` 补展示文案，前端加一个进度条。
- **新增节点指标**：在 `nodemont.go` 的 `nodeStatFromMap` 里从 CLI JSON 取字段并加入 `NodeStat`，模板与 `static/app.js` 的 `nodeRow` 各加一列；需要差值换算的计数器类指标（如网速）仿照 `nodeSpeedRates` 增加数据源即可。
- **新增告警规则**：在 `alerts.go` 实现 `func(alertInput, AlertThresholds) []MonitorAlert`，注册到 `alertRules` 切片，页面与接口输出不需要改动；阈值加到 `AlertThresholds` 并在 `defaultAlertThresholds` 里读环境变量。
- **接通知渠道**：`MonitorAlert` 的 `Key` 与 `Level` 已固定，可在规则求值后按 key 去重，再推送到邮件或 Webhook。

</details>

## 安全设计

### 登录安全

- 密码用 PBKDF2-HMAC-SHA256（12 万次迭代）加盐保存。
- 会话使用 HttpOnly Cookie，所有表单带 CSRF token，只读角色无法提交修改。
- **公网来源的登录页带图形验证码**：4 位数字 PNG，噪点 + 干扰线 + 逐位抖动。**校验排在口令校验之前**——验证码不对就不跑 PBKDF2，攻击者烧不掉 CPU。令牌存在服务端内存（TTL 5 分钟、**一次一用**、上限 1024 条），通过 HttpOnly Cookie `hsa_cap` 下发；图片不缓存，刷新即换一张。
- **验证码豁免只有一条规则、代码内固定、无开关**：来源在 `HSA_LOGIN_TRUSTED_IPS` 里，或者「来源是回环地址 **且** Host 也是回环 / 本机名」。第二个条件缺一不可——同机反代转过来的公网请求 Host 是公网域名，**仍然要验证码**，否则一部署反代验证码就形同虚设。
- 登录失败按「来源 IP + 账号名」与「来源 IP」两个维度分别计数：猜错多次后该来源对该账号的登录会逐渐变慢，同一 IP 失败过多会被临时锁定（默认 30 次触发、锁 2 分钟）；不存在的用户名同样要付出完整的密钥派生代价，避免被用来枚举账号。
- 登录成功后只清「来源 IP + 账号名」这一条计数，不清来源 IP 的累计值——否则攻击者用自己那个合法账号登录一次就能把限速状态洗白。
- 登录请求体上限 64 KB，超出直接返回 413；口令校验有并发闸门（CPU 核数减 2，最多 8 路），排队超时返回 429，避免几十个并发登录把机器 CPU 打满——这台机器同时还跑着 headscale 控制面。
- 管理员把账号降为只读、停用、删除或改密后，该账号已登录的会话会**立即失效**，撤权当场生效；`--reset-admin` 重置口令同样会吊销该账号的既有会话。

### 传输与响应头

管理界面直接暴露在公网上，缺这些头的后果分别是：没有 `X-Frame-Options` 就能被点击劫持（别人把管理界面嵌进自己的页面诱导管理员点「保存」）；没有 CSP 就能让一次 XSS 把会话 Cookie 与 CSRF 令牌一起带走；没有 `nosniff` 浏览器可能把返回内容当成别的类型解析；没有 HSTS 首次走 HTTP 就可能被链路上的设备降级。

| 响应头 | 值 | 作用 |
| --- | --- | --- |
| `X-Frame-Options` | `DENY` | 防点击劫持 |
| `X-Content-Type-Options` | `nosniff` | 浏览器不猜内容类型 |
| `Referrer-Policy` | `same-origin` | 不把管理界面地址带给外站 |
| `Content-Security-Policy` | `default-src 'self'`；`script-src 'self'`；`style-src 'self' 'unsafe-inline'`；`img-src 'self' data:`；`font-src 'self'`；`connect-src 'self'`；`object-src 'none'`；`base-uri 'none'`；`form-action 'self'`；`frame-ancestors 'none'` | 一次 XSS 的破坏面被压到最小 |
| `Strict-Transport-Security` | `max-age=31536000` | **只在直连 HTTPS 时加**（反代终止 TLS 的部署里服务端看不到 `r.TLS`，HSTS 要由反代自己加） |

`script-src` **不放开** `'unsafe-inline'`：页面上不允许再有内联脚本，出现 XSS 就没有第二次机会（为此把主题初始化脚本挪成了独立的 `static/theme.js`）。`style-src` 放开 `'unsafe-inline'` 是因为进度条宽度、主题色块用的是行内 style 属性，那不是脚本。

**静态资源带内容哈希版本号 + `Cache-Control: no-cache`**：Go 的 `http.FileServer` 只发 `Last-Modified`、不发 `Cache-Control`，浏览器会按「文件年龄的 10%」做启发式缓存——上线新版样式后老浏览器还能用十几小时的旧样式。两条措施互补，缺一不可：URL 带版本号解决「已经躺在缓存里的旧副本」，`no-cache` 解决「以后每次改动都要记得改版本号」。

### 共享出口与可信来源

登录限速按来源 IP 计数。NAT、公司统一出口或反向代理后面的所有人共用一个出口 IP，一次误触就会把所有人一起锁住。两个办法：

```bash
# 把运维网段列为可信来源：这些来源既不累计失败、不被锁定，也不需要验证码
# 多个网段用逗号分隔，单个 IP 与 CIDR 都支持
Environment="HSA_LOGIN_TRUSTED_IPS=192.168.1.0/24,10.8.0.5"
```

注意：**管理界面不解析 `X-Forwarded-For`**，认的是 TCP 连接的来源地址。放在反向代理后面时，所有请求的来源地址都是代理自己——此时千万不要把代理的 IP（尤其是 `127.0.0.1`）加进可信列表，否则等于对全世界关闭限速。

管理员账号与会话都存放在 SQLite（`/var/lib/headscale-admin/headscale-admin.db`，权限 0600），服务重启后登录状态仍然保留。首次启动时如果检测到旧版 `admins.json`，会自动迁移并把原文件改名为 `admins.json.migrated`，原密码继续可用。SQLite 驱动为纯 Go 实现（`modernc.org/sqlite`），编译不需要 gcc 或 CGO。

源码目录为 `headscale-admin/`，不依赖任何未声明许可证的第三方项目。

## 可靠性设计

管理界面通过命令行驱动 headscale。这条链路最容易出的问题是「headscale 某次调用卡住 → 整个界面僵住」，所以：

- **每次外部命令都有超时**，并且超时后杀掉整个进程组（`sh -c` 底下还有 openssl / find 这些孙进程，只杀直接子进程会留孤儿、文件一直锁着）。
- **所有调用排在一把可取消的闸门后面**：拿不到闸门就明确返回「headscale 忙」，而不是无限期等待。等闸门与执行共用同一个超时预算，避免「等锁等掉了超时、命令刚起来就被掐」。
- **stdout 与 stderr 分开**：headscale 的 JSON 走 stdout、提示走 stderr，合并两者会把提示混进 JSON 里导致解析失败。
- **改系统状态的动作不继承请求上下文**（`systemctl restart` 用独立 context）——否则发起它的 HTTP 请求一断，重启被取消，服务会停在新旧之间。
- **总览页不再反复起进程**：版本号缓存 10 分钟、证书到期缓存 5 分钟、列表结果缓存 5 秒，服务状态直接复用节点监控快照。缓存**只给只读的总览页用**——节点页、用户页、密钥页仍然读最新数据，改名删除之后立刻可见。缓存**只存成功结果**，一次偶发失败不会被缓存住。
- 超时档位可用 `HSA_HS_CALL_TIMEOUT`（秒）调短，给测试环境用。

## 节点网速上报

**HTTP 上报通道** — 部署脚本会自动生成上报令牌并写入 `/var/lib/headscale-admin/node-speed-token`（权限 0600，升级部署沿用），令牌也可通过 `HSA_NODE_SPEED_TOKEN` 直接给出。

> **没有配置令牌时整个接口关闭并返回 404**，不会变成任何人都能写的匿名入口。

```bash
# 单条上报：node 可以是节点 ID、节点名或 tailnet IP，三种都能匹配到节点
curl -X POST http://<服务器tailnet地址>:3001/api/speed \
  -H "X-Node-Speed-Token: <上报令牌>" \
  -H 'Content-Type: application/json' \
  -d '{"node":"100.64.0.5","rx_bytes":154600,"tx_bytes":31220}'

# 也支持数组批量上报；令牌也可放在请求体的 token 字段，或用 Authorization: Bearer
curl -X POST http://<服务器tailnet地址>:3001/api/speed \
  -H "Authorization: Bearer <上报令牌>" \
  -d '[{"node":"100.64.0.5","rx_bytes":154600,"tx_bytes":31220}]'
```

`rx_bytes` / `tx_bytes` 是**累计**字节数，服务端按两次上报的差值换算速率（与 CPU 利用率的 tick 差值同一思路），单位自动按 B/s、KB/s、MB/s 展示。节点连报两次后速度列开始显示。

**节点侧采集代理** — `node-speed-agent.sh` 是可直接使用的参考实现，自动找 `tailscale*` 网卡读取 `/proc/net/dev` 的累计计数并定时上报：

```bash
HSA_URL=http://<服务器tailnet地址>:3001/api/speed \
HSA_TOKEN=<上报令牌> \
bash node-speed-agent.sh
```

| 可选变量 | 说明 |
| --- | --- |
| `HSA_IFACE` | 指定网卡 |
| `HSA_INTERVAL` | 上报间隔秒数，默认 10，建议不超过 15 |
| `HSA_NODE` | 节点标识，默认用本机 tailnet IP |
| `HSA_ONCE=1` | 只报一次，用于自检 |

**计数器文件通道** — 环境变量 `HSA_NODE_SPEED_FILE` 指向一个 JSON 文件，由同机旁路采集器定期写入累计收发字节数，服务端按两次采样差值换算速率。文件格式按节点 ID 或节点名索引：

```json
{
  "12": {"rx_bytes": 154600, "tx_bytes": 31220},
  "laptop": {"rx_bytes": 1000, "tx_bytes": 500}
}
```

> 节点侧能采集到的只有控制面指标。Tailscale 客户端不会把 CPU、内存等主机资源上报给 headscale，因此各节点的 CPU / 内存不在这个页面的能力范围内；确需采集应在节点上部署 agent，把数据推到新增接口，按上面的扩展方式接入。

## 验证

在任意 Linux（包括 WSL）上可运行只读验证，**不会创建用户、写 `/etc` 或启动服务**：

```bash
bash test-headscale-cli.sh      # CLI 命令、配置键与 configtest
bash test-headscale-admin.sh    # 管理界面编译、登录、验证码、双语、主题、网速、本机与节点监控、
                                # 配置校验、IPv6 开关（含配置文件落盘校验）、网络表单、
                                # CSRF、登录限速、只读账号与撤权生效（需 go/curl）
(cd headscale-admin && go test ./...)
                                # 配置解析、管理界面 HTTPS 证书、节点映射、双语展示、
                                # 告警规则、账号权限、TTL 缓存与安全响应头单元测试
```

管理界面 HTTPS 的端到端验证需要一台可写 `/etc/systemd/system` 的 Linux 机器（要起独立的 systemd 单元），**不会碰生产服务**：

```bash
bash test-headscale-admin-tls.sh /usr/local/bin/headscale-admin
                                # 隔离实例（独立端口 + 独立单元）跑启用 → 证书失效回退
                                # → 证书恢复 → 关闭 → 复用 headscale 手动证书全流程，
                                # 每段都断言 Cookie 的 Secure 标记与登录态可用性
```

## 安全提示

| 项 | 说明 |
| --- | --- |
| 传输加密 | 管理界面走独立账号体系，密码经 PBKDF2 加盐摘要保存，登录态为 HttpOnly Cookie；HTTP 模式下传输仍是明文，公网使用请配合 HTTPS 或 `--allow-cidr` 限制来源 |
| 口令不进 argv | 初始与重置管理员口令可用环境变量传入（`HSA_INIT_ADMIN`、`HSA_RESET_ADMIN`，格式 `用户名:密码`）。命令行参数会出现在 `/proc/<pid>/cmdline` 里，同机任何用户都读得到；部署脚本默认走环境变量 |
| 防火墙 | 部署脚本会在防火墙放行 TCP/3001 给所有来源。内网自用建议加 `--allow-cidr` 限制网段；公网暴露务必先启用管理界面 HTTPS |
| 两套 HTTPS | 管理界面 HTTPS 与 headscale 服务的 HTTPS 是两套独立配置，可分别启用。启用管理界面 HTTPS 后该端口只接受 HTTPS，请求 `http://` 不会跳转；证书异常导致无法访问时，删除数据目录的 `admin-tls.json` 并重启服务即可恢复 HTTP |
| 明文告警 | HTTP 模式为明文，较新的 Tailscale 客户端可能需要设置 `TS_ALLOW_HTTP_CONTROL_SERVER=1` |
| 域名与端口 | HTTPS 模式需要域名正确解析到本机，并确保 80（证书验证）和 443（服务）端口可达；启用内置 DERP 时需放行 UDP/3478 |

## 许可证

本仓库中的脚本、文档与管理界面源码采用 [MIT License](LICENSE)。
