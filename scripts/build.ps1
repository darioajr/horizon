# Horizon Build Script for Windows
# Usage: .\scripts\build.ps1 [-Target <target>] [-Version <version>]

param(
    [ValidateSet("build", "build-all", "build-linux", "build-windows", "build-darwin", "docker", "docker-build", "docker-run", "test", "clean", "help")]
    [string]$Target = "build",
    [string]$Version = "0.1.0"
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

function Build-Current {
    Write-Header "Building for current platform"
    $env:CGO_ENABLED = "0"
    go build -ldflags $LDFLAGS -o horizon.exe ./cmd/horizon
    if ($LASTEXITCODE -eq 0) {
        Write-Host "Built: horizon.exe" -ForegroundColor Green
    }
}

function Build-Linux {
    Write-Header "Building for Linux"
    New-Item -ItemType Directory -Force -Path $DIST | Out-Null
    
    $env:CGO_ENABLED = "0"
    $env:GOOS = "linux"
    
    $env:GOARCH = "amd64"
    go build -ldflags $LDFLAGS -o "$DIST/horizon-linux-amd64" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-linux-amd64" -ForegroundColor Green
    
    $env:GOARCH = "arm64"
    go build -ldflags $LDFLAGS -o "$DIST/horizon-linux-arm64" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-linux-arm64" -ForegroundColor Green
    
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

function Build-Windows {
    Write-Header "Building for Windows"
    New-Item -ItemType Directory -Force -Path $DIST | Out-Null
    
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    
    $env:GOARCH = "amd64"
    go build -ldflags $LDFLAGS -o "$DIST/horizon-windows-amd64.exe" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-windows-amd64.exe" -ForegroundColor Green
    
    $env:GOARCH = "arm64"
    go build -ldflags $LDFLAGS -o "$DIST/horizon-windows-arm64.exe" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-windows-arm64.exe" -ForegroundColor Green
    
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

function Build-Darwin {
    Write-Header "Building for macOS"
    New-Item -ItemType Directory -Force -Path $DIST | Out-Null
    
    $env:CGO_ENABLED = "0"
    $env:GOOS = "darwin"
    
    $env:GOARCH = "amd64"
    go build -ldflags $LDFLAGS -o "$DIST/horizon-darwin-amd64" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-darwin-amd64" -ForegroundColor Green
    
    $env:GOARCH = "arm64"
    go build -ldflags $LDFLAGS -o "$DIST/horizon-darwin-arm64" ./cmd/horizon
    Write-Host "Built: $DIST/horizon-darwin-arm64" -ForegroundColor Green
    
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
    Write-Host @"

Horizon Build Script for Windows

Usage: .\scripts\build.ps1 [-Target <target>] [-Version <version>]

Targets:
  build          Build for current platform (default)
  build-all      Build for all platforms (Linux, Windows, macOS)
  build-linux    Build for Linux (amd64, arm64)
  build-windows  Build for Windows (amd64, arm64)
  build-darwin   Build for macOS (amd64, arm64)
  docker         Build Docker image
  docker-build   Build all platforms using Docker (no Go required)
  docker-run     Run Horizon in Docker
  test           Run tests
  clean          Clean build artifacts
  help           Show this help

Examples:
  .\scripts\build.ps1                           # Build for current platform
  .\scripts\build.ps1 -Target build-all         # Build for all platforms
  .\scripts\build.ps1 -Target docker-build      # Build using Docker
  .\scripts\build.ps1 -Target build -Version 1.0.0

"@ -ForegroundColor White
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
