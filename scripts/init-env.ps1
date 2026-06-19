param(
    [ValidateSet("dev", "prod")]
    [string]$Env = "dev",
    [switch]$Force
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$dir = Join-Path $root "env\$Env"

if (-not (Test-Path -LiteralPath $dir)) {
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
}

$examples = Get-ChildItem -LiteralPath $dir -Filter "*.example" -File
if ($examples.Count -eq 0) {
    Write-Host "错误: $dir 下没有 *.example 文件" -ForegroundColor Red
    exit 1
}

foreach ($ex in $examples) {
    $target = Join-Path $dir ($ex.Name -replace '\.example$', '')
    if ((Test-Path -LiteralPath $target) -and -not $Force) {
        Write-Host "跳过（已存在）: $target" -ForegroundColor DarkYellow
        continue
    }
    Copy-Item -LiteralPath $ex.FullName -Destination $target -Force
    Write-Host "已生成: $target" -ForegroundColor Green
}

Write-Host ""
Write-Host "环境: $Env  配置目录: env\$Env" -ForegroundColor Cyan
Write-Host "本地开发: `$env:APP_ENV='$Env'  或  `$env:CONFIG_DIR='env\$Env'" -ForegroundColor Cyan
Write-Host "生产构建: .\scripts\build-linux.ps1 -AppEnv prod" -ForegroundColor Cyan
