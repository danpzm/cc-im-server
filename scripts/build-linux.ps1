param(
    [ValidateSet("linux")]
    [string]$GoOs = "linux",
    [ValidateSet("amd64", "arm64")]
    [string]$GoArch = "amd64",
    [ValidateSet("dev", "prod")]
    [string]$AppEnv = "prod",
    [string]$OutputDir = ""
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location -Path $root

function Write-Utf8Lf([string]$Path, [string]$Content) {
    $normalized = $Content -replace "`r`n", "`n"
    [System.IO.File]::WriteAllText($Path, $normalized, (New-Object System.Text.UTF8Encoding($false)))
}

function Remove-TreeWithRetry {
    param(
        [string]$LiteralPath,
        [int]$MaxAttempts = 30,
        [int]$DelayMs = 400,
        [int]$RenameDepth = 0
    )
    if (-not (Test-Path -LiteralPath $LiteralPath)) {
        return
    }
    for ($a = 1; $a -le $MaxAttempts; $a++) {
        try {
            Remove-Item -LiteralPath $LiteralPath -Recurse -Force -ErrorAction Stop
            return
        } catch {
            Write-Host "等待目录释放占用: $LiteralPath ($a/$MaxAttempts)" -ForegroundColor DarkYellow
            Start-Sleep -Milliseconds $DelayMs
        }
    }
    if ($RenameDepth -ge 1) {
        throw "仍无法删除目录（已尝试过重命名）。请手动关闭占用后删除: $LiteralPath"
    }
    $parent = [System.IO.Path]::GetDirectoryName($LiteralPath)
    $leaf = [System.IO.Path]::GetFileName($LiteralPath)
    $bak = Join-Path $parent ($leaf + ".trash-" + [guid]::NewGuid().ToString("N").Substring(0, 12))
    try {
        Move-Item -LiteralPath $LiteralPath -Destination $bak -Force -ErrorAction Stop
        Write-Host "已将旧目录重命名为 $bak ，正在删除..." -ForegroundColor Yellow
        Remove-TreeWithRetry -LiteralPath $bak -MaxAttempts $MaxAttempts -DelayMs $DelayMs -RenameDepth 1
        return
    } catch {
        throw "无法在多次尝试内清空或移走目录。请关闭占用以下路径的程序（IDE 预览、资源管理器窗口、杀毒、仍在运行的服务）后重试: $LiteralPath`n$($_.Exception.Message)"
    }
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "错误: 未找到 go 命令，请先安装 Go" -ForegroundColor Red
    exit 1
}

if ([string]::IsNullOrWhiteSpace($OutputDir)) {
    $bundleRoot = [System.IO.Path]::GetFullPath((Join-Path $root "dist/$GoOs-$GoArch"))
} else {
    $bundleRoot = [System.IO.Path]::GetFullPath((Join-Path $root $OutputDir.Trim()))
}

$envSourceDir = Join-Path $root "env\$AppEnv"
$sharedSource = Join-Path $envSourceDir ".env.shared"
if (-not (Test-Path -LiteralPath $sharedSource)) {
    Write-Host "错误: 缺少 $sharedSource，请执行: .\scripts\init-env.ps1 -Env $AppEnv" -ForegroundColor Red
    exit 1
}
Write-Host "使用配置目录: env\$AppEnv" -ForegroundColor Cyan

$certSourceDir = Join-Path $root "cert"
if (-not (Test-Path $certSourceDir)) {
    Write-Host "错误: 缺少证书目录 $certSourceDir" -ForegroundColor Red
    exit 1
}

$geodbSourceDir = Join-Path $root "geodb"
$geodbPresent = Test-Path -LiteralPath $geodbSourceDir
if (-not $geodbPresent) {
    Write-Host "提示: 未找到 GeoIP 目录 $geodbSourceDir ，将跳过向 http/quic 复制 geodb（请自行放置 GeoLite2-City.mmdb）" -ForegroundColor DarkYellow
}

$infraSrc = Join-Path $root "scripts\infra.sh"
$composeSrc = Join-Path $root "docker-compose.infra.yml"
if (-not (Test-Path -LiteralPath $infraSrc)) {
    Write-Host "错误: 缺少 $infraSrc" -ForegroundColor Red
    exit 1
}
if (-not (Test-Path -LiteralPath $composeSrc)) {
    Write-Host "错误: 缺少 $composeSrc" -ForegroundColor Red
    exit 1
}

$cmds = @("http", "quic", "queue", "oss", "media")
$hadCgo = Test-Path Env:\CGO_ENABLED
$oldCgo = $env:CGO_ENABLED
$hadGoos = Test-Path Env:\GOOS
$oldGoos = $env:GOOS
$hadGoarch = Test-Path Env:\GOARCH
$oldGoarch = $env:GOARCH
try {
    $env:CGO_ENABLED = "0"
    $env:GOOS = $GoOs
    $env:GOARCH = $GoArch

    Write-Host "输出目录: $bundleRoot" -ForegroundColor Cyan
    Write-Host "清空输出目录（若被占用将重试，必要时整目录重命名后再删）..." -ForegroundColor Yellow
    Remove-TreeWithRetry -LiteralPath $bundleRoot
    New-Item -ItemType Directory -Path $bundleRoot -Force | Out-Null

    foreach ($name in $cmds) {
        $svcPath = Join-Path $envSourceDir ".env.$name"
        if (-not (Test-Path -LiteralPath $svcPath)) {
            throw "缺少 $svcPath（可先执行 .\scripts\init-env.ps1 -Env $AppEnv）"
        }

        $svcDir = Join-Path $bundleRoot $name
        $envDir = Join-Path $svcDir "env"
        New-Item -ItemType Directory -Path $envDir -Force | Out-Null

        $binOut = Join-Path $svcDir $name
        Write-Host "构建 ./cmd/$name -> $binOut" -ForegroundColor Cyan
        go build -trimpath -ldflags "-s -w" -o $binOut "./cmd/$name"

        Write-Utf8Lf (Join-Path $envDir ".env.shared") (Get-Content -LiteralPath $sharedSource -Raw)
        Write-Utf8Lf (Join-Path $envDir ".env.$name") (Get-Content -LiteralPath $svcPath -Raw)

        # QUIC / 媒体 QUIC 各自独立进程，各自目录下各带一份 cert/
        if ($name -eq "quic" -or $name -eq "media") {
            $certTargetDir = Join-Path $svcDir "cert"
            Copy-Item -Path $certSourceDir -Destination $certTargetDir -Recurse -Force
        }

        # GeoIP2 库仅 HTTP / QUIC 使用，打进各自服务目录下的 geodb/
        if ($geodbPresent -and ($name -eq "http" -or $name -eq "quic")) {
            $geodbTargetDir = Join-Path $svcDir "geodb"
            Copy-Item -Path $geodbSourceDir -Destination $geodbTargetDir -Recurse -Force
        }

        $startTpl = @'
#!/usr/bin/env bash
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$DIR"
export CONFIG_DIR="$DIR/env"
exec "./__SERVICE__"
'@
        $startSh = $startTpl.Replace('__SERVICE__', $name)
        Write-Utf8Lf (Join-Path $svcDir "start.sh") $startSh
    }

    Copy-Item -LiteralPath $sharedSource -Destination (Join-Path $bundleRoot "infra.env") -Force
    Copy-Item $infraSrc (Join-Path $bundleRoot "infra.sh") -Force
    Write-Utf8Lf (Join-Path $bundleRoot "infra.sh") (Get-Content (Join-Path $bundleRoot "infra.sh") -Raw)
    Copy-Item $composeSrc (Join-Path $bundleRoot "docker-compose.infra.yml") -Force
    Write-Utf8Lf (Join-Path $bundleRoot "docker-compose.infra.yml") (Get-Content (Join-Path $bundleRoot "docker-compose.infra.yml") -Raw)

    $serverSh = @'
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ALL_SERVICES=(http queue oss media quic)
services=()
use_all=false
ACTION="${1:-}"

usage() {
  echo "用法: $(basename "$0") <start|stop|restart|status> -all | -s <svc1,svc2,...>"
  echo "服务名: http, queue, oss, media, quic"
  echo "示例:"
  echo "  $(basename "$0") start -all"
  echo "  $(basename "$0") start -s http,quic"
  echo "  $(basename "$0") stop -s quic"
  echo "  $(basename "$0") restart -all"
  echo "  $(basename "$0") status -all"
  exit 1
}

is_valid_service() {
  local s="$1"
  for x in "${ALL_SERVICES[@]}"; do
    [[ "$x" == "$s" ]] && return 0
  done
  return 1
}

parse_services() {
  local input="$1"
  IFS=',' read -r -a arr <<<"$input"
  for raw in "${arr[@]}"; do
    local svc
    svc="$(echo "$raw" | xargs)"
    [[ -z "$svc" ]] && continue
    if ! is_valid_service "$svc"; then
      echo "错误: 未知服务 '$svc'"
      usage
    fi
    services+=("$svc")
  done
}

read_infra_kv() {
  local key="$1"
  grep -m1 "^${key}=" "$ROOT/infra.env" 2>/dev/null | cut -d= -f2- | tr -d '\r' || true
}

queue_replicas() {
  local n="${QUEUE_REPLICAS:-1}"
  if [[ "$n" =~ ^[0-9]+$ ]] && [[ "$n" -ge 1 ]]; then
    echo "$n"
  else
    echo 1
  fi
}

queue_concurrency_each() {
  local total="${QUEUE_CONCURRENCY_TOTAL:-10}"
  local reps
  reps="$(queue_replicas)"
  if [[ "$total" =~ ^[0-9]+$ ]] && [[ "$reps" -ge 1 ]]; then
    local each=$(( total / reps ))
    [[ "$each" -lt 2 ]] && each=2
    echo "$each"
  else
    echo 4
  fi
}

start_queue_replicas() {
  local reps i each
  reps="$(queue_replicas)"
  each="$(queue_concurrency_each)"
  if [[ ! -d "$ROOT/queue" ]] || [[ ! -f "$ROOT/queue/start.sh" ]] || [[ ! -f "$ROOT/queue/queue" ]]; then
    echo "错误: 缺少 queue 服务目录或二进制"
    exit 1
  fi
  chmod +x "$ROOT/queue/start.sh" "$ROOT/queue/queue"
  echo "启动 queue x${reps}（每实例 QUEUE_CONCURRENCY=${each}）..."
  for ((i = 1; i <= reps; i++)); do
    cd "$ROOT/queue"
    SERVER_NODE_ID="${SERVER_NODE_ID_PREFIX:-node}-queue-${i}" \
      QUEUE_CONCURRENCY="$each" \
      DB_AUTO_MIGRATE="false" \
      nohup ./start.sh >>"$ROOT/logs/queue-${i}.log" 2>&1 &
    echo $! >"$ROOT/run/queue-${i}.pid"
    echo "  queue-${i} pid=$(<"$ROOT/run/queue-${i}.pid") log=$ROOT/logs/queue-${i}.log"
    cd "$ROOT"
  done
}

start_services() {
  chmod +x "$ROOT/infra.sh" "$ROOT/server.sh" "$ROOT/start-all.sh" "$ROOT/stop-all.sh" "$ROOT/restart-all.sh" 2>/dev/null || true
  mkdir -p "$ROOT/logs" "$ROOT/run"

  for svc in "${services[@]}"; do
    if [[ "$svc" == "queue" ]]; then
      start_queue_replicas
      continue
    fi
    if [[ ! -d "$ROOT/$svc" ]] || [[ ! -f "$ROOT/$svc/start.sh" ]] || [[ ! -f "$ROOT/$svc/$svc" ]]; then
      echo "错误: 缺少服务目录或文件: $ROOT/$svc（需含 start.sh 与二进制 $svc）"
      exit 1
    fi
    chmod +x "$ROOT/$svc/start.sh" "$ROOT/$svc/$svc"
    local migrate="false"
    [[ "$svc" == "http" ]] && migrate="true"
    echo "启动 $svc ..."
    cd "$ROOT/$svc"
    DB_AUTO_MIGRATE="$migrate" nohup ./start.sh >>"$ROOT/logs/$svc.log" 2>&1 &
    echo $! >"$ROOT/run/$svc.pid"
    echo "  pid=$(<"$ROOT/run/$svc.pid") migrate=$migrate log=$ROOT/logs/$svc.log"
    cd "$ROOT"
  done
  echo "已启动服务: ${services[*]}（后台）。日志: $ROOT/logs/"

  local pg_port="5432"
  local rd_port="6379"
  if [[ -f "$ROOT/infra.env" ]]; then
    local v
    v="$(read_infra_kv POSTGRES_PORT)"; [[ -n "$v" ]] && pg_port="$v"
    v="$(read_infra_kv REDIS_PORT)"; [[ -n "$v" ]] && rd_port="$v"
  fi
  echo ""
  echo "提示: server.sh 仅管理业务进程，不会自动启动 Docker 基础设施。"
  echo "如需启动/停止容器，请单独执行: $ROOT/infra.sh up|down|restart|ps|logs"
  echo "防火墙: 放行 TCP $pg_port（Postgres）、$rd_port（Redis），以及各服务 env 中配置的对外端口（常见默认: http 6666、oss 6667、quic 4433、media-quic 4578）。"
}

stop_one_pidfile() {
  local name="$1"
  local f="$2"
  [[ -e "$f" ]] || { echo "$name 未运行（无 pid 文件）"; return 0; }
  local pid
  pid="$(<"$f")"
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid" || true
    echo "已停止 $name pid=$pid"
  else
    echo "$name 进程已不存在 pid=$pid"
  fi
  rm -f "$f"
}

stop_services() {
  if [[ ! -d "$ROOT/run" ]]; then
    echo "无 run 目录"
    return 0
  fi
  for name in "${services[@]}"; do
    if [[ "$name" == "queue" ]]; then
      local i reps
      reps="$(queue_replicas)"
      for ((i = 1; i <= reps; i++)); do
        stop_one_pidfile "queue-${i}" "$ROOT/run/queue-${i}.pid"
      done
      rm -f "$ROOT/run/queue.pid"
      continue
    fi
    stop_one_pidfile "$name" "$ROOT/run/$name.pid"
  done
}

status_one_pidfile() {
  local name="$1"
  local f="$2"
  local log="$3"
  if [[ ! -e "$f" ]]; then
    echo "$name: stopped (无 pid 文件)"
    return 0
  fi
  local pid
  pid="$(<"$f")"
  if kill -0 "$pid" 2>/dev/null; then
    echo "$name: running pid=$pid log=$log"
  else
    echo "$name: stale pid=$pid (进程不存在)"
  fi
}

status_services() {
  if [[ ! -d "$ROOT/run" ]]; then
    echo "无 run 目录"
    return 0
  fi
  for name in "${services[@]}"; do
    if [[ "$name" == "queue" ]]; then
      local i reps
      reps="$(queue_replicas)"
      for ((i = 1; i <= reps; i++)); do
        status_one_pidfile "queue-${i}" "$ROOT/run/queue-${i}.pid" "$ROOT/logs/queue-${i}.log"
      done
      continue
    fi
    status_one_pidfile "$name" "$ROOT/run/$name.pid" "$ROOT/logs/$name.log"
  done
}

[[ "$ACTION" == "start" || "$ACTION" == "stop" || "$ACTION" == "restart" || "$ACTION" == "status" ]] || usage
shift || true

if [[ $# -eq 0 ]]; then
  use_all=true
else
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -all)
        use_all=true
        shift
        ;;
      -s)
        [[ $# -lt 2 ]] && usage
        parse_services "$2"
        shift 2
        ;;
      *)
        usage
        ;;
    esac
  done
fi

if [[ "$use_all" == true ]]; then
  services=("${ALL_SERVICES[@]}")
fi
[[ ${#services[@]} -eq 0 ]] && usage

case "$ACTION" in
  start)
    start_services
    ;;
  stop)
    stop_services
    ;;
  restart)
    stop_services
    start_services
    ;;
  status)
    status_services
    ;;
esac
'@

    $startAll = @'
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$ROOT/server.sh" start "$@"
'@

    $stopAll = @'
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$ROOT/server.sh" stop "$@"
'@

    $restartAll = @'
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$ROOT/server.sh" restart "$@"
'@

    Write-Utf8Lf (Join-Path $bundleRoot "server.sh") $serverSh
    Write-Utf8Lf (Join-Path $bundleRoot "start-all.sh") $startAll
    Write-Utf8Lf (Join-Path $bundleRoot "stop-all.sh") $stopAll
    Write-Utf8Lf (Join-Path $bundleRoot "restart-all.sh") $restartAll
}
catch {
    Write-Host "构建失败: $($_.Exception.Message)" -ForegroundColor Red
    exit 1
}
finally {
    if ($hadCgo) { $env:CGO_ENABLED = $oldCgo } else { Remove-Item Env:\CGO_ENABLED -ErrorAction SilentlyContinue }
    if ($hadGoos) { $env:GOOS = $oldGoos } else { Remove-Item Env:\GOOS -ErrorAction SilentlyContinue }
    if ($hadGoarch) { $env:GOARCH = $oldGoarch } else { Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue }
}

Write-Host "构建完成: $bundleRoot" -ForegroundColor Green
Write-Host "  各服务目录: $($cmds -join ', ')" -ForegroundColor Green
Write-Host "  根目录: Linux 上先 ./infra.sh up，再 ./server.sh start -all（容器与业务进程分离管理）" -ForegroundColor Green
