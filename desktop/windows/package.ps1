# Builds the Windows desktop runner as one executable and, unless skipped, its
# Linux (WSLC) job image.
#
#   pwsh -File desktop/windows/package.ps1
#   pwsh -File desktop/windows/package.ps1 -SkipImage
#
# The app needs the Windows Go 1.27+ and Rust toolchains. The Go runner is
# compiled by the app's build script and embedded, so the result is a single
# `Forgelet Runner.exe` with nothing to install beside it. The job image needs
# wslc.exe (WSL 2.9.3 or newer) and is built with `wslc build`.
[CmdletBinding()]
param(
    [string]$OutputDir,
    [string]$ImageTag = 'forgelet-runner-wslc:dev',
    [switch]$SkipImage,
    [switch]$SkipRust
)

$ErrorActionPreference = 'Stop'
if (-not $IsWindows) {
    throw 'The Windows runner bundle must be built on Windows.'
}
$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
Push-Location $repo
try {
    if (-not $OutputDir) {
        $OutputDir = Join-Path $repo 'dist'
    }
    New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

    if (-not $SkipRust) {
        Write-Host '==> 构建单文件 Windows 应用'
        $manifest = Join-Path $PSScriptRoot 'Cargo.toml'
        $target = Join-Path $PSScriptRoot 'target'
        $locked = @()
        if (Test-Path (Join-Path $PSScriptRoot 'Cargo.lock')) {
            $locked = @('--locked')
        }
        cargo build --manifest-path $manifest --target-dir $target --release @locked
        if ($LASTEXITCODE -ne 0) {
            throw '构建 Windows 桌面应用失败'
        }
        $app = Join-Path $OutputDir 'Forgelet Runner.exe'
        Copy-Item (Join-Path $target 'release\forgelet-desktop.exe') $app -Force
        $hash = (Get-FileHash -Algorithm SHA256 -Path $app).Hash.ToLowerInvariant()
        "{0}  Forgelet Runner.exe" -f $hash | Set-Content -Path (Join-Path $OutputDir 'SHA256SUMS.txt') -Encoding ascii
        Write-Host "==> $app ($([math]::Round((Get-Item $app).Length / 1MB, 1)) MiB)"
    }

    if (-not $SkipImage) {
        $wslc = Join-Path $env:ProgramFiles 'WSL\wslc.exe'
        if (-not (Test-Path $wslc)) {
            $wslc = (Get-Command wslc.exe -ErrorAction SilentlyContinue).Source
        }
        if (-not $wslc) {
            throw '找不到 wslc.exe；请安装或更新 WSL（要求 2.9.3 或更高版本）。'
        }
        Write-Host '==> 构建 Linux 容器执行器'
        $env:CGO_ENABLED = '0'
        $env:GOOS = 'linux'
        $env:GOARCH = 'amd64'
        $imageDir = Join-Path $PSScriptRoot 'image'
        go build -trimpath -o (Join-Path $imageDir 'forgelet-runner') ./cmd/runner
        if ($LASTEXITCODE -ne 0) {
            throw '构建 Linux 容器执行器失败'
        }
        Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
        Write-Host "==> wslc build $ImageTag"
        & $wslc build -t $ImageTag -f (Join-Path $imageDir 'Dockerfile') $imageDir
        if ($LASTEXITCODE -ne 0) {
            throw 'wslc build 失败'
        }
        Write-Host "执行镜像: $ImageTag（推送并固定 digest 后填进应用）"
    }

    Write-Host ''
    Write-Host "产物目录: $OutputDir"
    Get-ChildItem $OutputDir | Select-Object Name, Length | Format-Table -AutoSize
}
finally {
    Pop-Location
}
