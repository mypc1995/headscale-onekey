#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Headscale Admin 控制台 —— Windows Server 部署脚本。

.DESCRIPTION
    与 Linux 侧 deploy-headscale-admin.sh 对等：
    编译安装 -> 初始化管理员 -> 注册计划任务 -> 防火墙 -> 健康检查。

    服务托管用「计划任务」而不是 sc.exe 注册的 Windows 服务：
    管理界面与 headscale 都是上游 Go 二进制，不会注册 SCM 控制处理器，
    sc.exe 注册必然报 1053「服务没有响应启动或控制函数」，而且改不了上游。
    计划任务零依赖、国内机器能离线用。

    计划任务的动作是 run.cmd —— 一个看门狗循环：
      * 进程崩了会被重新拉起（相当于 systemd 的 Restart=always）；
      * 进程以约定退出码 42 退出时立刻重启（管理界面改自身 HTTPS 配置时用）。

    关于 headscale 服务端：
    上游没有官方 Windows 二进制（v0.29.x 的发布包里只有 linux/darwin），
    脚本因此不负责下载，而是接受本地的 headscale.exe：
      * 从源码编译：git clone https://github.com/juanfont/headscale && cd headscale
        && go build -o headscale.exe ./cmd/headscale
      * 或者用 -SkipHeadscale 只装管理界面，之后再补 headscale.exe。

.PARAMETER Port
    管理界面监听端口，默认 3001。

.PARAMETER AdminUser
    初始管理员用户名，默认 admin。

.PARAMETER AdminPass
    初始管理员密码；不指定则交互输入。

.PARAMETER ResetAdmin
    重置管理员密码（会覆盖该用户旧密码）。

.PARAMETER AllowCidr
    仅允许指定网段访问管理界面；不指定则对所有来源开放。

.PARAMETER NoFirewall
    不修改防火墙。

.PARAMETER Binary
    使用本地已有的 headscale-admin 二进制（跳过编译，适合离线或低配机器）。

.PARAMETER HeadscaleBinary
    本地已有的 headscale.exe。上游无官方 Windows 包，需自行编译。

.PARAMETER HeadscaleConfig
    已有 headscale 配置文件；指定则直接使用，不生成新配置。

.PARAMETER SkipHeadscale
    只装管理界面，不碰 headscale 服务端。

.PARAMETER ServerUrl
    生成 headscale 配置时用的对外访问地址；不指定则按本机 IP 推导。

.PARAMETER BaseDomain
    MagicDNS 根域；不指定则按 ServerUrl 派生。

.PARAMETER PrefixV4
    虚拟网段，默认官方示例的 100.64.0.0/10。

.PARAMETER GoProxy
    编译时用的 Go 代理。

