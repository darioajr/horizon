# Horizon Build Script for Windows
# Usage: .\scripts\build.ps1 [-Target <target>] [-Version <version>]

param(
    [ValidateSet("build", "build-all", "build-linux", "build-windows", "build-darwin", "docker", "docker-build", "docker-run", "test", "clean", "help")]
    [string]$Target = "build",
    [string]$Version = "0.1.0",
    [switch]$Compress
)

$ErrorActionPreference = "Stop"

# Root directory (one level up from scripts/)
$RootDir = Split-Path -Parent $PSScriptRoot
if (-not $RootDir) { $RootDir = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path) }
Push-Location $RootDir

# Configuration
$DIST = "dist"
$COMMIT = try { git rev-parse --short HEAD 2>$null } catch { "none" }
$BUILD_DATE = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$LDFLAGS = "-s -w -X main.version=$Version -X main.commit=$COMMIT -X main.buildDate=$BUILD_DATE"

# Build tags for optional storage backends (e.g. "s3 redis infinispan" or "all_backends")
if (-not $env:BUILD_TAGS) { $env:BUILD_TAGS = "all_backends" }
$TAG_FLAGS = if ($env:BUILD_TAGS) { "-tags", $env:BUILD_TAGS } else { @() }

# Detect container runtime (Docker or Podman)
$script:ContainerRT = $null
$script:ComposeCmd = $null

function Detect-ContainerRuntime {
    if (Get-Command docker -ErrorAction SilentlyContinue) {
        $script:ContainerRT = "docker"
    } elseif (Get-Command podman -ErrorAction SilentlyContinue) {
        $script:ContainerRT = "podman"
    } else {
        Write-Host "Error: neither docker nor podman found in PATH" -ForegroundColor Red
        exit 1
    }

    if (Get-Command docker-compose -ErrorAction SilentlyContinue) {
        $script:ComposeCmd = "docker-compose"
    } elseif (& $script:ContainerRT compose version 2>$null) {
        $script:ComposeCmd = "$($script:ContainerRT) compose"
    } elseif (Get-Command podman-compose -ErrorAction SilentlyContinue) {
        $script:ComposeCmd = "podman-compose"
    } else {
        Write-Host "Error: neither docker-compose, docker compose, nor podman-compose found" -ForegroundColor Red
        exit 1
    }

    Write-Host "Using container runtime: $script:ContainerRT ($script:ComposeCmd)" -ForegroundColor Yellow
}

function Write-Header {
    param([string]$Message)
    Write-Host "`n=== $Message ===" -ForegroundColor Cyan
}

function Compress-Binary {
    param([string]$Path)
    if (-not $Compress) { return }
    if (-not (Get-Command upx -ErrorAction SilentlyContinue)) {
        Write-Host '  [skip] UPX not found - install from https://upx.github.io' -ForegroundColor Yellow
        return
    }
    $before = (Get-Item $Path).Length
    upx --best --lzma $Path 2>&1 | Out-Null
    if ($LASTEXITCODE -eq 0) {
        $after = (Get-Item $Path).Length
        $pct = [math]::Round((1 - $after / $before) * 100, 1)
        $beforeMB = [math]::Round($before / 1MB, 1)
        $afterMB = [math]::Round($after / 1MB, 1)
        Write-Host ('  UPX: {0}MB -> {1}MB (-{2}%)' -f $beforeMB, $afterMB, $pct) -ForegroundColor Magenta
    } else {
        Write-Host ('  [warn] UPX compression failed for ' + $Path) -ForegroundColor Yellow
    }
}

function Build-Current {
    Write-Header "Building for current platform"
    $env:CGO_ENABLED = "0"
    go build -trimpath @TAG_FLAGS -ldflags $LDFLAGS -o horizon.exe ./cmd/horizon
    if ($LASTEXITCODE -eq 0) {
        Write-Host "Built: horizon.exe" -ForegroundColor Green
        Compress-Binary "horizon.exe"
    }
}

