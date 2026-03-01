# Storage Backends

This document describes the `StorageEngine` interface and the available backend implementations.

## Interface

All persistence in Horizon goes through two interfaces defined in `internal/storage/interfaces.go`:

```go
// StorageEngine is the main interface for the persistence layer.
type StorageEngine interface {
    // Topic management
    CreateTopic(topic string, numPartitions int32) error
    DeleteTopic(topic string) error
    ListTopics() []string
    GetTopicPartitions(topic string) ([]int32, error)
    GetTopicMetadata(topic string) (*TopicMetadata, error)

    // Partition access
    GetPartition(topic string, partition int32) (PartitionReader, error)
    GetOrCreatePartition(topic string, partition int32) (PartitionReader, error)

    // Data operations
    AppendRaw(topic string, partition int32, data []byte, recordCount int32, maxTimestamp int64) (int64, error)
    Append(topic string, partition int32, records []Record) (int64, error)
    Fetch(topic string, partition int32, offset int64, maxBytes int64) ([]*RecordBatch, error)

    // Lifecycle
    Sync() error
    Close() error
}

// PartitionReader provides read-only access to partition metadata.
type PartitionReader interface {
    Topic() string
    PartitionNum() int32
    HighWatermark() int64
    LogStartOffset() int64
    LogEndOffset() int64
    GetOffsetByTime(timestamp int64) (int64, error)
}
```

The broker uses `StorageEngine` exclusively – it never references a concrete implementation directly.

## Backend Selection

The backend is configured via `storage.backend` in `configs/config.yaml`:

```yaml
storage:
  backend: "file"   # "file" | "s3" | "redis" | "infinispan"
```

At startup, `cmd/horizon/main.go` creates the appropriate engine:

```go
switch storage.BackendType(cfg.Storage.Backend) {
case storage.BackendS3:
    engine, err = s3.New(s3Cfg)
case storage.BackendRedis:
    engine, err = redis.New(redisCfg)
case storage.BackendInfinispan:
    engine, err = infinispan.New(ispnCfg)
default:
    // file backend – broker creates it internally
}
```

## File Backend (default)

**Package:** `internal/storage` (types `Log`, `Partition`, `Segment`)

The file backend stores data as append-only log segments on the local filesystem, mirroring the Kafka storage format.

### Data Layout

```
data/
├── my-topic-0/
│   ├── 00000000000000000000.log        # record data
│   ├── 00000000000000000000.index      # offset → position index
│   └── 00000000000000000000.timeindex  # timestamp → offset index
├── my-topic-1/
│   ├── 00000000000000000000.log
│   ├── 00000000000000000000.index
│   └── 00000000000000000000.timeindex
└── …
```

Each directory represents a topic-partition (`{topic}-{partition}`). Segments are named by their base offset, zero-padded to 20 digits.

### Segment Structure

| File | Purpose | Format |
|------|---------|--------|
| `.log` | Record batches in Kafka wire format | Append-only binary |
| `.index` | Maps relative offset → byte position | 8-byte entries (offset:4 + position:4) |
| `.timeindex` | Maps timestamp → relative offset | 12-byte entries (timestamp:8 + offset:4) |

Index entries are written every `IndexIntervalBytes` (default 4096) bytes of log data to balance lookup speed and index size.

### Write Path

```
AppendRaw(topic, partition, data, recordCount, maxTimestamp)
  → Partition.AppendRaw()
    → activeSegment.Append(data)
      → write to .log file
      → update .index (if interval exceeded)
      → update .timeindex
    → if segment full → roll new segment
  → update highWatermark
  → return base offset
```

### Read Path

```
Fetch(topic, partition, offset, maxBytes)
  → Partition.Fetch()
    → binary search segments by base offset
    → Segment.Read(offset, maxBytes)
      → binary search .index for byte position
      → read from .log file starting at position
    → return RecordBatch[]
```

### Configuration

```yaml
storage:
  backend: "file"
  data_dir: "./data"           # root directory
  segment_size_mb: 1024        # max segment size (1 GB default)
  retention_hours: 168         # log retention (7 days default)
  sync_writes: false           # fsync on every write
  flush_interval: 1s           # background flush interval
```

### Pros & Cons

| | |
|---|---|
| **Pros** | Zero dependencies, lowest latency, simplest ops |
| **Cons** | Single-node only, no built-in replication |

---

## S3 / MinIO Backend

**Package:** `internal/storage/s3`

Stores data in an S3-compatible object store. This is ideal for separating compute from storage (WarpStream model).

### Data Mapping

| Concept | S3 Object |
|---------|-----------|
| Partition | Key prefix: `{prefix}/{topic}-{partition}/` |
| Segment | Object: `{prefix}/{topic}-{partition}/{baseOffset}.log` |
| Index | Object: `{prefix}/{topic}-{partition}/{baseOffset}.index` |
| Topic metadata | Object: `{prefix}/__meta/{topic}.json` |

### Configuration

