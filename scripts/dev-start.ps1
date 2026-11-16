<#
.SYNOPSIS
    本地启动脚本（Windows 版），与 scripts/dev-start.sh 等价。

.DESCRIPTION
    为什么要有它：仓库里很多能力由环境变量开关（DATABASE_URL / REDIS_ADDR /
    AUTH_KEY / APP_SIGNING_KEYS / SMTP_HOST …）。手工 $env:X = ... 一堆既容易漏，
    也容易把「空壳启动」误当成「完整服务」。这个脚本把「怎么起」与
    「起完该有哪些端点」写在一处。

    为什么是 .ps1 而不是 .bat：cmd 在中文/UTF-8/行尾/LF-only 的 .env 上有多个
    硬限制（代码页 936 先把非 ASCII 注释解析坏、for /f 读 LF 行尾会整块吞、
    %%X 后不能跟数字）。PowerShell 天生支持 UTF-8、任意行尾与结构化解析。

.PARAMETER ProbeOnly
    只探测已运行的服务，不启动。

.PARAMETER Stop
    停掉本脚本起的服务（按 PID 文件 + 端口兜底）。

.PARAMETER Foreground
    前台运行（Ctrl+C 停止），便于直接看输出。

.PARAMETER Build
    先编译成 .exe 再运行（启动更快；改了代码要重跑本参数）。

.PARAMETER NoProbe
    启动后跳过端点探测。

.EXAMPLE
    .\scripts\dev-start.ps1
    .\scripts\dev-start.ps1 -ProbeOnly
    .\scripts\dev-start.ps1 -Stop
    .\scripts\dev-start.ps1 -Build

.NOTES
    约定：
      - 优先读仓库根的 .env（已在 .gitignore，禁提交真实值）；无 .env 时用当前环境变量。
      - **不打印任何密钥值**，只打印「已设置 / 未设置」。
      - 探测用 curl.exe，逐 URL 独立取状态码（不靠响应体猜）。
