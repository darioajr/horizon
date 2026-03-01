# Build stage
FROM golang:1.22-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

WORKDIR /app

# Copy go mod files first for caching
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY . .

# Build for Linux
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o horizon-linux-amd64 ./cmd/horizon

# Build for Windows
RUN CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o horizon-windows-amd64.exe ./cmd/horizon

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/horizon-linux-amd64 /app/horizon

# Create data directory
RUN mkdir -p /data

# Copy default config
COPY configs/config.yaml /app/config.yaml

# Expose Kafka port, HTTP gateway port, and inter-broker RPC/gossip port
EXPOSE 9092 8080 9093

# Volume for data persistence
VOLUME ["/data"]

# Run horizon
ENTRYPOINT ["/app/horizon"]
CMD ["--config", "/app/config.yaml", "--data-dir", "/data"]
