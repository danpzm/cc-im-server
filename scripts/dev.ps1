param(
    [int]$HttpNodes = 1,
    [int]$OssNodes = 1,
    [int]$QueueNodes = 1,
    [int]$QueueConcurrencyTotal = 10,
    [int]$MediaNodes = 1,
    [int]$QuicNodes = 3,
    [int]$HttpBasePort = 8080,
    [int]$OssBasePort = 8081,
    [int]$MediaBasePort = 4434,
    [int]$QuicBasePort = 4433,
    [string]$NodeIdPrefix = "local",
    [switch]$SkipClusterServices,
    [switch]$KillExistingCluster,
    # 兼容旧参数名
    [switch]$SkipSharedServices,
    [switch]$KillExistingQuic
)

if ($SkipSharedServices) { $SkipClusterServices = $true }
if ($KillExistingQuic) { $KillExistingCluster = $true }

# PowerShell：本地多实例集群（HTTP / OSS / Queue / Media / QUIC 均可横向扩容，地址写入 Redis 供登录与媒体接口负载均衡）
Write-Host "启动本地集群..." -ForegroundColor Green

$root = Split-Path -Parent $PSScriptRoot

Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue
$tmpDir = Join-Path $root "tmp"
if (Test-Path -LiteralPath $tmpDir) {
    Get-ChildItem -LiteralPath $tmpDir -Filter *.exe -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
}

function Start-Terminal([string]$name, [string]$command) {
    Write-Host "启动 $name ..." -ForegroundColor Cyan
    Start-Process powershell -ArgumentList "-NoExit", "-Command", $command -WindowStyle Normal | Out-Null
}

function Test-UdpPortInUse([int]$port) {
    try {
        $ep = Get-NetUDPEndpoint -LocalPort $port -ErrorAction SilentlyContinue
        return $null -ne $ep
    } catch {
        return $false
    }
}

function Test-TcpPortInUse([int]$port) {
    try {
        $ep = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue
        return $null -ne $ep
    } catch {
        return $false
    }
}

function Get-NextFreeUdpPort([int]$startPort, [hashtable]$reserved) {
    $port = $startPort
    while ($reserved.ContainsKey($port) -or (Test-UdpPortInUse $port)) {
        $port++
        if ($port -gt 65535) { throw "没有可用 UDP 端口" }
    }
    $reserved[$port] = $true
    return $port
}

function Get-NextFreeTcpPort([int]$startPort, [hashtable]$reserved) {
    $port = $startPort
    while ($reserved.ContainsKey($port) -or (Test-TcpPortInUse $port)) {
        $port++
        if ($port -gt 65535) { throw "没有可用 TCP 端口" }
    }
    $reserved[$port] = $true
    return $port
}

if (-not (Get-Command air -ErrorAction SilentlyContinue)) {
    Write-Host "错误: 未找到 air，请先安装: go install github.com/cosmtrek/air@latest" -ForegroundColor Red
    exit 1
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "错误: 未找到 go" -ForegroundColor Red
    exit 1
}
foreach ($n in @($HttpNodes, $OssNodes, $QueueNodes, $MediaNodes, $QuicNodes)) {
    if ($n -lt 1) {
        Write-Host "错误: 各服务节点数必须 >= 1" -ForegroundColor Red
        exit 1
    }
}

$reservedUdp = @{}
$reservedTcp = @{}

if ($KillExistingCluster) {
    $patterns = @("cmd/quic", "cmd/http", "cmd/oss", "cmd/media", "cmd/queue", ".air.http", ".air.oss", ".air.media", ".air.queue")
    $procs = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
        $cmd = $_.CommandLine
        if (-not $cmd) { return $false }
        foreach ($p in $patterns) { if ($cmd -match [regex]::Escape($p)) { return $true } }
        return $false
    }
    foreach ($p in $procs) {
        try {
            Stop-Process -Id $p.ProcessId -Force -ErrorAction Stop
            Write-Host "已停止旧进程 PID=$($p.ProcessId)" -ForegroundColor Yellow
        } catch {
            Write-Host "停止 PID=$($p.ProcessId) 失败: $($_.Exception.Message)" -ForegroundColor DarkYellow
        }
    }
}

# 预分配端口
$quicPorts = @()
$c = $QuicBasePort
for ($i = 0; $i -lt $QuicNodes; $i++) {
    $p = Get-NextFreeUdpPort $c $reservedUdp
    $quicPorts += $p
    $c = $p + 1
}
$mediaPorts = @()
$c = $MediaBasePort
for ($i = 0; $i -lt $MediaNodes; $i++) {
    $p = Get-NextFreeUdpPort $c $reservedUdp
    $mediaPorts += $p
    $c = $p + 1
}
$httpPorts = @()
$c = $HttpBasePort
for ($i = 0; $i -lt $HttpNodes; $i++) {
    $p = Get-NextFreeTcpPort $c $reservedTcp
    $httpPorts += $p
    $c = $p + 1
}
$ossPorts = @()
$c = $OssBasePort
for ($i = 0; $i -lt $OssNodes; $i++) {
    $p = Get-NextFreeTcpPort $c $reservedTcp
    $ossPorts += $p
    $c = $p + 1
}