.EXAMPLE
    .\deploy-headscale-admin.ps1 -HeadscaleBinary C:\src\headscale.exe `
        -ServerUrl http://192.168.1.10:8080 -BaseDomain hs.example.com

.EXAMPLE
    # 离线：先在别的机器编译好管理界面二进制，再拷过来部署
    go build -o headscale-admin.exe .
    .\deploy-headscale-admin.ps1 -Binary .\headscale-admin.exe -SkipHeadscale
#>
[CmdletBinding()]
param(
    [int]$Port = 3001,
    [string]$AdminUser = 'admin',
    [string]$AdminPass = '',
    [switch]$ResetAdmin,
    [string]$AllowCidr = '',
    [switch]$NoFirewall,
    [string]$Binary = '',
    [string]$HeadscaleBinary = '',
    [string]$HeadscaleConfig = '',
    [switch]$SkipHeadscale,
    [string]$ServerUrl = '',
    [string]$BaseDomain = '',
    [string]$PrefixV4 = '100.64.0.0/10',
    [string]$GoProxy = 'https://goproxy.cn,https://proxy.golang.org,direct'
)

$ErrorActionPreference = 'Stop'

# ===================== 路径常量 =====================
$AppDir      = Join-Path $env:ProgramFiles 'headscale-admin'
$HsDir       = Join-Path $env:ProgramFiles 'headscale'
$DataDir     = Join-Path $env:ProgramData 'headscale-admin'
$HsDataDir   = Join-Path $env:ProgramData 'headscale'
$DbFile      = Join-Path $DataDir 'headscale-admin.db'
$LegacyJson  = Join-Path $DataDir 'admins.json'
$SpeedToken  = Join-Path $DataDir 'node-speed-token'
$AppLogDir   = Join-Path $DataDir 'logs'
$HsLogDir    = Join-Path $HsDataDir 'logs'
$HsConfig    = Join-Path $HsDataDir 'config.yaml'
$HsPolicy    = Join-Path $HsDataDir 'acl.hujson'
$AppBin      = Join-Path $AppDir 'headscale-admin.exe'
$AppRun      = Join-Path $AppDir 'run.cmd'
$HsBin       = Join-Path $HsDir 'headscale.exe'
$HsRun       = Join-Path $HsDir 'run.cmd'
$TaskAdmin   = 'headscale-admin'
$TaskHs      = 'headscale'
$RestartCode = 42

# ===================== 输出辅助 =====================
function Write-Step { param([string]$Msg) Write-Host "`n==> $Msg" -ForegroundColor Cyan }
function Write-Info { param([string]$Msg) Write-Host "[INFO] $Msg" -ForegroundColor Green }
function Write-Warn { param([string]$Msg) Write-Host "[WARN] $Msg" -ForegroundColor Yellow }
function Write-Err  { param([string]$Msg) Write-Host "[ERROR] $Msg" -ForegroundColor Red; exit 1 }

# ===================== 任务与进程 =====================

# Stop-TaskIfExists 停掉已注册的计划任务，并收掉它拉起的进程。
#
# 任务停了不等于进程没了：任务动作是 cmd 包装器，结束任务不保证带走
# 真正的 headscale.exe。残留的孤儿进程会和新起的抢端口，也会锁住 exe 文件
# 导致覆盖安装失败。
function Stop-TaskIfExists {
    param([string]$TaskName, [string]$ExeName)

    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($task) {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 500
        Write-Info "已停止计划任务 $TaskName"
    }
    if ($ExeName) {
        Get-Process -Name $ExeName -ErrorAction SilentlyContinue |
            ForEach-Object { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue }
        Start-Sleep -Milliseconds 300
    }
}

