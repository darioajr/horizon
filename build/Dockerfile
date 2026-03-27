# =============================================================================
# Multi-platform build (linux/amd64, linux/arm64)
# Optimized for minimal binary size (inspired by Datadog's 77% reduction).
#
# Usage:
#   docker buildx build --platform linux/amd64,linux/arm64 -f build/Dockerfile .
#   docker buildx build --platform linux/amd64,linux/arm64 -t horizon:latest --push .
#
# Override BUILD_TAGS to include only the storage backends you need:
#   --build-arg BUILD_TAGS="s3"             # only S3 backend
#   --build-arg BUILD_TAGS="redis s3"       # Redis + S3
#   --build-arg BUILD_TAGS="all_backends"   # all backends (default)
#   --build-arg BUILD_TAGS=""               # file-only (smallest binary)
# =============================================================================

# Build stage — runs on the HOST architecture for speed
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

# Automatic ARGs injected by BuildKit
ARG TARGETPLATFORM
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG TARGETVARIANT

# Version info (can be overridden at build time)
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# Build tags for optional storage backends
ARG BUILD_TAGS=all_backends

# Install build dependencies (including upx for binary compression)
RUN apk add --no-cache git make upx

WORKDIR /app

# Copy go mod files first for caching
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary for the TARGET platform (cross-compilation via Go env vars)
# -trimpath: removes file paths from binary (smaller + reproducible)
# -s -w: strip symbol table and DWARF debug info
# -tags: include only selected storage backends
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
    -trimpath \
    -tags "${BUILD_TAGS}" \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /app/horizon ./cmd/horizon && \
    upx --best --lzma /app/horizon

# =============================================================================
# Optional: build Darwin binaries (extract with: docker buildx build --target=darwin-artifacts ...)
# =============================================================================
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS darwin-builder

WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG BUILD_TAGS=all_backends

RUN CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 \
    go build -trimpath -tags "${BUILD_TAGS}" -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/horizon-darwin-amd64 ./cmd/horizon && \
    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
    go build -trimpath -tags "${BUILD_TAGS}" -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/horizon-darwin-arm64 ./cmd/horizon

# Stage to extract Darwin binaries (scratch image, no runtime)
FROM scratch AS darwin-artifacts
COPY --from=darwin-builder /out/ /

# =============================================================================
# Runtime stage — scratch image for absolute minimum size.
# The binary is statically linked (CGO_ENABLED=0) so no libc needed.
# =============================================================================
FROM alpine:3.19 AS certs
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 -s /sbin/nologin horizon && \
    mkdir -p /data && chown horizon:horizon /data

FROM scratch

# Import CA certificates, timezone data, user info and data dir from alpine
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=certs /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=certs /etc/passwd /etc/passwd
COPY --from=certs /etc/group /etc/group
COPY --from=certs --chown=10001:10001 /data /data

WORKDIR /app

# Copy binary from builder (already compiled for the correct TARGETARCH)
COPY --from=builder /app/horizon /app/horizon

# Copy default config
COPY configs/config.yaml /app/config.yaml

# Expose Kafka port, HTTP gateway port, and inter-broker RPC/gossip port
EXPOSE 9092 8080 9093

# Volume for data persistence
VOLUME ["/data"]

USER horizon

# Run horizon
ENTRYPOINT ["/app/horizon"]
CMD ["--config", "/app/config.yaml", "--data-dir", "/data"]
