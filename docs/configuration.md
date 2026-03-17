# Configuration Reference

Horizon is configured via a YAML file (default: `configs/config.yaml`), environment variables, and/or command-line flags. This document lists every option.

## Configuration File

### Loading Order (lowest → highest priority)

1. Built-in defaults (see `config.Default()`)
2. YAML file (if `-config` flag is provided)
3. Environment variables (`HORIZON_*`)
4. Command-line flag overrides

### Full Example

```yaml
broker:
  id: 1
  host: "0.0.0.0"
  port: 9092
  cluster_id: "horizon-cluster"
  advertised_host: ""            # defaults to "localhost" when host is "0.0.0.0"

# HTTP/HTTPS gateway (optional)
http:
  enabled: false                 # set to true to activate
  host: "0.0.0.0"
  port: 8080
  # tls_cert_file: "/path/to/cert.pem"
  # tls_key_file:  "/path/to/key.pem"

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

# Cluster mode (optional)
cluster:
  enabled: false                 # set to true for multi-node deployment
  rpc_port: 9093                 # TCP port for inter-broker RPC
  seeds: []                      # seed node addresses, e.g. ["node2:9093", "node3:9093"]
  gossip_interval_ms: 1000       # gossip heartbeat interval
  failure_threshold_ms: 5000     # time before marking a node as dead

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

### `http`

Optional HTTP/HTTPS gateway. See [HTTP Gateway](http-gateway.md) for full endpoint documentation.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable the HTTP gateway. |
| `host` | string | `"0.0.0.0"` | Network interface to bind. |
| `port` | int | `8080` | HTTP listen port. |
| `tls_cert_file` | string | `""` | Path to TLS certificate (PEM). When both cert and key are set, the gateway runs as HTTPS. |
| `tls_key_file` | string | `""` | Path to TLS private key (PEM). |

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

### `cluster`

Optional multi-node cluster mode. See [Cluster Guide](cluster.md) for architecture and deployment details.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable cluster mode. When `false`, Horizon runs as a standalone single-node broker. |
| `rpc_port` | int | `9093` | TCP port for inter-broker RPC (request forwarding, replication, assignment broadcast). |
| `seeds` | []string | `[]` | List of seed node addresses in `host:rpc_port` format. Used for initial gossip join. Example: `["node2:9093", "node3:9093"]`. |
| `gossip_interval_ms` | int | `1000` | Interval (ms) between gossip heartbeat rounds. Each round pings up to 3 random peers. |
| `failure_threshold_ms` | int | `5000` | Time (ms) without a heartbeat before a node is marked suspect, then dead. Triggers controller re-election and partition reassignment. |

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

Environment variables override YAML values and are overridden by CLI flags.
They are particularly useful in container environments.

| Variable | YAML Equivalent | Default | Description |
|----------|-----------------|---------|-------------|
| `HORIZON_BROKER_ID` | `broker.id` | `1` | Broker node ID |
| `HORIZON_HOST` | `broker.host` | `0.0.0.0` | Bind address |
| `HORIZON_PORT` | `broker.port` | `9092` | Broker port |
| `HORIZON_ADVERTISED_HOST` | `broker.advertised_host` | `"localhost"` | **Host reported to clients in metadata.** Set to container name when using Docker networks |
| `HORIZON_DATA_DIR` | `storage.data_dir` | `./data` | Data directory path |

> **Why `HORIZON_ADVERTISED_HOST` matters:** When a Kafka client connects, it uses the bootstrap address only for the initial handshake. All subsequent connections use the *advertised* address from the metadata response. If the advertised host is `localhost`, clients in other containers cannot reach the broker. Set it to the container name or external hostname.

### Examples

```bash
# Docker with custom network (container-to-container)
docker run -d --name horizon --network my-net \
  -e HORIZON_ADVERTISED_HOST=horizon \
  darioajr/horizon

# External access from other machines
docker run -d --name horizon \
  -p 9092:9092 \
  -e HORIZON_ADVERTISED_HOST=192.168.1.100 \
  darioajr/horizon
```

---

## Docker / Podman Configuration

When running in Docker or Podman, mount the config file and data volume:

```bash
docker run -d \
  -p 9092:9092 \
  -p 8080:8080 \
  -p 9093:9093 \
  -e HORIZON_ADVERTISED_HOST=horizon \
  -v ./my-config.yaml:/app/config.yaml \
  -v horizon-data:/data \
  --name horizon \
  darioajr/horizon
```

Or use Docker Compose / Podman Compose:

```yaml
services:
  horizon:
    image: darioajr/horizon
    ports:
      - "9092:9092"
      - "8080:8080"
      - "9093:9093"
    environment:
      HORIZON_ADVERTISED_HOST: horizon
      HORIZON_BROKER_ID: "1"
    volumes:
      - ./configs/config.yaml:/app/config.yaml
      - horizon-data:/data
    networks:
      - app-net

volumes:
  horizon-data:

networks:
  app-net:
```