function Build-Linux {
    Write-Header "Building for Linux"
    New-Item -ItemType Directory -Force -Path $DIST | Out-Null
    
    $env:CGO_ENABLED = "0"
    $env:GOOS = "linux"
    
    $env:GOARCH = "amd64"
    go build -trimpath @TAG_FLAGS -ldflags $LDFLAGS -o "$DIST/horizon-linux-amd64" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-linux-amd64" -ForegroundColor Green
    Compress-Binary "$DIST/horizon-linux-amd64"
    
    $env:GOARCH = "arm64"
    go build -trimpath @TAG_FLAGS -ldflags $LDFLAGS -o "$DIST/horizon-linux-arm64" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-linux-arm64" -ForegroundColor Green
    Compress-Binary "$DIST/horizon-linux-arm64"
    
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

function Build-Windows {
    Write-Header "Building for Windows"
    New-Item -ItemType Directory -Force -Path $DIST | Out-Null
    
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    
    $env:GOARCH = "amd64"
    go build -trimpath @TAG_FLAGS -ldflags $LDFLAGS -o "$DIST/horizon-windows-amd64.exe" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-windows-amd64.exe" -ForegroundColor Green
    Compress-Binary "$DIST/horizon-windows-amd64.exe"
    
    $env:GOARCH = "arm64"
    go build -trimpath @TAG_FLAGS -ldflags $LDFLAGS -o "$DIST/horizon-windows-arm64.exe" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-windows-arm64.exe" -ForegroundColor Green
    Compress-Binary "$DIST/horizon-windows-arm64.exe"
    
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

function Build-Darwin {
    Write-Header "Building for macOS"
    New-Item -ItemType Directory -Force -Path $DIST | Out-Null
    
    $env:CGO_ENABLED = "0"
    $env:GOOS = "darwin"
    
    $env:GOARCH = "amd64"
    go build -trimpath @TAG_FLAGS -ldflags $LDFLAGS -o "$DIST/horizon-darwin-amd64" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-darwin-amd64" -ForegroundColor Green
    Compress-Binary "$DIST/horizon-darwin-amd64"
    
    $env:GOARCH = "arm64"
    go build -trimpath @TAG_FLAGS -ldflags $LDFLAGS -o "$DIST/horizon-darwin-arm64" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-darwin-arm64" -ForegroundColor Green
    Compress-Binary "$DIST/horizon-darwin-arm64"
    
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

function Build-All {
    Build-Linux
    Build-Windows
    Build-Darwin
    Write-Header "All builds complete!"
    Get-ChildItem $DIST | Format-Table Name, Length
}

function Docker-Build {
    Detect-ContainerRuntime
    Write-Header "Building all platforms using $script:ContainerRT"
    & $script:ComposeCmd -f deployments/docker-compose.yml --profile build run --rm builder
}

function Docker-Image {
    Detect-ContainerRuntime
    Write-Header "Building container image"
    & $script:ContainerRT build -f build/Dockerfile -t "horizon:$Version" -t horizon:latest .
}

function Docker-Run {
    Detect-ContainerRuntime
    Write-Header "Starting Horizon in container"
    & $script:ComposeCmd -f deployments/docker-compose.yml up -d horizon
    Write-Host "Horizon is running. Access at localhost:9092" -ForegroundColor Green
}

function Run-Tests {
    Write-Header "Running tests"
    go test -v ./...
}

function Clean-Build {
    Write-Header "Cleaning build artifacts"
    if (Test-Path $DIST) {
        Remove-Item -Recurse -Force $DIST
    }
    if (Test-Path "horizon.exe") {
        Remove-Item -Force "horizon.exe"
    }
    Write-Host "Cleaned!" -ForegroundColor Green
}

function Show-Help {
    Write-Host ''
    Write-Host 'Horizon Build Script for Windows' -ForegroundColor White
    Write-Host ''
    Write-Host 'Usage: .\scripts\build.ps1 [-Target target] [-Version version]' -ForegroundColor White
    Write-Host ''
    Write-Host 'Targets:' -ForegroundColor White
    Write-Host '  build          Build for current platform (default)' -ForegroundColor White
    Write-Host '  build-all      Build for all platforms' -ForegroundColor White
    Write-Host '  build-linux    Build for Linux' -ForegroundColor White
    Write-Host '  build-windows  Build for Windows' -ForegroundColor White
    Write-Host '  build-darwin   Build for macOS' -ForegroundColor White
    Write-Host '  docker         Build Docker image' -ForegroundColor White
    Write-Host '  docker-build   Build all platforms using Docker' -ForegroundColor White
    Write-Host '  docker-run     Run Horizon in Docker' -ForegroundColor White
    Write-Host '  test           Run tests' -ForegroundColor White
    Write-Host '  clean          Clean build artifacts' -ForegroundColor White
    Write-Host '  help           Show this help' -ForegroundColor White
    Write-Host ''
    Write-Host 'Options:' -ForegroundColor White
    Write-Host '  -Compress      Compress binary with UPX after build' -ForegroundColor White
    Write-Host ''
    Write-Host 'Environment:' -ForegroundColor White
    Write-Host '  BUILD_TAGS     Backends to include (default: all_backends)' -ForegroundColor White
    Write-Host '                 Values: s3, redis, infinispan, all_backends, or empty' -ForegroundColor White
    Write-Host ''
}

# Main execution
switch ($Target) {
    "build"         { Build-Current }
    "build-all"     { Build-All }
    "build-linux"   { Build-Linux }
    "build-windows" { Build-Windows }
    "build-darwin"  { Build-Darwin }
    "docker"        { Docker-Image }
    "docker-build"  { Docker-Build }
    "docker-run"    { Docker-Run }
    "test"          { Run-Tests }
    "clean"         { Clean-Build }
    "help"          { Show-Help }
    default         { Build-Current }
}

# Restore original directory
Pop-Location
