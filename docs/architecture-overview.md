# Architecture Overview

This document describes the high-level architecture of Horizon, a Kafka protocol-compatible event streaming platform written in Go.

## Design Goals

1. **Kafka wire-protocol compatibility** – Any Kafka client (librdkafka, kafka-go, Sarama, confluent-kafka-python, etc.) must be able to connect and produce/consume without modification.
2. **Pluggable persistence** – The storage layer is an interface (`StorageEngine`) so the backing store can be swapped between local files, S3, Redis, or Infinispan without touching broker or protocol code.
3. **Single binary, zero dependencies** – The default file backend requires nothing beyond the operating system. Alternative backends are opt-in via configuration.
4. **High throughput, low latency** – Zero-copy I/O paths, buffer pooling, TCP_NODELAY, and buffered writes minimize allocations and system calls.
5. **Horizontal scalability** – Optional cluster mode with gossip membership, automatic partition assignment, leader election, and data replication.

## Component Diagram

```
       Kafka Clients                    HTTP Clients
  (produce / fetch / group)        (curl, fetch, Postman…)
            │                              │
            ▼                              ▼
   ┌────────────────┐            ┌─────────────────┐
   │   TCP Server   │            │  HTTP/HTTPS     │
   │  connection.go │            │  Gateway        │
   └───────┬────────┘            │  (http.go)      │
           │  reads Kafka        └──────┬──────────┘
           │  request frames            │  REST API
           ▼                            │
   ┌────────────────┐                   │
   │    Handler     │  (handler.go)     │
   │   Dispatch     │                   │
   └──┬──────┬──┬──┘                   │
      │      │  │                      │
   ┌──┘      │  └──────────┐           │
   ▼         ▼              ▼          │
┌────────┐ ┌──────────┐ ┌──────────┐   │
│Produce │ │  Fetch   │ │ Group    │   │
│Handler │ │ Handler  │ │ APIs     │   │
└───┬────┘ └────┬─────┘ └────┬─────┘   │
    │           │             │         │
    └─────┬─────┘             │         │
          ▼                   ▼         │
   ┌────────────────┐  ┌───────────┐   │
   │     Broker     │◀─│  Group    │   │
   │   (broker.go)  │  │  Manager  │   │
   │                │◀─────────────────┘
   └───────┬────────┘  └───────────┘
           │
           ▼
   ┌────────────────┐
   │ StorageEngine  │  (interfaces.go)
   │   interface    │
   ├────────────────┤
   │  File │ S3 │ Redis │ Infinispan
   └────────────────┘
           ▲
           │ (replication writes)
   ┌───────┴──────────────────────────────────────────┐
   │              Cluster Layer (optional)             │
   │  ┌──────────┐  ┌──────────┐  ┌────────────────┐  │
   │  │  Gossip  │  │   RPC    │  │  Controller    │  │
   │  │  (UDP)   │  │  (TCP)   │  │  Election &    │  │
   │  │  SWIM    │  │  Forward │  │  Partition     │  │
   │  │  lite    │  │  Replica │  │  Assignment    │  │
   │  └──────────┘  └──────────┘  └────────────────┘  │
   │  ┌──────────────────────────────────────────────┐ │
   │  │  Replicator – follower fetch loops, ISR mgmt │ │
   │  └──────────────────────────────────────────────┘ │
   └──────────────────────────────────────────────────┘
```

## Packages

### `cmd/horizon`

Application entry point. Parses CLI flags, loads YAML configuration, creates the storage engine (based on `storage.backend`), initialises the broker, starts the TCP server, optionally starts the HTTP/HTTPS gateway (when `http.enabled: true`), optionally bootstraps the cluster layer (when `cluster.enabled: true`), and handles OS signals for graceful shutdown of all components.

### `internal/server`