#>
[CmdletBinding()]
param(
    [switch]$ProbeOnly,
    [switch]$Stop,
    [switch]$Foreground,
    [switch]$Build,
    [switch]$NoProbe
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# ---- 定位仓库根 ----
#
# 正常情况：脚本在 <root>\scripts\ 下，取上一级即仓库根。
#
# 但不能盲信这个推导：脚本被复制到别处（实测踩到——有人把 dev-start.ps1
# 拷到仓库根后运行，于是「根」变成了 apps\，.env 找不到、日志写到 apps\ 下、
# go run 也报 cannot find main module）。
# 因此这里做一次校验：推导出的目录必须含 go.mod；否则从脚本所在目录向上找。
function Resolve-RepoRoot([string]$StartDir) {
    $candidate = $null
    $dir = $StartDir
    for ($i = 0; $i -lt 5; $i++) {
        if (Test-Path -LiteralPath (Join-Path $dir 'go.mod')) { $candidate = $dir; break }
        $parent = Split-Path -Parent $dir
        if ([string]::IsNullOrEmpty($parent) -or $parent -eq $dir) { break }
        $dir = $parent
    }
    return $candidate
}

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path

# 先按约定推导（脚本父目录），再校验；不行就从脚本所在目录向上找
$Root = $null
$guess = (Resolve-Path (Join-Path $ScriptDir '..') -ErrorAction SilentlyContinue)
if ($guess) { $Root = Resolve-RepoRoot -StartDir $guess.Path }
if (-not $Root) { $Root = Resolve-RepoRoot -StartDir $ScriptDir }
if (-not $Root) {
    Write-Host "[env] 找不到仓库根（向上 5 层内没有 go.mod）。请把脚本放在 <repo>\scripts\ 下运行。" -ForegroundColor Red
    exit 1
}
if ($ScriptDir -ne (Join-Path $Root 'scripts')) {
    Write-Host "[env] 注意：脚本不在 <repo>\scripts\ 下（当前：$ScriptDir），已按 go.mod 定位仓库根：$Root" -ForegroundColor Yellow
}
Set-Location -LiteralPath $Root

$PidFile = Join-Path $Root '.dev-server.pid'
$LogFile = Join-Path $Root '.dev-server.log'
$ErrFile = Join-Path $Root '.dev-server.err.log'
$BinFile = Join-Path $Root '.dev-server.exe'

# ============================================================================
#  工具函数
# ============================================================================

function Write-Section([string]$Text) {
    Write-Host ''
    Write-Host "=== $Text ===" -ForegroundColor Cyan
}

# 只报「已设置/未设置」，绝不打印值
function Show-EnvSwitches {
    $keys = @(
        'APP_ENV', 'ADDR', 'AUTH_KEY', 'APP_SIGNING_KEYS', 'DATABASE_URL',
        'REDIS_ADDR', 'REDIS_PASSWORD', 'SMTP_HOST', 'TICKET_KEY'
    )
    foreach ($k in $keys) {
        $v = [Environment]::GetEnvironmentVariable($k)
        $state = if ([string]::IsNullOrEmpty($v)) { '未设置' } else { '已设置' }
        $color = if ([string]::IsNullOrEmpty($v)) { 'DarkGray' } else { 'Green' }
        Write-Host ("  {0,-20} {1}" -f $k, $state) -ForegroundColor $color
    }
}

# 读 .env，不覆盖已存在的环境变量；支持 UTF-8 / CRLF / LF / 值内 '=' / 成对引号
function Import-DotEnv([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) {
        Write-Host '[env] 未找到 .env（用当前环境变量；完整能力需要 DATABASE_URL / REDIS_ADDR / AUTH_KEY）' -ForegroundColor Yellow
        return 0
    }

    $loaded = 0
    $skipped = 0
    foreach ($raw in Get-Content -LiteralPath $Path -Encoding UTF8) {
        $line = $raw -replace "`r$", ''

        if ($line -match '^\s*$') { continue }
        if ($line -match '^\s*#') { continue }
        if ($line -notmatch '^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$') { continue }

        $key = $Matches[1]
        $val = $Matches[2].Trim()

        # 去掉一层成对引号
        if ($val.Length -ge 2) {
            $first = $val[0]
            $last = $val[$val.Length - 1]
            if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                $val = $val.Substring(1, $val.Length - 2)
            }
        }

        # 与 .sh 语义一致：已存在的环境变量优先
        if ([string]::IsNullOrEmpty([Environment]::GetEnvironmentVariable($key))) {
            [Environment]::SetEnvironmentVariable($key, $val, 'Process')
            $loaded++
        } else {
            $skipped++
        }
    }

    Write-Host ("[env] 已读取 .env（载入 {0} 项，跳过已存在 {1} 项）" -f $loaded, $skipped) -ForegroundColor Green
    return $loaded
}

# 从 ADDR 推导端口（默认 :8080，与 .sh 一致）
function Get-Port {
    $addr = [Environment]::GetEnvironmentVariable('ADDR')
    if ([string]::IsNullOrEmpty($addr)) { return 8080 }
    $idx = $addr.LastIndexOf(':')
    if ($idx -lt 0) { return 8080 }
    $p = $addr.Substring($idx + 1)
    $n = 0
    if ([int]::TryParse($p, [ref]$n) -and $n -gt 0) { return $n }
    return 8080
}

# 找出监听指定端口的进程 PID（可能多个，去重）
function Get-PortOwners([int]$Port) {
    $pids = @()
    try {
        $conns = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
        if ($conns) { $pids = $conns | Select-Object -ExpandProperty OwningProcess }
    } catch {
        # Get-NetTCPConnection 在部分环境不可用，回退 netstat
        $lines = netstat -ano | Select-String -Pattern ":$Port\s" | Select-String -Pattern 'LISTENING'
        foreach ($l in $lines) {
            $parts = ($l.Line -split '\s+') | Where-Object { $_ }
            if (@($parts).Count -ge 5) { $pids += [int]@($parts)[-1] }
        }
    }
    return ($pids | Where-Object { $_ -and $_ -ne 0 } | Select-Object -Unique)
}

# 读 PID 文件
function Get-PidFileValue {
    if (Test-Path -LiteralPath $PidFile) {
        $raw = (Get-Content -LiteralPath $PidFile -ErrorAction SilentlyContinue | Select-Object -First 1)
        $n = 0
        if ([int]::TryParse("$raw".Trim(), [ref]$n)) { return $n }
    }
    return 0
}