```yaml
storage:
  backend: "s3"
  segment_size_mb: 1024
  s3:
    bucket: "horizon-data"
    prefix: ""                    # optional key prefix
    region: "us-east-1"
    endpoint: ""                  # empty = AWS; set for MinIO/LocalStack
    access_key: "AKIAEXAMPLE"
    secret_key: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
```

| Parameter | Description | Default |
|-----------|-------------|---------|
| `bucket` | S3 bucket name | `horizon-data` |
| `prefix` | Key prefix inside the bucket | `""` |
| `region` | AWS region | `us-east-1` |
| `endpoint` | Custom endpoint (MinIO, LocalStack) | `""` (AWS) |
| `access_key` | AWS Access Key | |
| `secret_key` | AWS Secret Key | |

### Pros & Cons

| | |
|---|---|
| **Pros** | Virtually unlimited storage, built-in durability (11 nines), separation of compute and storage |
| **Cons** | Higher latency per operation, eventual consistency (mitigated by read-after-write for PUTs) |

> **Status:** Skeleton implementation. The interface methods are implemented with correct signatures and error handling. Actual S3 SDK calls are marked with `// TODO`.

---

## Redis Backend

**Package:** `internal/storage/redis`

Uses Redis Streams as the storage primitive. Each partition maps to a Redis Stream; metadata and indexes are stored in hashes and sorted sets.

### Data Mapping

| Concept | Redis Key |
|---------|-----------|
| Partition data | Stream: `{prefix}:stream:{topic}:{partition}` |
| Offset index | Sorted set: `{prefix}:index:{topic}:{partition}` |
| Topic metadata | Hash: `{prefix}:meta:{topic}` |
| Topic list | Set: `{prefix}:topics` |

### Configuration

```yaml
storage:
  backend: "redis"
  segment_size_mb: 1024
  redis:
    addr: "localhost:6379"
    password: ""
    db: 0
    key_prefix: "horizon"
```

| Parameter | Description | Default |
|-----------|-------------|---------|
| `addr` | Redis server address | `localhost:6379` |
| `password` | AUTH password | `""` |
| `db` | Redis database number | `0` |
| `key_prefix` | Prefix for all keys | `horizon` |

### Pros & Cons

| | |
|---|---|
| **Pros** | In-memory speed, Redis Streams are an excellent fit for append-only logs, can share state across brokers |
| **Cons** | Data limited by memory (unless using Redis on Flash), requires running Redis |

> **Status:** Skeleton implementation.

---

## Infinispan Backend

**Package:** `internal/storage/infinispan`

Uses the Infinispan REST API v2. Suitable for Red Hat / JBoss environments where Infinispan (Data Grid) is already deployed.

### Data Mapping

| Concept | Infinispan Resource |
|---------|-------------------|
| Partition data | Cache: `{cacheName}-{topic}-{partition}`, keys = offset |
| Topic metadata | Cache: `{cacheName}-meta`, key = topic name |
| Topic list | Cache: `{cacheName}-meta`, key = `__topics` |

### Configuration

```yaml
storage:
  backend: "infinispan"
  segment_size_mb: 1024
  infinispan:
    url: "http://localhost:11222"
    cache_name: "horizon"
    username: "admin"
    password: "secret"
```

| Parameter | Description | Default |
|-----------|-------------|---------|
| `url` | Infinispan REST API base URL | `http://localhost:11222` |
| `cache_name` | Cache name prefix | `horizon` |
| `username` | HTTP Basic Auth username | `""` |
| `password` | HTTP Basic Auth password | `""` |

### Pros & Cons

| | |
|---|---|
| **Pros** | Distributed caching, enterprise support (Red Hat), cross-datacenter replication |
| **Cons** | HTTP overhead per operation, requires Infinispan cluster |

> **Status:** Skeleton implementation.

---

## Implementing a Custom Backend

To add a new backend:

### 1. Create the package

```
internal/storage/mybackend/
└── mybackend.go
```

### 2. Implement the interfaces

```go
package mybackend

import "horizon/internal/storage"

// Compile-time interface check
var _ storage.StorageEngine = (*Engine)(nil)

type Engine struct {
    // your connection/client fields
}

func New(cfg Config) (*Engine, error) {
    // initialise connection
    return &Engine{...}, nil
}

// Implement all StorageEngine methods...
func (e *Engine) CreateTopic(topic string, numPartitions int32) error { ... }
func (e *Engine) AppendRaw(topic string, partition int32, data []byte, recordCount int32, maxTimestamp int64) (int64, error) { ... }
// ... etc.
```

### 3. Register in main.go

Add a new case to the backend switch in `cmd/horizon/main.go`:

```go
case storage.BackendType("mybackend"):
    engine, err = mybackend.New(mybackendCfg)
```

### 4. Add config structs

Add a `MyBackendConfig` struct in `internal/config/config.go` and a corresponding YAML field in `StorageConfig`.

### 5. Add to factory (optional)

Register a `BackendType` constant and option in `internal/storage/factory.go` for use with the programmatic API:

```go
const BackendMyBackend BackendType = "mybackend"
```