| File | Responsibility |
|------|---------------|
| `server.go` | TCP listener, accept loop, connection lifecycle |
| `connection.go` | Per-connection goroutine: read request frame → dispatch → write response. Uses `bufio.Reader`/`Writer` (64 KB buffers) and `sync.Pool` for request buffers |
| `http.go` | HTTP/HTTPS gateway — REST interface for producing (`POST /topics/{topic}`), metadata (`GET /topics`), health check (`GET /health`), and admin API (`PUT`/`DELETE`/`PATCH`/`POST purge` on `/admin/topics/{topic}`). Supports query-param and header-based configuration of compression, data type, record key. See [HTTP Gateway](http-gateway.md). |
| `handler.go` | `RequestHandler.HandleRequest()` – switch on `ApiKey` to call the specific handler |
| `handler_produce.go` | Produce API |
| `handler_fetch.go` | Fetch API |
| `handler_group.go` | JoinGroup, SyncGroup, Heartbeat, LeaveGroup, FindCoordinator, DescribeGroups, ListGroups, DeleteGroups |
| `handler_offset.go` | OffsetCommit, OffsetFetch |
| `handler_metadata.go` | ApiVersions, Metadata |
| `handler_topic.go` | CreateTopics, DeleteTopics |
| `handler_init_producer.go` | InitProducerId (idempotent producer support) |
| `request.go` | `Request` / `Response` types with pooled `protocol.Writer` |

### `internal/broker`

| File | Responsibility |
|------|---------------|
| `broker.go` | `Broker` struct – topic management, metadata, produce/fetch orchestration. Accepts a `StorageEngine` via dependency injection. Exposes `ClusterRouter` interface for cluster integration. |
| `group.go` | `ConsumerGroup` – full Kafka-compatible group coordinator with blocking JoinGroup/SyncGroup, session timers, rebalance state machine. `GroupManager` – manages all groups. |
| `errors.go` | Sentinel errors (`ErrUnknownMember`, `ErrIllegalGeneration`, etc.) |

### `internal/protocol`

| File | Responsibility |
|------|---------------|
| `types.go` | API key constants, error code constants |
| `reader.go` | Binary reader for Kafka wire format (big-endian integers, length-prefixed strings/bytes, arrays) |
| `writer.go` | Binary writer with `sync.Pool` for buffer reuse |

### `internal/storage`

| File | Responsibility |
|------|---------------|
| `interfaces.go` | `StorageEngine` and `PartitionReader` interfaces |
| `factory.go` | `NewEngine()` factory with `BackendType` enum and functional options |
| `log.go` | `Log` – file-based `StorageEngine` implementation (manages topics and partitions) |
| `partition.go` | `Partition` – manages ordered segments for a single topic-partition |
| `segment.go` | `Segment` – append-only log file + offset index + time index |
| `record.go` | `Record`, `RecordBatch`, `TopicMetadata` types |
| `errors.go` | Storage-level errors |
| `s3/s3.go` | S3 / MinIO backend (skeleton) |
| `redis/redis.go` | Redis Streams backend (skeleton) |
| `infinispan/infinispan.go` | Infinispan REST backend (skeleton) |

### `internal/config`

| File | Responsibility |
|------|---------------|
| `config.go` | YAML configuration structs (`Config`, `BrokerConfig`, `StorageConfig`, `ClusterConfig`, etc.) and `Load()` / `Default()` |
| `errors.go` | Config validation errors |

### `internal/cluster`

Optional cluster layer. Only active when `cluster.enabled: true`. All cluster types are isolated in this package to avoid circular dependencies with the broker.

| File | Responsibility |
|------|---------------|
| `cluster.go` | Main orchestrator. Implements `broker.ClusterRouter` interface. Manages lifecycle of gossip, RPC, controller, and replicator. |
| `state.go` | `ClusterState` – thread-safe cluster metadata: `NodeInfo` (ID, host, ports, state, generation), `PartitionAssignment` (topic, partition, leader, replicas, ISR, leader epoch). Binary encoding/decoding for network transport. |
| `gossip.go` | UDP SWIM-lite membership protocol. Sends ping/ack/join/leave messages. Merges node lists, detects failures (alive → suspect → dead), triggers controller re-election on membership changes. |
| `rpc.go` | TCP binary RPC server and client pool. Frame protocol: `[4 len][1 type][4 corrID][payload]`. Message types: ForwardProduce, ForwardFetch, ReplicaFetch, AssignBroadcast, AckOffset. |
| `controller.go` | Deterministic controller election (lowest alive node ID). Computes partition assignments (round-robin with replication factor) and broadcasts to all nodes via RPC. |
| `replicator.go` | Follower fetch loops. Each follower partition runs a goroutine that continuously fetches from the leader via RPC, writes locally via `StorageEngine`, and reports offset progress. Exponential backoff on errors. |