# 服务是否在跑（PID 文件优先，端口兜底）
function Test-ServiceRunning([int]$Port) {
    $pidFromFile = Get-PidFileValue
    if ($pidFromFile -gt 0) {
        if (Get-Process -Id $pidFromFile -ErrorAction SilentlyContinue) { return $true }
    }
    $owners = Get-PortOwners -Port $Port
    return (@($owners).Count -gt 0)
}

# 探测一个 URL，返回状态码字符串（失败返回 ---）
#
# 注意：curl 连不上时会往 stderr 写 "curl: (7) Failed to connect"，而本脚本设了
# $ErrorActionPreference='Stop'，原生命令的 stderr 会被 PowerShell 当成终止错误。
# 探测一个「本来就没在跑」的服务是正常场景，必须把 stderr 吞掉、退出码视作失败。
function Get-HttpCode([string]$Url, [int]$TimeoutSec = 5) {
    $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
    if (-not $curl) { return '(无 curl)' }

    $prevEap = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $out = & $curl.Source -sS -o NUL -w '%{http_code}' --max-time $TimeoutSec $Url 2>$null
        $code = "$out".Trim()
    } catch {
        return '---'
    } finally {
        $ErrorActionPreference = $prevEap
    }

    if ([string]::IsNullOrWhiteSpace($code) -or $code -eq '000') { return '---' }
    return $code
}

# 逐端点探测
function Invoke-Probe([string]$Base) {
    Write-Section "端点探测 ($Base)"

    $paths = @(
        '/healthz', '/status',
        '/announcements', '/announcements/latest',
        '/posts', '/posts/1',
        '/messages', '/messages/unread-count',
        '/track/events/summary',
        '/admin/announcements'
    )

    foreach ($p in $paths) {
        $code = Get-HttpCode -Url ($Base + $p) -TimeoutSec 5
        $color = switch -Wildcard ($code) {
            '200' { 'Green' }
            '401' { 'Yellow' }
            '403' { 'Yellow' }
            '404' { 'DarkGray' }
            default { 'DarkGray' }
        }
        Write-Host ("  {0,-28} {1}" -f $p, $code) -ForegroundColor $color
    }

    Write-Host ''
    Write-Host '状态码含义：200=可用；401=需登录（中间件已生效）；403=权限门槛拒；404=该域尚未实现/未挂载。'
    Write-Host '⚠ 注意：401 不能证明端点「已实现」——全局鉴权中间件挂在所有路由之前，'
    Write-Host '   未注册的路径也会先被它拦成 401。要确认某端点真的注册了，看启动日志的 [GIN-debug] 路由表：'
    Write-Host ("   Get-Content '{0}' | Select-String 'GIN-debug'" -f $LogFile) -ForegroundColor DarkGray
}

# 打印日志尾部
function Show-LogTail([int]$Lines = 8) {
    if (-not (Test-Path -LiteralPath $LogFile)) {
        Write-Host '          (无日志文件)' -ForegroundColor DarkGray
        return
    }
    Get-Content -LiteralPath $LogFile -Encoding UTF8 -Tail $Lines | ForEach-Object {
        Write-Host "          $_" -ForegroundColor DarkGray
    }
}

# 从日志里取装配完成行
function Get-AssemblyLine {
    if (-not (Test-Path -LiteralPath $LogFile)) { return $null }
    $line = Get-Content -LiteralPath $LogFile -Encoding UTF8 |
        Select-String -Pattern '"msg":"装配完成"' -SimpleMatch |
        Select-Object -First 1
    if ($line) { return $line.Line }
    return $null
}

# 从日志里取 WARN/ERROR 行（去重）
function Get-WarnLines {
    if (-not (Test-Path -LiteralPath $LogFile)) { return @() }
    return Get-Content -LiteralPath $LogFile -Encoding UTF8 |
        Select-String -Pattern '"level":"(WARN|ERROR)"' |
        ForEach-Object { $_.Line } |
        Select-Object -Unique
}

# 日志里是否出现「装配失败」
function Test-AssemblyFailed {
    if (-not (Test-Path -LiteralPath $LogFile)) { return $false }
    $hit = Get-Content -LiteralPath $LogFile -Encoding UTF8 |
        Select-String -Pattern '"msg":"装配失败"' -SimpleMatch |
        Select-Object -Last 1
    return [bool]$hit
}

