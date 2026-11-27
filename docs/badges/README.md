# README 徽章图片

本目录存放 README 顶部那排徽章的 **SVG 源文件**，README 用相对路径 `docs/badges/<name>.svg` 引用。

## 为什么不直接用 shields.io 的外链

GitHub 会把 README 里的**外部图片**统一转走 `camo.githubusercontent.com` 这个图片代理。camo 回源
shields.io 时会稳定返回 **504 Gateway Timeout**（shields.io 的 Cloudflare 拦了 camo 的出口 IP），
于是浏览器拿到非图片响应、被 Chromium 的 ORB（Opaque Response Blocking）拦掉，页面上就是一片破图。
只有**已经在 camo 缓存里**的旧徽章能侥幸显示，冷缓存的必然坏。

把 SVG 收进仓库后，图片走 `raw.githubusercontent.com` 第一方通道，不经过 camo，**稳定可渲染**。

## 怎么更新

改版本号之类的文案后，重新生成对应文件即可（需要本机可访问 shields.io）：

```bash
cd docs/badges
curl -fsSL -o license.svg 'https://img.shields.io/badge/License-MIT-2ea44f.svg?style=flat-square'
curl -fsSL -o headscale.svg 'https://img.shields.io/badge/Headscale-v0.29.7-4B5563.svg?style=flat-square'
curl -fsSL -o go.svg 'https://img.shields.io/badge/Go-1.26-00ADD8.svg?style=flat-square&logo=go&logoColor=white'
curl -fsSL -o platform.svg 'https://img.shields.io/badge/Platform-Linux-FCC624.svg?style=flat-square&logo=linux&logoColor=black'
curl -fsSL -o arch.svg 'https://img.shields.io/badge/Arch-x86__64%20%7C%20aarch64-6B7280.svg?style=flat-square'
curl -fsSL -o shell.svg 'https://img.shields.io/badge/Shell-Bash-4EAA25.svg?style=flat-square&logo=gnubash&logoColor=white'
curl -fsSL -o sqlite.svg 'https://img.shields.io/badge/SQLite-pure%20Go-6B7280.svg?style=flat-square'
curl -fsSL -o dependencies.svg 'https://img.shields.io/badge/dependencies-none-6B7280.svg?style=flat-square'
curl -fsSL -o i18n.svg 'https://img.shields.io/badge/i18n-%E7%AE%80%E4%BD%93%E4%B8%AD%E6%96%87%20%7C%20English-8B5CF6.svg?style=flat-square'
```

生成后确认每个文件都是合法 SVG（首行以 `<?xml` 或 `<svg` 开头），再提交。

> 注意：这里刻意用 **SVG 而不是 PNG**——SVG 体积小（几百字节 vs 几 KB）、矢量清晰，
> 且 GitHub 对仓库内 SVG 不做代理，渲染最稳。