# Register-SupervisorTask 注册一个开机自启的看门狗计划任务。
#
# 用 SYSTEM 身份跑：服务要读写 ProgramData 下的数据目录，也要能改防火墙。
# ExecutionTimeLimit 设成 0（不限时）——默认 3 天会把长期运行的服务掐掉。
function Register-SupervisorTask {
    param(
        [string]$TaskName,
        [string]$ScriptPath,
        [string]$WorkDir = ''
    )

    # 任务动作的参数里只能有**一个**带引号的路径。
    #
    # cmd.exe 的 /c 有条引号剥离规则：/c 后面紧跟引号时，它会去掉首个引号
    # 并删掉命令行里最后一个引号。参数里一旦出现第二个带引号的路径
    # （比如把日志重定向也写在参数里），整条命令行就被拼坏，
    # cmd.exe 以 0x80070001 (ERROR_INVALID_FUNCTION) 退出——
    # 任务瞬间结束，连日志文件都不会生成。
    #
    # 所以：重定向放进 run.cmd 内部，参数用双引号技巧
    # （cmd /c ""路径"" 去掉首尾引号后，正好剩下一个带引号的完整路径）。
    $action = New-ScheduledTaskAction -Execute 'cmd.exe' `
        -Argument "/c `"`"$ScriptPath`"`"" -WorkingDirectory $WorkDir
    $trigger   = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $settings  = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
        -MultipleInstances IgnoreNew

    # 先删旧任务：同名覆盖会保留旧设置，容易留下脏配置。
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
        -Principal $principal -Settings $settings `
        -Description 'Headscale OneKey 看门狗任务' | Out-Null
    Start-ScheduledTask -TaskName $TaskName
    Start-Sleep -Seconds 2
    $state = (Get-ScheduledTask -TaskName $TaskName).State
    if ($state -eq 'Running') {
        Write-Info "计划任务 $TaskName 已启动"
    } else {
        Write-Warn "计划任务 $TaskName 当前状态: $state（可能仍在启动，请查看日志）"
    }
}

# ===================== headscale 配置生成 =====================

# New-HsConfig 生成一份 Windows 路径的最小可用 headscale 配置。
#
# 路径一律用单引号包住：YAML 的双引号里反斜杠是转义符，
# 'C:\ProgramData\headscale\db.sqlite' 才是字面路径。
function New-HsConfig {
    Write-Step "生成 headscale 配置 $HsConfig"

    if ([string]::IsNullOrWhiteSpace($ServerUrl)) {
        $ip = (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
            Where-Object { $_.IPAddress -notlike '127.*' -and $_.IPAddress -notlike '169.254.*' } |
            Select-Object -First 1).IPAddress
        if (-not $ip) { $ip = '127.0.0.1' }
        $script:ServerUrl = "http://${ip}:8080"
        Write-Info "未指定 -ServerUrl，按本机地址生成: $ServerUrl"
    }
    $ServerUrl = $ServerUrl.TrimEnd('/')

    # MagicDNS 根域不能等于 server_url 的域名，也不能是它的子域——
    # headscale 会直接拒绝这种配置（否则 Tailscale 接管该域，服务不可达）。
    if ([string]::IsNullOrWhiteSpace($BaseDomain)) {
        $urlHost = ([Uri]$ServerUrl).Host
        if ($urlHost -match '^[a-zA-Z0-9-]+(\.[a-zA-Z0-9-]+)*\.[a-zA-Z]{2,}$') {
            $script:BaseDomain = 'hs.' + $urlHost
        } else {
            $script:BaseDomain = 'headscale.local'
        }
        Write-Info "MagicDNS base_domain: $BaseDomain"
    }

    $cfg = @"
# Headscale 配置（由 deploy-headscale-admin.ps1 生成）
#
# 路径分两类，不能混：
#  1) headscale 会先把这些键按「相对配置文件所在目录」解析
#     （noise.private_key_path / database.sqlite.path / tls_letsencrypt_cache_dir
#      / tls_cert_path / tls_key_path / derp.server.private_key_path / policy.path）：
#     必须写相对路径。写成 C:\... 会被拼成
#     C:\ProgramData\headscale\C:\ProgramData\headscale\xxx 直接报错。
#  2) unix_socket 是原样使用的，可以写 Windows 绝对路径。
#
# 路径本身用单引号包住：YAML 的双引号里反斜杠是转义符，单引号才是字面路径。

server_url: $ServerUrl
listen_addr: 0.0.0.0:8080
metrics_listen_addr: 127.0.0.1:9090
grpc_listen_addr: 127.0.0.1:50443
grpc_allow_insecure: false
trusted_proxies: []

noise:
  private_key_path: noise_private.key

prefixes:
  v4: $PrefixV4
  v6: fd7a:115c:a1e0::/48
  allocation: sequential

derp:
  server:
    enabled: false
    region_id: 999
    region_code: "headscale"
    region_name: "Headscale Embedded DERP"
    verify_clients: true
    stun_listen_addr: "0.0.0.0:3478"
    private_key_path: derp_server_private.key
    automatically_add_embedded_derp_region: true
  urls:
    - https://controlplane.tailscale.com/derpmap/default
  paths: []
  auto_update_enabled: true
  update_frequency: 3h

disable_check_updates: true

database:
  type: sqlite
  debug: false
  gorm:
    prepare_stmt: true
    parameterized_queries: true
    skip_err_record_not_found: true
    slow_threshold: 1000
  sqlite:
    path: db.sqlite
    write_ahead_log: true
    wal_autocheckpoint: 1000

acme_url: https://acme-v02.api.letsencrypt.org/directory
acme_email: ""
tls_letsencrypt_hostname: ""
tls_letsencrypt_cache_dir: cache
tls_letsencrypt_challenge_type: HTTP-01
tls_letsencrypt_listen: ":http"
tls_cert_path: ""
tls_key_path: ""

log:
  level: info
  format: text

policy:
  mode: file
  path: acl.hujson

dns:
  magic_dns: true
  base_domain: $BaseDomain
  override_local_dns: true
  nameservers:
    global:
      - 1.1.1.1
      - 1.0.0.1
    split: {}
  search_domains: []
  extra_records: []

unix_socket: '$HsDataDir\headscale.sock'
unix_socket_permission: "0770"

logtail:
  enabled: false

taildrop:
  enabled: true

auto_update:
  enabled: false
"@
    Set-Content -Path $HsConfig -Value $cfg -Encoding utf8
    Write-Info "配置已生成: $HsConfig"
}

# ===================== 源码定位 =====================
# 仓库里源码在 headscale-admin/ 子目录；离线部署包里二进制与模板同级。
# 两种布局都要认，否则离线包会被误判成「找不到源码」。
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$SrcRoot   = $ScriptDir
if (-not (Test-Path (Join-Path $SrcRoot 'main.go')) -and
    (Test-Path (Join-Path $ScriptDir 'headscale-admin\templates'))) {
    $SrcRoot = Join-Path $ScriptDir 'headscale-admin'
}

# ===================== 管理员口令 =====================
$preserveAdmin = $false
if ((Test-Path $DbFile) -or (Test-Path $LegacyJson)) {
    if (-not $ResetAdmin) {
        $preserveAdmin = $true
        Write-Info '检测到已有数据库，保留现有管理员密码（如需重置加 -ResetAdmin）'
    }
}
if (-not $preserveAdmin -and [string]::IsNullOrEmpty($AdminPass)) {
    $secure = Read-Host -AsSecureString -Prompt "  设置管理员 $AdminUser 的密码"
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    $AdminPass = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
    if ([string]::IsNullOrEmpty($AdminPass)) { Write-Err '密码不能为空' }
}

# ===================== 编译 =====================
if ($Binary) {
    Write-Step "使用本地 headscale-admin 二进制 $Binary（跳过编译）"
    if (-not (Test-Path $Binary)) { Write-Err "二进制不存在: $Binary" }
    $NewBin = (Resolve-Path $Binary).Path
} else {
    Write-Step '检查 Go 工具链'
    $go = Get-Command go -ErrorAction SilentlyContinue
    if (-not $go) {
        Write-Err '未检测到 Go。请先安装 Go 1.22+（https://go.dev/dl/），或用 -Binary 指定已编译好的 headscale-admin.exe'
    }
    Write-Info ("Go 版本: " + ((& go version) -replace '^go version go(\S+).*$', '$1'))
    if (-not (Test-Path (Join-Path $SrcRoot 'main.go'))) {
        Write-Err "未找到源码 main.go（查找位置: $SrcRoot）。请在仓库目录运行，或用 -Binary 指定预编译二进制。"
    }

    Write-Step '编译 Headscale Admin'
    Push-Location $SrcRoot
    try {
        $env:GOPROXY = $GoProxy
        Write-Info "下载 Go 依赖（GOPROXY=$GoProxy）"
        & go mod download 2>&1 | Out-Null
        $NewBin = Join-Path $env:TEMP 'headscale-admin.build.exe'
        $env:CGO_ENABLED = '0'
        & go build -o $NewBin . 2>&1 | Out-Null
        if ($LASTEXITCODE -ne 0) { Write-Err '编译失败' }
    } finally {
        Pop-Location
    }
}

# ===================== 目录与文件 =====================
Write-Step '安装程序与资源'
foreach ($d in @($AppDir, $HsDir, $DataDir, $HsDataDir, $AppLogDir, $HsLogDir,
        (Join-Path $HsDataDir 'certs'), (Join-Path $HsDataDir 'cache'),
        (Join-Path $DataDir 'certs'))) {
    if (-not (Test-Path $d)) { New-Item -ItemType Directory -Path $d -Force | Out-Null }
}

# 先停旧任务再覆盖文件：正在运行的 exe 被覆盖会失败。
Stop-TaskIfExists -TaskName $TaskAdmin -ExeName 'headscale-admin'
Stop-TaskIfExists -TaskName $TaskHs -ExeName 'headscale'

Copy-Item -Path $NewBin -Destination $AppBin -Force
if ($NewBin -ne $AppBin) { Remove-Item $NewBin -Force -ErrorAction SilentlyContinue }

# 资源要覆盖式复制：整目录复制到已存在的同名目录下会变成 templates/templates，
# 旧文件原地不动，新文件根本没生效（升级部署时才会暴露）。
foreach ($sub in @('templates', 'static')) {
    $src = Join-Path $SrcRoot $sub
    if (-not (Test-Path $src)) { Write-Err "缺少资源目录 $src，请把仓库里的 headscale-admin 目录一起上传" }
    $dst = Join-Path $AppDir $sub
    if (-not (Test-Path $dst)) { New-Item -ItemType Directory -Path $dst -Force | Out-Null }
    Copy-Item -Path (Join-Path $src '*') -Destination $dst -Recurse -Force
}
Write-Info "二进制与资源已就绪: $AppBin"

# ===================== 初始化管理员 =====================
if (-not $preserveAdmin) {
    # 口令走环境变量，不进命令行参数：命令行参数同机任何用户都能在
    # 进程列表里看到（环境变量只有同用户与管理员能读）。
    Write-Step '初始化管理员'
    if ($ResetAdmin) {
        $env:HSA_RESET_ADMIN = "${AdminUser}:$AdminPass"
    } else {
        $env:HSA_INIT_ADMIN = "${AdminUser}:$AdminPass"
    }
    $env:HSA_DATA_FILE = $DbFile
    $env:HSA_LEGACY_JSON = $LegacyJson
    & $AppBin --init-only
    if ($LASTEXITCODE -ne 0) { Write-Err '管理员初始化失败' }
    Remove-Item Env:HSA_INIT_ADMIN -ErrorAction SilentlyContinue
    Remove-Item Env:HSA_RESET_ADMIN -ErrorAction SilentlyContinue
} else {
    Write-Info '保留原有管理员账号，未改动数据库'
}

# 节点网速上报令牌：节点侧 agent 用它调用 POST /api/speed 上报累计流量计数器。
# 令牌放独立文件里，不写进任务的环境变量，避免同机用户从任务属性里直接读到。
# 已存在则沿用，保证升级不会让节点掉线。
if (-not (Test-Path $SpeedToken) -or (Get-Item $SpeedToken).Length -eq 0) {
    $bytes = New-Object byte[] 32
    [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    Set-Content -Path $SpeedToken -Value (($bytes | ForEach-Object { $_.ToString('x2') }) -join '') -Encoding ascii
}

# ===================== 管理界面：看门狗 + 计划任务 =====================
Write-Step "注册计划任务 $TaskAdmin（端口 $Port）"
$appRunContent = @'
@echo off
rem Headscale Admin supervisor.
rem Restarts the process when it dies (like systemd Restart=always).
rem Exit code 42 means "restart now" (used when the admin UI switches its own HTTPS config).
rem Output is redirected into the log file from inside this script on purpose:
rem the scheduled task action must carry a single quoted path, otherwise cmd.exe
rem mangles the command line (its /c quote-stripping rule) and fails with 0x80070001.
setlocal
set "LOG=__LOG__
set "HSA_PORT=__PORT__
set "HSA_DATA_FILE=__DB__
set "HSA_LEGACY_JSON=__LEGACY__
set "HSA_TEMPLATES=__TEMPLATES__
set "HSA_STATIC=__STATIC__
set "HSA_NODE_SPEED_TOKEN_FILE=__TOKEN__
set "HSA_HEADSCALE_BIN=__HSBIN__
set "HSA_CONFIG_FILE=__HSCONFIG__
set "HSA_RESTART_EXIT_CODE=__RESTARTCODE__
set "HEADSCALE_CONFIG=__HSCONFIG__
:loop
"%~dp0headscale-admin.exe" %* >> "%LOG%" 2>&1
set "RC=%ERRORLEVEL%"
if "%RC%"=="__RESTARTCODE__" (
    echo [%DATE% %TIME%] exit code __RESTARTCODE__, restarting in 2 seconds >> "%LOG%"
    rem timeout fails in a scheduled task ("Input redirection is not supported"),
    rem ping is the reliable headless sleep.
    ping -n 3 127.0.0.1 > nul
    goto loop
)
exit /b %RC%
'@
$appRunContent = $appRunContent.Replace('__LOG__', (Join-Path $AppLogDir 'headscale-admin.log'))
$appRunContent = $appRunContent.Replace('__PORT__', "$Port")
$appRunContent = $appRunContent.Replace('__DB__', $DbFile)
$appRunContent = $appRunContent.Replace('__LEGACY__', $LegacyJson)
$appRunContent = $appRunContent.Replace('__TEMPLATES__', (Join-Path $AppDir 'templates'))
$appRunContent = $appRunContent.Replace('__STATIC__', (Join-Path $AppDir 'static'))
$appRunContent = $appRunContent.Replace('__TOKEN__', $SpeedToken)
$appRunContent = $appRunContent.Replace('__HSBIN__', $HsBin)
$appRunContent = $appRunContent.Replace('__HSCONFIG__', $HsConfig)
$appRunContent = $appRunContent.Replace('__RESTARTCODE__', "$RestartCode")
Set-Content -Path $AppRun -Value $appRunContent -Encoding ascii

Register-SupervisorTask -TaskName $TaskAdmin -ScriptPath $AppRun -WorkDir $HsDataDir

# ===================== headscale 服务端 =====================
if ($SkipHeadscale) {
    Write-Info '按要求跳过 headscale 服务端'
} elseif (-not $HeadscaleBinary) {
    Write-Warn '未提供 -HeadscaleBinary，跳过 headscale 服务端安装。'
    Write-Warn '上游没有官方 Windows 二进制，自行编译的方法：'
    Write-Warn '  git clone https://github.com/juanfont/headscale && cd headscale'
    Write-Warn '  go build -o headscale.exe ./cmd/headscale'
    Write-Warn "装好后把 headscale.exe 放到: $HsBin"
} else {
    if (-not (Test-Path $HeadscaleBinary)) { Write-Err "headscale 二进制不存在: $HeadscaleBinary" }
    Write-Step '安装 headscale 服务端'
    Copy-Item -Path $HeadscaleBinary -Destination $HsBin -Force

    # 配置：已有就用，没有就生成一份 Windows 路径的最小可用配置。
    if ($HeadscaleConfig) {
        if (-not (Test-Path $HeadscaleConfig)) { Write-Err "配置文件不存在: $HeadscaleConfig" }
        Copy-Item -Path $HeadscaleConfig -Destination $HsConfig -Force
        Write-Info "使用已有配置: $HeadscaleConfig"
    } elseif (Test-Path $HsConfig) {
        Write-Info "已存在配置，保持不变: $HsConfig"
    } else {
        New-HsConfig
    }

    # 初始 ACL：policy.path 指向的文件必须存在，否则 headscale 起不来。
    # 内容是「全放行」，与官方示例一致。
    if (-not (Test-Path $HsPolicy)) {
        $acl = @'
{
  "acls": [
    {
      "action": "accept",
      "src": ["*"],
      "dst": ["*:*"]
    }
  ]
}
'@
        Set-Content -Path $HsPolicy -Value $acl -Encoding utf8
    }

    # 用 headscale 自带的 configtest 校验，失败即终止，不带病启动。
    Write-Step '校验 headscale 配置（configtest）'
    $ctOut = & $HsBin configtest -c $HsConfig 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host $ctOut -ForegroundColor Red
        Write-Err 'headscale 配置校验失败，已终止。'
    }
    Write-Info '配置校验通过'

    $hsRunContent = @'
@echo off
rem headscale supervisor: restarts the process 3 seconds after it exits
rem (like systemd Restart=always).
setlocal
set "LOG=__LOG__
set "HEADSCALE_CONFIG=__HSCONFIG__
:loop
"%~dp0headscale.exe" serve %* >> "%LOG%" 2>&1
set "RC=%ERRORLEVEL%"
echo [%DATE% %TIME%] headscale exited with code %RC%, restarting in 3 seconds >> "%LOG%"
ping -n 4 127.0.0.1 > nul
goto loop
'@
    $hsRunContent = $hsRunContent.Replace('__LOG__', (Join-Path $HsLogDir 'headscale.log'))
    $hsRunContent = $hsRunContent.Replace('__HSCONFIG__', $HsConfig)
    Set-Content -Path $HsRun -Value $hsRunContent -Encoding ascii
    Register-SupervisorTask -TaskName $TaskHs -ScriptPath $HsRun -WorkDir $HsDataDir
    Write-Info "headscale 计划任务已注册，日志: $(Join-Path $HsLogDir 'headscale.log')"
}

# ===================== 防火墙 =====================
Write-Step '配置防火墙'
if (-not $NoFirewall) {
    $fwParams = @{
        DisplayName = "Headscale Admin (TCP $Port)"
        Direction   = 'Inbound'
        Action      = 'Allow'
        Protocol    = 'TCP'
        LocalPort   = $Port
        Profile     = 'Any'
    }
    if ($AllowCidr) { $fwParams['RemoteAddress'] = $AllowCidr }
    Get-NetFirewallRule -DisplayName $fwParams.DisplayName -ErrorAction SilentlyContinue |
        Remove-NetFirewallRule -ErrorAction SilentlyContinue
    New-NetFirewallRule @fwParams | Out-Null
    if ($AllowCidr) {
        Write-Info "防火墙已仅允许 $AllowCidr 访问 TCP/$Port"
    } else {
        Write-Info "防火墙已对所有来源开放 TCP/$Port"
    }
} else {
    Write-Warn "按要求不修改防火墙，请自行放行 TCP/$Port"
}

# ===================== 健康检查 =====================
Write-Step '健康检查'
Start-Sleep -Seconds 3
$health = 'failed'
try {
    $resp = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 5 -UseBasicParsing
    if ($resp.Content -eq 'ok') { $health = 'ok' }
} catch {
    # 管理界面可能开在 HTTPS（数据目录下的 admin-tls.json），
    # 只探明文 HTTP 会在 HTTPS 部署下永远失败并误报，两种协议都要试。
    try {
        $p = @{ Uri = "https://127.0.0.1:$Port/health"; TimeoutSec = 5; UseBasicParsing = $true }
        # SkipCertificateCheck 是 PowerShell 6+ 才有的参数，Server 2019 自带的是 5.1。
        if ((Get-Command Invoke-WebRequest).Parameters.ContainsKey('SkipCertificateCheck')) {
            $p['SkipCertificateCheck'] = $true
        }
        $resp = Invoke-WebRequest @p
        if ($resp.Content -eq 'ok') { $health = 'ok' }
    } catch { }
}
if ($health -eq 'ok') {
    Write-Info '健康检查通过'
} else {
    Write-Warn "本机健康检查未返回 ok，请查看日志: $(Join-Path $AppLogDir 'headscale-admin.log')"
}

# ===================== 汇总 =====================
Write-Host @"

============================================================
 Headscale Admin 部署完成（Windows）
============================================================
访问地址     : http://<服务器IP>:$Port
健康检查     : http://127.0.0.1:$Port/health
数据库文件   : $DbFile
程序目录     : $AppDir
日志目录     : $AppLogDir
============
"@ -ForegroundColor Cyan
if ($preserveAdmin) {
    Write-Host "管理员账号   : 保留原密码（$AdminUser 若存在）"
} else {
    Write-Host "管理员账号   : $AdminUser"
    Write-Host "管理员密码   : $AdminPass"
    Write-Host '密码副本     : 请立即保存；服务本身只保存 PBKDF2 摘要'
}
Write-Host @"
============
常用命令：
  启动   schtasks /Run /TN $TaskAdmin
  停止   schtasks /End /TN $TaskAdmin
  状态   schtasks /Query /TN $TaskAdmin /V /FO LIST
  卸载   schtasks /Delete /TN $TaskAdmin /F
说明：
  - 服务由计划任务托管，开机自动启动
  - 修改配置/ACL 会自动备份原文件并做 configtest 校验
  - certbot 自动续期只在 Linux 上有；Windows 上请手工更换证书
============================================================
"@ -ForegroundColor Cyan