# 等 /healthz 返回 200
#
# 关键：必须比对**真实状态码**，不能只判「输出非空」。curl 在服务未起时会输出
# 000（非空！），据此 break 就会在编译还没完时去探测，结果所有端点都报 000——
# 看起来像服务挂了，其实只是探早了。（这个坑在 .sh 里已经踩过）
function Wait-Ready([string]$Base, [int]$TimeoutSec = 90) {
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        $code = Get-HttpCode -Url ($Base + '/healthz') -TimeoutSec 1
        if ($code -eq '200') { return $true }

        # 子进程可能已经挂了，别白等到超时
        if (Test-AssemblyFailed) { return $false }

        Start-Sleep -Milliseconds 500
    }
    return $false
}

# ============================================================================
#  动作：Stop
# ============================================================================
function Invoke-Stop {
    $port = Get-Port
    $stopped = $false

    $pidFromFile = Get-PidFileValue
    if ($pidFromFile -gt 0) {
        $proc = Get-Process -Id $pidFromFile -ErrorAction SilentlyContinue
        if ($proc) {
            Write-Host "[stop] 结束 pid=$pidFromFile（$($proc.ProcessName)）" -ForegroundColor Yellow
            # /T 连带子进程：go run 会再拉起真正的 server.exe
            & taskkill /PID $pidFromFile /T /F 2>&1 | Out-Null
            $stopped = $true
        } else {
            Write-Host "[stop] pid=$pidFromFile 已不在运行" -ForegroundColor DarkGray
        }
        Remove-Item -LiteralPath $PidFile -Force -ErrorAction SilentlyContinue
    } else {
        Write-Host '[stop] 无 PID 文件' -ForegroundColor DarkGray
    }

    # 端口兜底：go run 的 server 子进程、或 PID 文件丢失时
    $owners = Get-PortOwners -Port $port
    foreach ($ownerPid in $owners) {
        if ($ownerPid -eq $PID) { continue }  # 别杀自己
        Write-Host "[stop] 结束占用端口 $port 的进程 pid=$ownerPid" -ForegroundColor Yellow
        & taskkill /PID $ownerPid /T /F 2>&1 | Out-Null
        $stopped = $true
    }

    Start-Sleep -Milliseconds 800
    $still = Get-PortOwners -Port $port
    if (@($still).Count -gt 0) {
        Write-Host "[stop] ⚠ 端口 $port 仍被占用：$($still -join ', ')" -ForegroundColor Red
        return 1
    }

    if ($stopped) {
        Write-Host '[stop] 已停止（exe 模式可手动删除 .dev-server.exe）' -ForegroundColor Green
    } else {
        Write-Host '[stop] 未发现运行中的实例' -ForegroundColor DarkGray
    }
    return 0
}