## Request Lifecycle

```
1. Client opens TCP connection
2. Server accept loop creates Connection (1 goroutine)
3. Connection.Handle() loop:
   a. Read 4-byte size prefix
   b. Read request payload into pooled buffer
   c. Parse Kafka request header (ApiKey, Version, CorrelationID, ClientID)
   d. Dispatch to handler based on ApiKey
   e. Handler interacts with Broker / GroupManager / StorageEngine
   f. **Cluster routing check** (if cluster enabled):
      - If this node is the partition leader → process locally
      - If not → forward to leader via RPC, return response to client
   g. Handler writes response using protocol.Writer
   h. Connection writes 4-byte size prefix + response bytes
   i. Flush buffered writer to socket
   j. Return request buffer to pool
   k. Loop back to (a)
4. On error or client disconnect, goroutine exits and connection is cleaned up
```

## Concurrency Model

- **One goroutine per TCP connection** – Sequential request processing within a connection. This matches Kafka's per-connection ordering guarantee and avoids lock contention.
- **Shared broker state** – Topic metadata and storage are protected by `sync.RWMutex`. Writes (produce) take a write lock per partition; reads (fetch) take a read lock.
- **Consumer group coordination** – JoinGroup/SyncGroup block the handler goroutine using buffered channels. The rebalance timer and session timeout run as `time.AfterFunc` goroutines.
- **Buffer pooling** – `sync.Pool` is used for both request data buffers (1 MB default) and `protocol.Writer` byte slices to reduce GC pressure under high throughput.
- **Cluster goroutines** – When cluster mode is enabled: gossip heartbeat loop (1 goroutine), gossip receive loop (1 goroutine), RPC server accept loop (1 goroutine), 1 goroutine per RPC connection, 1 replicator goroutine per follower partition. Controller election and assignment broadcast are triggered by callbacks, not dedicated goroutines.

## Data Flow

### Produce

```
Client → Connection → handleProduce → Cluster routing check
  → [If not leader] → RPC ForwardProduce → Leader node → response
  → [If leader]     → Broker.Produce()
    → StorageEngine.AppendRaw(topic, partition, data)
      → Partition.AppendRaw()
        → Segment.Append() (write to .log file, update .index/.timeindex)
    ← base offset returned
← Produce response (base offset, error code)
```

### Fetch

```
Client → Connection → handleFetch → Cluster routing check
  → [If not leader] → RPC ForwardFetch → Leader node → response
  → [If leader]     → Broker.Fetch()
    → StorageEngine.Fetch(topic, partition, offset, maxBytes)
      → Partition.Fetch()
        → Segment.Read() (binary search index, read from .log file)
    ← RecordBatch[]
← Fetch response (records, highWatermark)
```

### Consumer Group Join

```
Client → Connection → handleJoinGroup
  → GroupManager.GetOrCreateGroup(groupID)
  → ConsumerGroup.JoinGroup(memberID, protocols, …)
    → Adds/updates member
    → Transitions to PreparingRebalance
    → Starts rebalance timer (time.AfterFunc)
    → Blocks on channel ← (waits for all members or timeout)
    → completeJoinPhaseLocked():
       ▸ Bumps generation
       ▸ Selects protocol
       ▸ Elects leader
       ▸ Sends JoinResult to all waiting goroutines
  ← JoinResult (memberID, generation, leader, members)
← JoinGroup response
```

## Deployment

Horizon is distributed as a single static binary or a minimal Docker image (~15 MB) based on `alpine:3.19`. See `build/Dockerfile` for the multi-stage build that produces both Linux and Windows binaries.

```yaml
# deployments/docker-compose.yml (standalone)
services:
  horizon:
    image: horizon:latest
    ports:
      - "9092:9092"   # Kafka protocol
      - "8080:8080"   # HTTP gateway
    volumes:
      - horizon-data:/data
```

### Cluster Deployment

For a multi-node cluster, each node needs a unique `broker.id`, its own `cluster.seeds` list pointing to other nodes' RPC ports, and `cluster.enabled: true`. See [cluster.md](cluster.md) for a full deployment guide.
