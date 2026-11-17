# headscale-onekey

一键部署私有 [Headscale](https://github.com/juanfont/headscale) 控制服务器，并可选择部署社区项目 HeadControl 作为中文 Web 管理界面。

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

## 目录结构

| 文件 | 说明 |
| --- | --- |
| `deploy-headscale.sh` | Headscale 一键部署 |
| `deploy-headcontrol.sh` | HeadControl 一键部署与汉化 |
| `headcontrol-zh.patch` | HeadControl 中文补丁，针对固定上游提交 |
| `test-headscale-cli.sh` | Headscale CLI / 配置键只读沙盒验证 |
| `test-headcontrol-patch.sh` | 汉化补丁、Basic Auth 应用与编译验证 |

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

## 验证

在任意 Linux（包括 WSL）上可运行只读验证，不会创建用户、写 `/etc` 或启动服务：

```bash
bash test-headscale-cli.sh        # 验证 CLI 命令、配置键与 configtest
bash test-headcontrol-patch.sh    # 验证汉化补丁、Basic Auth 与 Go 编译（需 git/go/gcc/curl/openssl）
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
