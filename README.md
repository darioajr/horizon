# Horizon - Kafka-Compatible Event Streaming Platform

<!-- Badges -->
[![CI](https://github.com/darioajr/horizon/actions/workflows/ci.yml/badge.svg)](https://github.com/darioajr/horizon/actions/workflows/ci.yml)
[![Release](https://github.com/darioajr/horizon/actions/workflows/release.yml/badge.svg)](https://github.com/darioajr/horizon/actions/workflows/release.yml)
[![GitHub Release](https://img.shields.io/github/v/release/darioajr/horizon?style=flat&logo=github)](https://github.com/darioajr/horizon/releases/latest)
[![Docker Hub](https://img.shields.io/docker/v/darioajr/horizon?sort=semver&logo=docker&label=Docker%20Hub)](https://hub.docker.com/r/darioajr/horizon)
[![Docker Pulls](https://img.shields.io/docker/pulls/darioajr/horizon?logo=docker)](https://hub.docker.com/r/darioajr/horizon)
[![Docker Image Size](https://img.shields.io/docker/image-size/darioajr/horizon?sort=semver&logo=docker)](https://hub.docker.com/r/darioajr/horizon)
[![Go Version](https://img.shields.io/github/go-mod/go-version/darioajr/horizon?logo=go)](go.mod)
[![License](https://img.shields.io/github/license/darioajr/horizon)](LICENSE)
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fdarioajr%2Fhorizon.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Fdarioajr%2Fhorizon?ref=badge_shield) 

A high-performance, Kafka protocol-compatible event streaming platform implemented in Go. Inspired by WarpStream and EventHorizon.

## Features

- **Kafka Protocol Compatible** - Works with existing Kafka clients (librdkafka, kafka-go, Sarama, etc.)
- **HTTP/HTTPS Gateway** - Optional REST API for producing messages, managing topics, and health checks — no Kafka client needed
- **Persistent Storage** - Durable append-only log segments with configurable retention
- **Pluggable Storage Backends** - File (default), S3/MinIO, Redis Streams, Infinispan
- **Topic Partitioning** - Horizontal scaling through configurable partitions
- **Consumer Groups** - Full Kafka-compatible coordinator with blocking JoinGroup/SyncGroup, session timeouts, and automatic rebalancing
- **High Performance** - Optimized for throughput and low latency with zero-copy I/O, buffer pooling, and TCP_NODELAY
- **Cross-Platform** - Runs on Windows, Linux, and macOS (amd64 and arm64)
- **Single Binary** - No external dependencies for the file backend

## Documentation

Detailed architecture and design documents are available in the [docs/](docs/) folder:

| Document | Description |
|----------|-------------|
| [Architecture Overview](docs/architecture-overview.md) | High-level system design and component interactions |
| [Kafka Protocol](docs/protocol.md) | Wire protocol implementation and supported API versions |
| [HTTP Gateway](docs/http-gateway.md) | REST API for producing, topic admin, and health checks |
| [Consumer Groups](docs/consumer-groups.md) | Group coordinator, rebalance protocol, and state machine |
| [Storage Backends](docs/storage-backends.md) | StorageEngine interface and backend implementations |
| [Configuration](docs/configuration.md) | Complete configuration reference |
| [Testing: Horizon vs Kafka](docs/testing-horizon-vs-kafka.md) | Side-by-side benchmark with Kafka KRaft using Docker |

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        Horizon Broker                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌───────────┐   ┌──────────────┐   ┌────────────────────────┐  │
│  │  TCP      │   │   Protocol   │   │   Consumer Group       │  │
│  │  Server   │──▶│   Handler    │──▶│   Coordinator          │  │
│  │           │   │   (dispatch) │   │   (state machine)      │  │
│  └───────────┘   └──────┬───────┘   └────────────────────────┘  │
│       │                 │                                        ││  ┌───────────┐                                                   │
│  │  HTTP     │   REST API: POST /topics/{topic}                  │
│  │  Gateway  │─────┬───────────── Admin: PUT/DELETE/PATCH/PURGE   │
│  └───────────┘     │                                        ││       │   ┌─────────────▼──────────────────────────────┐        │
│       │   │            Broker Core                      │        │
│       │   │  ┌──────────┐ ┌──────────┐ ┌──────────┐    │        │
│       │   │  │ Topic A  │ │ Topic B  │ │ Topic C  │ …  │        │
│       │   │  │ P0 P1 P2 │ │ P0 P1   │ │ P0       │    │        │
│       │   │  └──────────┘ └──────────┘ └──────────┘    │        │
│       │   └─────────────┬──────────────────────────┘    │        │
│       │                 ▼                                │        │
│  ┌──────────────────────────────────────────────────────┐       │
│  │                  Storage Engine                       │       │
│  │              (StorageEngine interface)                 │       │
│  │  ┌────────┐  ┌────────┐  ┌────────┐  ┌────────────┐  │       │
│  │  │  File  │  │   S3   │  │ Redis  │  │ Infinispan │  │       │
│  │  │(default│  │ /MinIO │  │Streams │  │  REST API  │  │       │
│  │  └────────┘  └────────┘  └────────┘  └────────────┘  │       │
│  └──────────────────────────────────────────────────────┘       │
└─────────────────────────────────────────────────────────────────┘

              ┌─────── Cluster Layer (optional) ───────┐
              │  Gossip (UDP)  │  RPC (TCP)  │ Ctrl    │
              │  SWIM-lite     │  Replicate  │ Elect   │
              │  membership    │  Forward    │ Assign  │
              └────────────────┴─────────────┴─────────┘
```

## Quick Start

### Requirements

- **Go 1.22+** (for local builds)
- **Docker** and **Docker Compose** (optional, for containerized builds)
- **Make** (Linux/macOS) or **PowerShell** (Windows)

### Build & Run

```powershell
# Windows – build Docker image
.\scripts\build.ps1 -Target docker

# Windows – build local binary
.\scripts\build.ps1

# Linux/macOS
make build
```

```bash
# Run with default config
./horizon

# Run with custom config
./horizon -config configs/config.yaml

# Run with Docker
docker run -d -p 9092:9092 -p 8080:8080 -p 9093:9093 -v horizon-data:/data --name horizon horizon:latest
```

### Produce via HTTP (no Kafka client needed)

```bash
# Simple JSON message
curl -X POST "http://localhost:8080/topics/orders?type=json&key=order-1" \
  -d '{"orderId": 1, "amount": 99.90}'

# Create a topic with 10 partitions
curl -X PUT "http://localhost:8080/admin/topics/events?partitions=10"

# Delete a topic
curl -X DELETE "http://localhost:8080/admin/topics/old-events"

# Purge topic data (keep topic config)
curl -X POST "http://localhost:8080/admin/topics/events/purge"
```

### Test with any Kafka client

```go
// Producer (using kafka-go)
writer := kafka.NewWriter(kafka.WriterConfig{
    Brokers: []string{"localhost:9092"},
    Topic:   "my-topic",
})
writer.WriteMessages(context.Background(),
    kafka.Message{Value: []byte("Hello, Horizon!")},
)

// Consumer with group
reader := kafka.NewReader(kafka.ReaderConfig{
    Brokers: []string{"localhost:9092"},
    Topic:   "my-topic",
    GroupID: "my-group",
})
msg, _ := reader.ReadMessage(context.Background())
fmt.Println(string(msg.Value))
```

## Docker

### Quick Run

```bash
docker pull darioajr/horizon
docker run -d \
  --name horizon \
  -p 9092:9092 \
  -p 8080:8080 \
  -v horizon-data:/data \
  darioajr/horizon
```

Horizon is now available on `localhost:9092` (Kafka protocol) and `localhost:8080` (HTTP gateway).

### Supported Platforms

| Platform | Architecture | Tag Example |
|----------|-------------|-------------|
| Linux    | amd64       | `darioajr/horizon:latest` |
| Linux    | arm64       | `darioajr/horizon:latest` |

Docker automatically pulls the correct image for your architecture.

### Ports

| Port | Protocol | Description |
|------|----------|-------------|
| 9092 | TCP      | Kafka binary protocol (clients) |
| 8080 | TCP      | HTTP/HTTPS gateway (REST API) |
| 9093 | TCP/UDP  | Inter-broker RPC & gossip (cluster mode) |

### Volumes

| Path | Description |
|------|-------------|
| `/data` | Persistent storage for log segments and topic data |
| `/app/config.yaml` | Configuration file (mount to override defaults) |

### Custom Configuration

Mount your own `config.yaml` to override any default:

```bash
docker run -d \
  --name horizon \
  -p 9092:9092 \
  -p 8080:8080 \
  -v horizon-data:/data \
  -v ./my-config.yaml:/app/config.yaml \
  darioajr/horizon
```

### Command-Line Overrides

The entrypoint accepts CLI flags that override config file values:

```bash
docker run -d \
  --name horizon \
  -p 9092:9092 \
  -p 8080:8080 \
  -v horizon-data:/data \
  darioajr/horizon \
    --config /app/config.yaml \
    --data-dir /data \
    --port 9092 \
    --host 0.0.0.0 \
    --broker-id 1
```

### Environment Variables

Environment variables provide the easiest way to configure Horizon in containers. They take
precedence over config file values but are overridden by CLI flags:

| Variable | Default | Description |
|----------|---------|-------------|
| `HORIZON_ADVERTISED_HOST` | `localhost` | Hostname/IP advertised to clients in metadata responses. **Required** when running in Docker with custom networks |
| `HORIZON_HOST` | `0.0.0.0` | Listen address |
| `HORIZON_PORT` | `9092` | Listen port |
| `HORIZON_BROKER_ID` | `1` | Broker ID |
| `HORIZON_DATA_DIR` | `./data` | Data directory path |

Example — Docker Compose on a custom network:

```yaml
services:
  horizon:
    image: darioajr/horizon
    environment:
      HORIZON_ADVERTISED_HOST: horizon   # container name
    networks:
      - app-net
```

Example — standalone Docker with host networking:

```bash
docker run -d --name horizon \
  -p 9092:9092 \
  -e HORIZON_ADVERTISED_HOST=my-server.example.com \
  darioajr/horizon
```

### Storage Backends via Docker

#### File (default — no extra config needed)

```bash
docker run -d --name horizon \
  -p 9092:9092 -v horizon-data:/data \
  darioajr/horizon
```

#### S3 / MinIO

```yaml
# s3-config.yaml
broker:
  id: 1
  host: "0.0.0.0"
  port: 9092
storage:
  backend: "s3"
  s3:
    bucket: "horizon-data"
    region: "us-east-1"
    endpoint: "http://minio:9000"   # omit for AWS S3
    access_key: "minioadmin"
    secret_key: "minioadmin"
```

```bash
docker run -d --name horizon \
  -p 9092:9092 \
  -v ./s3-config.yaml:/app/config.yaml \
  darioajr/horizon
```

#### Redis Streams

```yaml
# redis-config.yaml
broker:
  id: 1
  host: "0.0.0.0"
  port: 9092
storage:
  backend: "redis"
  redis:
    addr: "redis:6379"
    password: ""
    db: 0
    key_prefix: "horizon"
```

```bash
docker run -d --name horizon \
  -p 9092:9092 \
  -v ./redis-config.yaml:/app/config.yaml \
  --link my-redis:redis \
  darioajr/horizon
```

#### Infinispan

```yaml
# infinispan-config.yaml
broker:
  id: 1
  host: "0.0.0.0"
  port: 9092
storage:
  backend: "infinispan"
  infinispan:
    url: "http://infinispan:11222"
    cache_name: "horizon"
    username: "admin"
    password: "secret"
```

```bash
docker run -d --name horizon \
  -p 9092:9092 \
  -v ./infinispan-config.yaml:/app/config.yaml \
  darioajr/horizon
```

### HTTP Gateway

Enable the REST API for producing messages without a Kafka client:

```yaml
# http-config.yaml
broker:
  id: 1
  host: "0.0.0.0"
  port: 9092
http:
  enabled: true
  host: "0.0.0.0"
  port: 8080
  # tls_cert_file: "/certs/cert.pem"
  # tls_key_file:  "/certs/key.pem"
```

```bash
docker run -d --name horizon \
  -p 9092:9092 -p 8080:8080 \
  -v ./http-config.yaml:/app/config.yaml \
  darioajr/horizon

# Test
curl -X POST "http://localhost:8080/topics/test?type=json" \
  -d '{"hello": "world"}'
```

### Performance Tuning

```yaml
# performance-config.yaml
broker:
  id: 1
  host: "0.0.0.0"
  port: 9092
storage:
  backend: "file"
  data_dir: "/data"
  segment_size_mb: 1024       # 1 GB per segment
  retention_hours: 168         # 7 days
  sync_writes: false           # true = fsync every write (slower, safer)
  flush_interval: "1s"
defaults:
  num_partitions: 6            # more partitions = more parallelism
  replication_factor: 1
performance:
  write_buffer_kb: 2048        # 2 MB write buffer
  max_connections: 10000
  io_threads: 4
  read_buffer_size: 65536      # 64 KB per connection
```

### Docker Compose Examples

#### Single Node

```yaml
version: "3.8"
services:
  horizon:
    image: darioajr/horizon:latest
    ports:
      - "9092:9092"
      - "8080:8080"
    volumes:
      - horizon-data:/data
      - ./configs/config.yaml:/app/config.yaml
    restart: unless-stopped

volumes:
  horizon-data:
```

#### 3-Node Cluster

```yaml
version: "3.8"
services:
  horizon-1:
    image: darioajr/horizon:latest
    ports: ["9092:9092", "8080:8080"]
    volumes:
      - ./configs/cluster-node1.yaml:/app/config.yaml
      - horizon-data-1:/data

  horizon-2:
    image: darioajr/horizon:latest
    ports: ["9192:9092", "8180:8080"]
    volumes:
      - ./configs/cluster-node2.yaml:/app/config.yaml
      - horizon-data-2:/data

  horizon-3:
    image: darioajr/horizon:latest
    ports: ["9292:9092", "8280:8080"]
    volumes:
      - ./configs/cluster-node3.yaml:/app/config.yaml
      - horizon-data-3:/data

volumes:
  horizon-data-1:
  horizon-data-2:
  horizon-data-3:
```

#### With MinIO (S3 Backend)

```yaml
version: "3.8"
services:
  minio:
    image: minio/minio
    command: server /data --console-address ":9001"
    ports: ["9000:9000", "9001:9001"]
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    volumes:
      - minio-data:/data

  horizon:
    image: darioajr/horizon:latest
    ports: ["9092:9092", "8080:8080"]
    volumes:
      - ./configs/s3-config.yaml:/app/config.yaml
    depends_on: [minio]

volumes:
  minio-data:
```

#### With Redis

```yaml
version: "3.8"
services:
  redis:
    image: redis:7-alpine
    ports: ["6379:6379"]
    volumes:
      - redis-data:/data

  horizon:
    image: darioajr/horizon:latest
    ports: ["9092:9092", "8080:8080"]
    volumes:
      - ./configs/redis-config.yaml:/app/config.yaml
    depends_on: [redis]

volumes:
  redis-data:
```

### Health Check

```bash
# HTTP gateway health endpoint
curl http://localhost:8080/health
```

Add to Docker Compose:

```yaml
services:
  horizon:
    image: darioajr/horizon:latest
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/health"]
      interval: 10s
      timeout: 5s
      retries: 3
```

### Version Pinning

```bash
# Latest stable
docker pull darioajr/horizon:latest

# Specific version
docker pull darioajr/horizon:0.1.0

# Major.minor (auto-patches)
docker pull darioajr/horizon:0.1
```

## Supported Kafka APIs

| API | Key | Versions | Status |
|-----|-----|----------|--------|
| ApiVersions | 18 | 0-2 | ✅ Stable |
| Metadata | 3 | 0-8 | ✅ Stable |
| Produce | 0 | 0-8 | ✅ Stable |
| Fetch | 1 | 0-11 | ✅ Stable |
| ListOffsets | 2 | 0-5 | ✅ Stable |
| FindCoordinator | 10 | 0-3 | ✅ Stable |
| JoinGroup | 11 | 0-6 | ✅ Stable |
| SyncGroup | 14 | 0-4 | ✅ Stable |
| Heartbeat | 12 | 0-3 | ✅ Stable |
| LeaveGroup | 13 | 0-4 | ✅ Stable |
| OffsetCommit | 8 | 0-7 | ✅ Stable |
| OffsetFetch | 9 | 0-7 | ✅ Stable |
| DescribeGroups | 15 | 0-4 | ✅ Stable |
| ListGroups | 16 | 0-3 | ✅ Stable |
| DeleteGroups | 42 | 0-1 | ✅ Stable |
| CreateTopics | 19 | 0-4 | ✅ Stable |
| DeleteTopics | 20 | 0-4 | ✅ Stable |
| InitProducerId | 22 | 0-2 | ✅ Stable |

## Configuration

Minimal `configs/config.yaml`:

```yaml
broker:
  id: 1
  host: "0.0.0.0"
  port: 9092

http:
  enabled: true              # enable HTTP/HTTPS gateway
  host: "0.0.0.0"
  port: 8080
  # tls_cert_file: "/path/to/cert.pem"
  # tls_key_file:  "/path/to/key.pem"

# Cluster mode (optional)
cluster:
  enabled: false             # set to true for multi-node
  rpc_port: 9093
  seeds: []                  # e.g. ["node2:9093", "node3:9093"]
  gossip_interval_ms: 1000
  failure_threshold_ms: 5000

storage:
  backend: "file"          # "file" | "s3" | "redis" | "infinispan"
  data_dir: "./data"
  segment_size_mb: 1024
  retention_hours: 168

defaults:
  num_partitions: 3
  replication_factor: 1
```

See [docs/configuration.md](docs/configuration.md) for the full reference including S3, Redis, and Infinispan backend options.

## Storage Backends

Horizon implements the `StorageEngine` interface, allowing the persistence layer to be swapped without changing the broker or protocol handlers.

| Backend | Use Case | Dependencies |
|---------|----------|--------------|
| **File** (default) | Single-node, low latency | None |
| **S3 / MinIO** | Durable cloud storage, separation of compute and storage | S3-compatible endpoint |
| **Redis** | In-memory speed, shared state across brokers | Redis 5.0+ (Streams) |
| **Infinispan** | Red Hat / JBoss environments, distributed caching | Infinispan 10+ REST API |

See [docs/storage-backends.md](docs/storage-backends.md) for detailed configuration and implementation guide.

## Consumer Groups

Horizon implements the full Kafka consumer group protocol:

- **Blocking JoinGroup** – Handler goroutines block until all members join or the rebalance timeout expires
- **Blocking SyncGroup** – Followers block until the leader assigns partitions
- **Session Timeout** – Per-member timers automatically expire inactive consumers, triggering rebalance
- **State Machine** – Empty → PreparingRebalance → CompletingRebalance → Stable → Dead
- **Protocol Selection** – Picks the first assignment strategy supported by all members
- **DescribeGroups / ListGroups / DeleteGroups** – Full admin API support

See [docs/consumer-groups.md](docs/consumer-groups.md) for the complete design.

## Cluster Mode

Horizon supports **multi-node clustering** for horizontal scalability and high availability. When `cluster.enabled: true`, each broker participates in:

- **Gossip membership** (UDP SWIM-lite) – automatic peer discovery and failure detection
- **Controller election** – deterministic (lowest alive node ID), handles partition assignment
- **Partition assignment** – round-robin across alive nodes with configurable replication factor
- **Data replication** – followers continuously fetch from leaders via TCP RPC
- **Request routing** – produce/fetch requests are transparently forwarded to the correct leader

### 3-Node Cluster with Docker Compose

```yaml
# deployments/docker-compose-cluster.yml
services:
  horizon-1:
    image: horizon:latest
    ports: ["9092:9092", "8080:8080"]
    environment:
      - HORIZON_BROKER_ID=1
    volumes:
      - ./configs/cluster-node1.yaml:/app/config.yaml
      - horizon-data-1:/data

  horizon-2:
    image: horizon:latest
    ports: ["9192:9092", "8180:8080"]
    environment:
      - HORIZON_BROKER_ID=2
    volumes:
      - ./configs/cluster-node2.yaml:/app/config.yaml
      - horizon-data-2:/data

  horizon-3:
    image: horizon:latest
    ports: ["9292:9092", "8280:8080"]
    environment:
      - HORIZON_BROKER_ID=3
    volumes:
      - ./configs/cluster-node3.yaml:/app/config.yaml
      - horizon-data-3:/data

volumes:
  horizon-data-1:
  horizon-data-2:
  horizon-data-3:
```

See [docs/cluster.md](docs/cluster.md) for the full cluster architecture, deployment guide, and failure scenarios.

## Project Structure

```
horizon/
├── cmd/horizon/              # Application entry point
├── internal/
│   ├── broker/               # Broker logic, topic management, consumer groups
│   ├── cluster/              # Cluster mode (gossip, RPC, controller, replicator)
│   │   ├── cluster.go        # Main orchestrator, ClusterRouter implementation
│   │   ├── state.go          # ClusterState, NodeInfo, PartitionAssignment
│   │   ├── gossip.go         # UDP SWIM-lite membership protocol
│   │   ├── rpc.go            # TCP inter-broker RPC (forward, replicate)
│   │   ├── controller.go     # Controller election, partition assignment
│   │   └── replicator.go     # Follower fetch loops, ISR tracking
│   ├── config/               # YAML configuration loading
│   ├── protocol/             # Kafka wire protocol (reader/writer/types)
│   ├── server/               # TCP server, HTTP gateway, request dispatch
│   │   ├── http.go           # HTTP/HTTPS gateway (produce, admin, metadata)
│   │   ├── server.go         # TCP (Kafka protocol) server
│   │   └── handler*.go       # Kafka protocol request handlers
│   └── storage/              # Storage engine interface & file backend
│       ├── s3/               # S3 / MinIO backend
│       ├── redis/            # Redis Streams backend
│       └── infinispan/       # Infinispan REST API backend
├── build/Dockerfile          # Multi-stage Docker build (ports 9092, 8080, 9093)
├── configs/config.yaml       # Default configuration
├── deployments/              # Docker Compose files
├── docs/                     # Architecture & design documents
├── scripts/                  # Build & test scripts
├── benchmarks/               # Performance benchmarks
├── Makefile                  # Linux/macOS build targets
└── go.mod
```

## Benchmarks

```
BenchmarkProduce-8         500000    2341 ns/op    1.7 GB/s
BenchmarkFetch-8           800000    1523 ns/op    2.6 GB/s
BenchmarkPartitionWrite-8  1000000   1102 ns/op    3.6 GB/s
```

## Build Options

| Command | Description |
|---------|------------|
| `.\scripts\build.ps1` | Build for current platform |
| `.\scripts\build.ps1 -Target build-all` | Cross-compile for all platforms |
| `.\scripts\build.ps1 -Target docker` | Build Docker image |
| `.\scripts\build.ps1 -Target test` | Run tests |
| `.\scripts\build.ps1 -Target clean` | Clean build artifacts |
| `make build` | Build (Linux/macOS) |
| `make docker` | Docker image (Linux/macOS) |

## Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) first.

## License

Apache License 2.0 - See [LICENSE](LICENSE) for details.

[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fdarioajr%2Fhorizon.svg?type=large)](https://app.fossa.com/projects/git%2Bgithub.com%2Fdarioajr%2Fhorizon?ref=badge_large)