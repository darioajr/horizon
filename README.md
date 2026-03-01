# Horizon - Kafka-Compatible Event Streaming Platform

A high-performance, Kafka protocol-compatible event streaming platform implemented in Go. Inspired by WarpStream and EventHorizon.

## Features

- **Kafka Protocol Compatible** - Works with existing Kafka clients (librdkafka, kafka-go, Sarama, etc.)
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
| [Consumer Groups](docs/consumer-groups.md) | Group coordinator, rebalance protocol, and state machine |
| [Storage Backends](docs/storage-backends.md) | StorageEngine interface and backend implementations |
| [Configuration](docs/configuration.md) | Complete configuration reference |

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
│       │                 │                                        │
│       │   ┌─────────────▼──────────────────────────────┐        │
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
docker run -d -p 9092:9092 -v horizon-data:/data --name horizon horizon:latest
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

## Project Structure

```
horizon/
├── cmd/horizon/              # Application entry point
├── internal/
│   ├── broker/               # Broker logic, topic management, consumer groups
│   ├── config/               # YAML configuration loading
│   ├── protocol/             # Kafka wire protocol (reader/writer/types)
│   ├── server/               # TCP server, connection handling, request dispatch
│   └── storage/              # Storage engine interface & file backend
│       ├── s3/               # S3 / MinIO backend
│       ├── redis/            # Redis Streams backend
│       └── infinispan/       # Infinispan REST API backend
├── build/Dockerfile          # Multi-stage Docker build
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

## License

Apache License 2.0 - See [LICENSE](LICENSE) for details.

## Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) first.
