#!/usr/bin/env bash
# Horizon Build Script for Linux/macOS
# Usage: ./scripts/build.sh [--compress] [target] [version]

set -e

# Parse flags
COMPRESS="${COMPRESS:-0}"
while [[ "$1" == --* ]]; do
    case "$1" in
        --compress) COMPRESS=1; shift ;;
        *) echo "Unknown flag: $1"; exit 1 ;;
    esac
done

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

# Build tags for optional storage backends (e.g. "s3 redis infinispan" or "all_backends")
BUILD_TAGS="${BUILD_TAGS:-all_backends}"
TAG_FLAGS=""
if [ -n "$BUILD_TAGS" ]; then
    TAG_FLAGS="-tags ${BUILD_TAGS}"
fi

# UPX compression (--compress flag or COMPRESS=1 env var)

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

compress_binary() {
    local bin="$1"
    if [ "$COMPRESS" != "1" ]; then return; fi
    if ! command -v upx &>/dev/null; then
        echo "  [skip] UPX not found – install from https://upx.github.io"
        return
    fi
    local before; before=$(stat -c%s "$bin" 2>/dev/null || stat -f%z "$bin")
    upx --best --lzma "$bin" >/dev/null 2>&1
    if [ $? -eq 0 ]; then
        local after; after=$(stat -c%s "$bin" 2>/dev/null || stat -f%z "$bin")
        local pct; pct=$(awk "BEGIN{printf \"%.1f\", (1-${after}/${before})*100}")
        echo "  UPX: $(awk "BEGIN{printf \"%.1f\", ${before}/1048576}")MB -> $(awk "BEGIN{printf \"%.1f\", ${after}/1048576}")MB (-${pct}%)"
    else
        echo "  [warn] UPX compression failed for $bin"
    fi
}

build_current() {
    write_header "Building for current platform"
    CGO_ENABLED=0 go build -trimpath $TAG_FLAGS -ldflags "$LDFLAGS" -o horizon ./cmd/horizon
    echo "Built: horizon"
    compress_binary horizon
}

build_linux() {
    write_header "Building for Linux"
    mkdir -p "$DIST"

    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath $TAG_FLAGS -ldflags "$LDFLAGS" -o "$DIST/horizon-linux-amd64" ./cmd/horizon
    echo "Built: $DIST/horizon-linux-amd64"
    compress_binary "$DIST/horizon-linux-amd64"

    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath $TAG_FLAGS -ldflags "$LDFLAGS" -o "$DIST/horizon-linux-arm64" ./cmd/horizon
    echo "Built: $DIST/horizon-linux-arm64"
    compress_binary "$DIST/horizon-linux-arm64"
}

build_windows() {
    write_header "Building for Windows"
    mkdir -p "$DIST"

    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath $TAG_FLAGS -ldflags "$LDFLAGS" -o "$DIST/horizon-windows-amd64.exe" ./cmd/horizon
    echo "Built: $DIST/horizon-windows-amd64.exe"
    compress_binary "$DIST/horizon-windows-amd64.exe"

    CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath $TAG_FLAGS -ldflags "$LDFLAGS" -o "$DIST/horizon-windows-arm64.exe" ./cmd/horizon
    echo "Built: $DIST/horizon-windows-arm64.exe"
    compress_binary "$DIST/horizon-windows-arm64.exe"
}

build_darwin() {
    write_header "Building for macOS"
    mkdir -p "$DIST"

    CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath $TAG_FLAGS -ldflags "$LDFLAGS" -o "$DIST/horizon-darwin-amd64" ./cmd/horizon
    echo "Built: $DIST/horizon-darwin-amd64"
    compress_binary "$DIST/horizon-darwin-amd64"

    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath $TAG_FLAGS -ldflags "$LDFLAGS" -o "$DIST/horizon-darwin-arm64" ./cmd/horizon
    echo "Built: $DIST/horizon-darwin-arm64"
    compress_binary "$DIST/horizon-darwin-arm64"
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

Usage: ./scripts/build.sh [--compress] [target] [version]

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
  ./scripts/build.sh --compress                # Build + UPX compression
  COMPRESS=1 ./scripts/build.sh                # Same via env var
  BUILD_TAGS="s3" ./scripts/build.sh            # Only include S3 backend
  BUILD_TAGS="" ./scripts/build.sh              # File-only (smallest binary)

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
