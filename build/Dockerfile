# =============================================================================
# Multi-platform build (linux/amd64, linux/arm64)
# Usage:
#   docker buildx build --platform linux/amd64,linux/arm64 -f build/Dockerfile .
#   docker buildx build --platform linux/amd64,linux/arm64 -t horizon:latest --push .
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

# Install build dependencies
RUN apk add --no-cache git make

WORKDIR /app

# Copy go mod files first for caching
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary for the TARGET platform (cross-compilation via Go env vars)
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /app/horizon ./cmd/horizon

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

RUN CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 \
    go build -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/horizon-darwin-amd64 ./cmd/horizon && \
    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
    go build -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/horizon-darwin-arm64 ./cmd/horizon

# Stage to extract Darwin binaries (scratch image, no runtime)
FROM scratch AS darwin-artifacts
COPY --from=darwin-builder /out/ /

# =============================================================================
# Runtime stage — minimal image, matches the TARGET platform automatically
# =============================================================================
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -s /bin/sh horizon

WORKDIR /app

# Copy binary from builder (already compiled for the correct TARGETARCH)
COPY --from=builder /app/horizon /app/horizon

# Create data directory
RUN mkdir -p /data && chown horizon:horizon /data

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