# ============================================================================
#  动作：Start
# ============================================================================
function Invoke-Start {
    $port = Get-Port
    $base = "http://127.0.0.1:$port"

    if (Test-ServiceRunning -Port $port) {
        $existing = Get-PidFileValue
        $hint = if ($existing -gt 0) { "pid=$existing" } else { "端口 $port" }
        Write-Host "[start] 检测到已有实例（$hint），先 -Stop 再启动" -ForegroundColor Yellow
        return 1
    }

    Write-Section '环境开关'
    Show-EnvSwitches

    # ---- 前台 ----
    if ($Foreground) {
        Write-Host ''
        Write-Host "[start] 前台运行（Ctrl+C 停止）。日志同时写入 $LogFile" -ForegroundColor Cyan
        # Tee 让输出既上屏又进日志
        & go run ./cmd/server 2>&1 | Tee-Object -FilePath $LogFile
        Write-Host '[start] 已退出'
        return 0
    }

    # ---- 清理旧日志 ----
    Remove-Item -LiteralPath $LogFile -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $ErrFile -Force -ErrorAction SilentlyContinue

    # ---- 启动子进程 ----
    if ($Build) {
        Write-Host ''
        Write-Host "[start] 编译到 $BinFile" -ForegroundColor Cyan
        & go build -o $BinFile ./cmd/server
        if ($LASTEXITCODE -ne 0) {
            Write-Host '[start] 编译失败，未启动' -ForegroundColor Red
            return 1
        }
        $exe = $BinFile
        $argList = @()
    } else {
        $exe = 'go'
        $argList = @('run', './cmd/server')
    }

    Write-Host ''
    $modeText = if ($Build) { 'exe' } else { 'go run' }
    Write-Host "[start] 启动（$modeText），日志：$LogFile" -ForegroundColor Cyan

    $proc = $null
    if ($argList -and @($argList).Count -gt 0) {
        $proc = Start-Process -FilePath $exe -ArgumentList $argList `
            -WorkingDirectory $Root -WindowStyle Hidden -PassThru `
            -RedirectStandardOutput $LogFile -RedirectStandardError $ErrFile
    } else {
        # Start-Process 不接受空 ArgumentList（exe 模式没有参数）
        $proc = Start-Process -FilePath $exe `
            -WorkingDirectory $Root -WindowStyle Hidden -PassThru `
            -RedirectStandardOutput $LogFile -RedirectStandardError $ErrFile
    }

    Write-Host "[start] 已拉起 pid=$($proc.Id)" -ForegroundColor Green

    # ---- 等就绪 ----
    $ready = Wait-Ready -Base $base -TimeoutSec 90
    if (-not $ready) {
        Write-Host '[start] ⚠ 等待就绪超时（90s）；服务可能仍在编译或启动失败。' -ForegroundColor Red
        Write-Host '        最近日志：'
        Show-LogTail 10
    }

    # PID 文件优先记「真正监听端口」的进程。
    # `go run` 会再拉起 server.exe，$proc.Id 是 go 的 PID，不是服务本身的；
    # 记成 go 的 PID 会让 -Stop 必须依赖端口兜底。端口已在监听就记占用者。
    $listenPids = Get-PortOwners -Port $port
    $recordPid = if (@($listenPids).Count -gt 0) { @($listenPids)[0] } else { $proc.Id }
    Set-Content -LiteralPath $PidFile -Value $recordPid -Encoding ASCII
    if ($recordPid -ne $proc.Id) {
        Write-Host "[start] PID 文件记录端口占用者 pid=$recordPid（启动器 pid=$($proc.Id)）" -ForegroundColor DarkGray
    }

    # ---- 装配失败就直接失败，不要谎报「服务在运行」----
    if (Test-AssemblyFailed) {
        Write-Host ''
        Write-Host '[start] 装配失败（服务未启动）。真实原因：' -ForegroundColor Red
        Show-LogTail 12
        Write-Host "          修复后重试；完整日志：$LogFile"
        Remove-Item -LiteralPath $PidFile -Force -ErrorAction SilentlyContinue
        return 1
    }

    # ---- 装配摘要 ----
    $assembly = Get-AssemblyLine
    if ($assembly) {
        Write-Host '[start] 装配摘要：' -ForegroundColor Cyan
        Write-Host "  $assembly"
    }

    # ---- WARN ----
    $warns = Get-WarnLines
    if (@($warns).Count -gt 0) {
        Write-Host '[start] 告警（未启用能力 / 降级）：' -ForegroundColor Yellow
        foreach ($w in $warns) { Write-Host "  - $w" -ForegroundColor DarkYellow }
    }

    # ---- 探测 ----
    if (-not $NoProbe) {
        Invoke-Probe -Base $base
    }

    Write-Host ''
    Write-Host "[start] 服务在后台运行（pid=$(Get-PidFileValue)）。" -ForegroundColor Green
    Write-Host "        查看日志：Get-Content '$LogFile' -Wait -Tail 30"
    Write-Host "        停止服务：.\scripts\dev-start.ps1 -Stop"
    return 0
}

# ============================================================================
#  主流程
# ============================================================================

Import-DotEnv -Path (Join-Path $Root '.env') | Out-Null

if ($Stop) {
    exit (Invoke-Stop)
}

if ($ProbeOnly) {
    $port = Get-Port
    Write-Section '环境开关'
    Show-EnvSwitches
    Invoke-Probe -Base "http://127.0.0.1:$port"
    # 探测只是体检，服务没起也不算脚本失败（退出码 0）。
    # 需要机器可读结果时看输出的状态码列（--- 表示连不上）。
    exit 0
}

exit (Invoke-Start)
