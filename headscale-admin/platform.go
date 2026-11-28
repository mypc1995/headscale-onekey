package main

// 平台适配层：把 Linux 与 Windows 的差异收敛到几个文件里。
//
// 管理界面要能在 Linux 与 Windows Server 上都跑起来，差异只有三处：
//  ① 默认落点——配置、证书、数据目录的位置；
//  ② 服务控制——systemd 单元 vs 计划任务；
//  ③ 系统命令——chown / journalctl / certbot 只在 Linux 有。
//
// ① ② ③ 的实现按平台拆到 platform_linux.go 与 platform_windows.go，
// 本文件只放两边共用的常量与取值规则。新增平台相关的东西时，
// 先在这里定好函数签名，再去两个平台文件里各实现一份，
// 免得某天只改了一边、编译在另一个平台才炸。

import (
	"os"
	"path/filepath"
	"strings"
)

// hsServiceName 是 headscale 控制服务的名字。
//
// Linux 上是 systemd 单元名，Windows 上是计划任务名——刻意保持同名，
// 界面文案、部署脚本、状态判断都不用区分平台。
const hsServiceName = "headscale"

// headscaleUser 是 headscale 服务的运行账号名。
// 只在 Linux 上用得到（属主修正）；Windows 没有对应的概念，
// 那边的 ACL 由部署脚本用 icacls 设一次即可。
const headscaleUser = "headscale"

// envPath 按「环境变量 → 平台默认值」的顺序取路径。
//
// 环境变量优先：同一份二进制在不同机器上都能按需改落点；
// 平台默认值只是兜底，保证「什么都不配」也能跑起来。
func envPath(env, def string) string {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v
	}
	return def
}

// trimSep 去掉路径末尾的分隔符。
//
// 平台默认路径是手写的字面量，末尾带不带斜杠全凭习惯；
// 拼进 filepath.Join 之前统一去掉，免得出现 "C:\ProgramData\headscale\" 这种尾巴。
func trimSep(p string) string {
	return strings.TrimRight(p, `/\`)
}

// joinPath 拼路径。filepath.Join 会按当前平台的规则处理分隔符，
// 不用自己拼 "C:\\" + "foo"。
func joinPath(dir, name string) string {
	return filepath.Join(dir, name)
}