# QUIC 进程内也跑 asynq 消费（quic 队列 DB），多节点时按实例分摊并发，避免 Redis 单点打满
$quicQueueConcurrencyEach = [Math]::Max(2, [int][Math]::Floor($QueueConcurrencyTotal / $QuicNodes))
for ($i = 0; $i -lt $QuicNodes; $i++) {
    $idx = $i + 1
    $nodeId = "$NodeIdPrefix-quic-$idx"
    $port = $quicPorts[$i]
    $listen = "0.0.0.0:$port"
    $dial = "localhost:$port"
        $cmd = "Set-Location '${root}'; `$env:CC_DEV_CLUSTER='1'; `$env:APP_ENV='dev'; `$env:SERVER_NODE_ID='${nodeId}'; `$env:QUIC_LISTEN_ADDR='${listen}'; `$env:QUIC_CLIENT_DIAL_ADDR='${dial}'; `$env:QUEUE_CONCURRENCY='${quicQueueConcurrencyEach}'; go run ./cmd/quic"
    Start-Terminal "QUIC $nodeId (queue-concurrency=$quicQueueConcurrencyEach)" $cmd
    Start-Sleep -Milliseconds 400
}

if ($SkipClusterServices) {
    Write-Host "已跳过 HTTP/OSS/Queue/Media" -ForegroundColor Yellow
} else {
    for ($i = 0; $i -lt $MediaNodes; $i++) {
        $idx = $i + 1
        $nodeId = "$NodeIdPrefix-media-$idx"
        $port = $mediaPorts[$i]
        $listen = "0.0.0.0:$port"
        $dial = "localhost:$port"
        $cmd = "Set-Location '${root}'; `$env:CC_DEV_CLUSTER='1'; `$env:APP_ENV='dev'; `$env:SERVER_NODE_ID='${nodeId}'; `$env:MEDIA_QUIC_LISTEN_ADDR='${listen}'; `$env:MEDIA_CLIENT_DIAL_ADDR='${dial}'; air -c .air.media.toml"
        Start-Terminal "Media $nodeId" $cmd
        Start-Sleep -Milliseconds 400
    }

    for ($i = 0; $i -lt $OssNodes; $i++) {
        $idx = $i + 1
        $nodeId = "$NodeIdPrefix-oss-$idx"
        $port = $ossPorts[$i]
        $listen = ":$port"
        $base = "http://localhost:$port"
        $cmd = "Set-Location '${root}'; `$env:CC_DEV_CLUSTER='1'; `$env:APP_ENV='dev'; `$env:SERVER_NODE_ID='${nodeId}'; `$env:OSS_LISTEN_ADDR='${listen}'; `$env:OSS_CLIENT_BASE_URL='${base}'; air -c .air.oss.toml"
        Start-Terminal "OSS $nodeId" $cmd
        Start-Sleep -Milliseconds 400
    }

    $queueConcurrencyEach = [Math]::Max(2, [int][Math]::Floor($QueueConcurrencyTotal / $QueueNodes))
    Write-Host "Queue 并发: 总=$QueueConcurrencyTotal, 每实例=$queueConcurrencyEach, 实例数=$QueueNodes" -ForegroundColor DarkCyan
    for ($i = 0; $i -lt $QueueNodes; $i++) {
        $idx = $i + 1
        $nodeId = "$NodeIdPrefix-queue-$idx"
        $cmd = "Set-Location '${root}'; `$env:CC_DEV_CLUSTER='1'; `$env:APP_ENV='dev'; `$env:SERVER_NODE_ID='${nodeId}'; `$env:QUEUE_CONCURRENCY='${queueConcurrencyEach}'; air -c .air.queue.toml"
        Start-Terminal "Queue $nodeId (concurrency=$queueConcurrencyEach)" $cmd
        Start-Sleep -Milliseconds 300
    }

    Start-Sleep -Seconds 2

    for ($i = 0; $i -lt $HttpNodes; $i++) {
        $idx = $i + 1
        $nodeId = "$NodeIdPrefix-http-$idx"
        $port = $httpPorts[$i]
        $listen = ":$port"
        $base = "http://localhost:$port"
        $cmd = "Set-Location '${root}'; `$env:CC_DEV_CLUSTER='1'; `$env:APP_ENV='dev'; `$env:SERVER_NODE_ID='${nodeId}'; `$env:HTTP_LISTEN_ADDR='${listen}'; `$env:HTTP_CLIENT_BASE_URL='${base}'; `$env:INVITE_WEB_BASE_URL='${base}'; air -c .air.http.toml"
        Start-Terminal "HTTP $nodeId" $cmd
        Start-Sleep -Milliseconds 400
    }
}

if (-not $SkipClusterServices) {
    Write-Host "集群已启动: HTTP=$HttpNodes OSS=$OssNodes Queue=$QueueNodes Media=$MediaNodes QUIC=$QuicNodes" -ForegroundColor Green
    Write-Host "登录将下发 http_base_urls / oss_base_urls / quic_addrs；媒体 join 从 media 集群随机选取节点。" -ForegroundColor Green
    if ($HttpNodes -gt 1) {
        Write-Host "提示: 客户端 dev.json 的 api_base_url 可指向任一 HTTP 节点，登录后会自动切换到集群列表。" -ForegroundColor DarkCyan
    }
} else {
    Write-Host "仅 QUIC: $QuicNodes 节点" -ForegroundColor Green
}
