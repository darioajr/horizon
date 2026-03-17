#!/usr/bin/env bash
# Horizon Build Script for Linux/macOS
# Usage: ./scripts/build.sh [target] [version]

set -e

TARGET="${1:-build}"
VERSION="${2:-0.1.0}"

# Root directory (one level up from scripts/)
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

# Configuration
DIST="dist"
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}"

# Detect container runtime (Docker or Podman)
detect_container_runtime() {
    if command -v docker &>/dev/null; then
        CONTAINER_RT="docker"
    elif command -v podman &>/dev/null; then
        CONTAINER_RT="podman"
    else
        echo "Error: neither docker nor podman found in PATH" >&2
        exit 1
    fi

    if command -v docker-compose &>/dev/null; then
        COMPOSE_CMD="docker-compose"
    elif "$CONTAINER_RT" compose version &>/dev/null 2>&1; then
        COMPOSE_CMD="$CONTAINER_RT compose"
    elif command -v podman-compose &>/dev/null; then
        COMPOSE_CMD="podman-compose"
    else
        echo "Error: neither docker-compose, docker compose, nor podman-compose found" >&2
        exit 1
    fi

    echo "Using container runtime: $CONTAINER_RT ($COMPOSE_CMD)"
}

write_header() {
    echo ""
    echo "=== $1 ==="
}

build_current() {
    write_header "Building for current platform"
    CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -o horizon ./cmd/horizon
    echo "Built: horizon"
}

build_linux() {
    write_header "Building for Linux"
    mkdir -p "$DIST"

    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$DIST/horizon-linux-amd64" ./cmd/horizon
    echo "Built: $DIST/horizon-linux-amd64"

    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$LDFLAGS" -o "$DIST/horizon-linux-arm64" ./cmd/horizon
    echo "Built: $DIST/horizon-linux-arm64"
}

build_windows() {
    write_header "Building for Windows"
    mkdir -p "$DIST"

    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$DIST/horizon-windows-amd64.exe" ./cmd/horizon
    echo "Built: $DIST/horizon-windows-amd64.exe"

    CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags "$LDFLAGS" -o "$DIST/horizon-windows-arm64.exe" ./cmd/horizon
    echo "Built: $DIST/horizon-windows-arm64.exe"
}

build_darwin() {
    write_header "Building for macOS"
    mkdir -p "$DIST"

    CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$DIST/horizon-darwin-amd64" ./cmd/horizon
    echo "Built: $DIST/horizon-darwin-amd64"

    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$LDFLAGS" -o "$DIST/horizon-darwin-arm64" ./cmd/horizon
    echo "Built: $DIST/horizon-darwin-arm64"
}

build_all() {
    build_linux
    build_windows
    build_darwin
    write_header "All builds complete!"
    ls -lh "$DIST"
}

docker_build() {
    detect_container_runtime
    write_header "Building all platforms using $CONTAINER_RT"
    $COMPOSE_CMD -f deployments/docker-compose.yml --profile build run --rm builder
}

docker_image() {
    detect_container_runtime
    write_header "Building container image"
    $CONTAINER_RT build -f build/Dockerfile -t "horizon:${VERSION}" -t horizon:latest .
}

docker_run() {
    detect_container_runtime
    write_header "Starting Horizon in container"
    $COMPOSE_CMD -f deployments/docker-compose.yml up -d horizon
    echo "Horizon is running. Access at localhost:9092"
}

run_tests() {
    write_header "Running tests"
    go test -v ./...
}

clean_build() {
    write_header "Cleaning build artifacts"
    rm -rf "$DIST"
    rm -f horizon
    echo "Cleaned!"
}

show_help() {
    cat <<EOF

Horizon Build Script for Linux/macOS

Usage: ./scripts/build.sh [target] [version]

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
  ./scripts/build.sh                           # Build for current platform
  ./scripts/build.sh build-all                 # Build for all platforms
  ./scripts/build.sh docker-build              # Build using Docker
  ./scripts/build.sh build 1.0.0              # Build with specific version

EOF
}

# Main execution
case "$TARGET" in
    build)         build_current ;;
    build-all)     build_all ;;
    build-linux)   build_linux ;;
    build-windows) build_windows ;;
    build-darwin)  build_darwin ;;
    docker)        docker_image ;;
    docker-build)  docker_build ;;
    docker-run)    docker_run ;;
    test)          run_tests ;;
    clean)         clean_build ;;
    help)          show_help ;;
    *)             echo "Unknown target: $TARGET"; show_help; exit 1 ;;
esac
