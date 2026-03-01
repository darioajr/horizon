# Configuration Reference

Horizon is configured via a YAML file (default: `configs/config.yaml`) and/or command-line flags. This document lists every option.

## Configuration File

### Loading Order

1. Built-in defaults (see `config.Default()`)
2. YAML file (if `-config` flag is provided)
3. Command-line flag overrides

### Full Example

```yaml
broker:
  id: 1
  host: "0.0.0.0"
  port: 9092
  cluster_id: "horizon-cluster"
  advertised_host: ""            # defaults to "localhost" when host is "0.0.0.0"

storage:
  backend: "file"                # "file" | "s3" | "redis" | "infinispan"
  data_dir: "./data"             # used by file backend
  segment_size_mb: 1024          # 1 GB per segment
  retention_hours: 168           # 7 days
  sync_writes: false             # fsync on every write
  flush_interval: 1s             # background flush interval

  # S3 / MinIO backend
  s3:
    bucket: "horizon-data"
    prefix: ""
    region: "us-east-1"
    endpoint: ""                 # empty = AWS, set for MinIO
    access_key: ""
    secret_key: ""

  # Redis backend
  redis:
    addr: "localhost:6379"
    password: ""
    db: 0
    key_prefix: "horizon"

  # Infinispan backend
  infinispan:
    url: "http://localhost:11222"
    cache_name: "horizon"
    username: ""
    password: ""

defaults:
  num_partitions: 3              # partitions for auto-created topics
  replication_factor: 1          # replication factor (future use)

performance:
  write_buffer_kb: 2048          # write buffer size
  max_connections: 10000         # max concurrent TCP connections
  io_threads: 4                  # I/O worker threads (future use)
  read_buffer_size: 65536        # read buffer per connection (bytes)
```

---

## Sections

### `broker`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `id` | int | `1` | Unique broker node ID. Used in metadata responses. |
| `host` | string | `"0.0.0.0"` | Network interface to bind. `"0.0.0.0"` listens on all interfaces. |
| `port` | int | `9092` | TCP port for Kafka protocol connections. |
| `cluster_id` | string | `"horizon-cluster"` | Cluster identifier returned in metadata responses. |
| `advertised_host` | string | `""` | Host reported to clients. Defaults to `"localhost"` when `host` is `"0.0.0.0"`. Set this when running behind a load balancer or in Docker. |

### `storage`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `backend` | string | `"file"` | Storage engine to use: `"file"`, `"s3"`, `"redis"`, or `"infinispan"`. |
| `data_dir` | string | `"./data"` | Root directory for the file backend. Ignored by other backends. |
| `segment_size_mb` | int | `1024` | Maximum size of a single log segment in MB. |
| `retention_hours` | int | `168` | How long to retain log data (hours). `168` = 7 days. |
| `sync_writes` | bool | `false` | When `true`, calls fsync after every write. Increases durability but reduces throughput. |
| `flush_interval` | duration | `1s` | Interval for background flushes when `sync_writes` is false. |

### `storage.s3`

Only used when `backend: "s3"`. See [Storage Backends](storage-backends.md#s3--minio-backend).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `bucket` | string | `"horizon-data"` | S3 bucket name. |
| `prefix` | string | `""` | Optional key prefix inside the bucket. |
| `region` | string | `"us-east-1"` | AWS region. |
| `endpoint` | string | `""` | Custom S3 endpoint for MinIO / LocalStack. Empty = use AWS. |
| `access_key` | string | `""` | AWS access key ID. |
| `secret_key` | string | `""` | AWS secret access key. |

### `storage.redis`

Only used when `backend: "redis"`. See [Storage Backends](storage-backends.md#redis-backend).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `addr` | string | `"localhost:6379"` | Redis server address. |
| `password` | string | `""` | Redis AUTH password. |
| `db` | int | `0` | Redis database number. |
| `key_prefix` | string | `"horizon"` | Prefix for all Redis keys. |

### `storage.infinispan`

Only used when `backend: "infinispan"`. See [Storage Backends](storage-backends.md#infinispan-backend).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `url` | string | `"http://localhost:11222"` | Infinispan REST API base URL. |
| `cache_name` | string | `"horizon"` | Cache name prefix. |
| `username` | string | `""` | HTTP Basic Auth username. |
| `password` | string | `""` | HTTP Basic Auth password. |

### `defaults`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `num_partitions` | int | `3` | Default number of partitions for auto-created topics. |
| `replication_factor` | int | `1` | Default replication factor (reserved for future clustering). |

### `performance`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `write_buffer_kb` | int | `2048` | Write buffer size in KB. |
| `max_connections` | int | `10000` | Maximum concurrent TCP connections. |
| `io_threads` | int | `4` | I/O worker threads (reserved for future use). |
| `read_buffer_size` | int | `65536` | Read buffer size per connection in bytes. |

---

## Command-Line Flags

Flags override the corresponding YAML values:

| Flag | YAML Equivalent | Default | Description |
|------|-----------------|---------|-------------|
| `-config` | — | `""` | Path to YAML configuration file |
| `-port` | `broker.port` | `9092` | Broker TCP port |
| `-host` | `broker.host` | `0.0.0.0` | Bind address |
| `-advertised-host` | `broker.advertised_host` | `""` | Advertised host for clients |
| `-data-dir` | `storage.data_dir` | `./data` | Data directory |
| `-broker-id` | `broker.id` | `0` | Broker node ID |
| `-version` | — | — | Print version info and exit |

### Examples

```bash
# Use config file with port override
./horizon -config configs/config.yaml -port 19092

# Minimal run with defaults
./horizon

# Docker with custom data directory
./horizon -data-dir /data -host 0.0.0.0 -advertised-host kafka.example.com
```

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `HORIZON_BROKER_ID` | Broker node ID | `1` |
| `HORIZON_HOST` | Bind address | `0.0.0.0` |
| `HORIZON_PORT` | Broker port | `9092` |

> **Note:** Environment variable support is planned. Currently, use flags or the YAML file.

---

## Docker Configuration

When running in Docker, mount the config file and data volume:

```bash
docker run -d \
  -p 9092:9092 \
  -v ./my-config.yaml:/app/config.yaml \
  -v horizon-data:/data \
  --name horizon \
  horizon:latest
```

Or use Docker Compose:

```yaml
services:
  horizon:
    image: horizon:latest
    ports:
      - "9092:9092"
    volumes:
      - ./configs/config.yaml:/app/config.yaml
      - horizon-data:/data
    environment:
      - HORIZON_BROKER_ID=1

volumes:
  horizon-data:
```
