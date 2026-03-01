# Cluster Mode

This document describes Horizon's multi-node cluster architecture, how it works internally, and how to deploy a cluster.

## Overview

When `cluster.enabled: true`, Horizon brokers form a self-organising cluster using:

| Component | Transport | Purpose |
|-----------|-----------|---------|
| **Gossip** | UDP | Membership discovery and failure detection (SWIM-lite) |
| **RPC** | TCP | Request forwarding, data replication, assignment broadcast |
| **Controller** | — | Elected leader that computes and distributes partition assignments |
| **Replicator** | TCP (RPC) | Follower fetch loops that replicate data from partition leaders |

In standalone mode (`cluster.enabled: false`), none of these components are started and the broker behaves as a single-node system with zero overhead.

## Architecture

```
                     ┌────────── Node 1 (Controller) ──────────┐
                     │  Broker · Storage · Gossip · RPC        │
                     │  Controller: assigns partitions         │
                     │  Leader for: topicA-0, topicB-1         │
                     │  Follower for: topicA-1, topicB-0       │
                     └──────────┬──────────┬───────────────────┘
                UDP gossip ◄────┘          │ TCP RPC
                     ┌──────────┴──────────┴───────────────────┐
                     │          Network Fabric                 │
                     └──────────┬──────────┬───────────────────┘
           ┌────────────────────┘          └────────────────────┐
           ▼                                                    ▼
┌────────── Node 2 ──────────┐              ┌────────── Node 3 ──────────┐
│  Broker · Storage · Gossip │              │  Broker · Storage · Gossip │
│  Leader for: topicA-1      │              │  Leader for: topicB-0      │
│  Follower for: topicB-1    │              │  Follower for: topicA-0    │
└────────────────────────────┘              └────────────────────────────┘
```

## Gossip Protocol (SWIM-lite)

The gossip layer handles **peer discovery** and **failure detection** using a UDP-based protocol inspired by SWIM.

### Message Types

| Type | Code | Description |
|------|------|-------------|
| Ping | `0x01` | Heartbeat probe. Includes sender's full node list. |
| Ack | `0x02` | Response to ping. Includes responder's node list. |
| Join | `0x03` | Sent to seed nodes on startup. |
| Leave | `0x04` | Graceful shutdown announcement. |

### Lifecycle

1. On startup, the node sends **Join** to all configured `seeds`
2. Every `gossip_interval_ms`, the node selects up to **3 random peers** and sends a **Ping** with its known node list
3. Receiving a Ping or Ack triggers **node list merge** — new nodes are added, existing nodes are updated if their generation is higher
4. If a node misses heartbeats for `failure_threshold_ms`, it transitions: **Alive → Suspect → Dead**
5. Membership changes trigger the `onMemberChange` callback, which re-evaluates controller election

### Node States

```
  Alive ──(timeout)──► Suspect ──(timeout)──► Dead
    ▲                                           │
    └────────────(heartbeat received)───────────┘
```

A **Left** state is set when a node sends a graceful Leave message.

## Controller Election

The controller is the node responsible for computing and broadcasting partition assignments.

**Election rule:** The alive node with the **lowest node ID** becomes the controller. This is deterministic — all nodes independently reach the same conclusion.

When the controller changes (due to membership change):
1. Every node runs `electController()` after merging node lists
2. If `localNodeID == controllerID`, the node takes controller responsibility
3. The new controller computes partition assignments and broadcasts them to all alive nodes via RPC

## Partition Assignment

The controller assigns partitions using a **round-robin** strategy:

1. Collect all alive nodes, sorted by ID
2. For each topic, for each partition:
   - **Leader** = `nodes[partitionIndex % len(nodes)]`
   - **Replicas** = next `replicationFactor - 1` nodes (wrapping around)
3. If the leader hasn't changed, the `LeaderEpoch` is preserved
4. If a new leader is elected, the `LeaderEpoch` is bumped

The assignment is broadcast via `rpcAssignBroadcast` to all alive nodes. Each node applies the assignment to its local `ClusterState`.

## Data Replication

Replication follows a **pull model** (similar to Kafka):

1. After receiving a partition assignment, the **Replicator** compares desired follower partitions with running fetch loops
2. For each new follower assignment, a goroutine is started:
   ```
   loop:
     localOffset = storage.GetLatestOffset(topic, partition)
     batches = RPC ReplicaFetch(leader, topic, partition, localOffset)
     storage.ProduceRaw(topic, partition, batches)
     RPC AckOffset(leader, topic, partition, newOffset)
     sleep or exponential backoff on error
   ```
3. The leader tracks follower offsets for ISR (In-Sync Replicas) management

### ISR Management

- A follower is **in-sync** when its acknowledged offset is within a configurable lag of the leader's high watermark
- The `PartitionAssignment.ISR` field reflects the current in-sync replica set
- ISR changes are propagated via gossip state updates

## Request Routing

When a client sends a Produce or Fetch request:

```
Client → Broker (any node)
  → Is this node the leader for the partition?
     YES → Process locally → Response
     NO  → Forward via RPC to leader → Return leader's response
```

Both the Kafka TCP handlers and the HTTP gateway perform this routing check. The client does not need to know which node is the leader.

### Metadata Response

In cluster mode, `Metadata` responses include **all brokers** in the cluster with accurate partition leadership information. Kafka clients use this to route requests to the correct broker, but Horizon also transparently forwards when clients connect to the wrong node.

## Configuration

```yaml
broker:
  id: 1                          # must be unique per node
  host: "0.0.0.0"
  port: 9092

cluster:
  enabled: true
  rpc_port: 9093
  seeds: ["node2:9093", "node3:9093"]
  gossip_interval_ms: 1000
  failure_threshold_ms: 5000

defaults:
  replication_factor: 2          # how many copies of each partition
```

| Field | Description |
|-------|-------------|
| `broker.id` | Must be unique across the cluster. Used for controller election (lowest ID wins). |
| `cluster.enabled` | Set to `true` to activate cluster mode. |
| `cluster.rpc_port` | TCP port for inter-broker RPC. Must be accessible from all other nodes. |
| `cluster.seeds` | Initial peer list for gossip join. Format: `host:rpc_port`. Not all nodes need to list all others — gossip propagates. |
| `cluster.gossip_interval_ms` | How often to ping peers. Lower = faster failure detection, more network traffic. |
| `cluster.failure_threshold_ms` | How long to wait without heartbeat before marking a node dead. |
| `defaults.replication_factor` | Number of replicas per partition (including leader). `1` = no replication, `2` = one leader + one follower, etc. |

## Deployment Guide

### 3-Node Docker Compose

Create per-node config files:

**configs/cluster-node1.yaml:**
```yaml
broker:
  id: 1
  host: "0.0.0.0"
  port: 9092
  advertised_host: "horizon-1"

http:
  enabled: true
  port: 8080

cluster:
  enabled: true
  rpc_port: 9093
  seeds: ["horizon-2:9093", "horizon-3:9093"]

storage:
  backend: "file"
  data_dir: "/data"

defaults:
  num_partitions: 6
  replication_factor: 2
```

**configs/cluster-node2.yaml:** (same, but `id: 2`, `advertised_host: "horizon-2"`, seeds point to nodes 1 and 3)

**configs/cluster-node3.yaml:** (same, but `id: 3`, `advertised_host: "horizon-3"`, seeds point to nodes 1 and 2)

**docker-compose-cluster.yml:**
```yaml
services:
  horizon-1:
    image: horizon:latest
    hostname: horizon-1
    ports:
      - "9092:9092"
      - "8080:8080"
      - "9093:9093"
    volumes:
      - ./configs/cluster-node1.yaml:/app/config.yaml
      - horizon-data-1:/data

  horizon-2:
    image: horizon:latest
    hostname: horizon-2
    ports:
      - "9192:9092"
      - "8180:8080"
      - "9193:9093"
    volumes:
      - ./configs/cluster-node2.yaml:/app/config.yaml
      - horizon-data-2:/data

  horizon-3:
    image: horizon:latest
    hostname: horizon-3
    ports:
      - "9292:9092"
      - "8280:8080"
      - "9293:9093"
    volumes:
      - ./configs/cluster-node3.yaml:/app/config.yaml
      - horizon-data-3:/data

volumes:
  horizon-data-1:
  horizon-data-2:
  horizon-data-3:
```

```bash
docker compose -f docker-compose-cluster.yml up -d
```

### Health Check

```bash
# Check cluster status via HTTP health endpoint
curl http://localhost:8080/health

# Response (cluster mode):
{
  "status": "ok",
  "mode": "cluster",
  "brokers": 3,
  "controller": 1
}
```

### Kubernetes

For Kubernetes deployment, use a `StatefulSet` with:
- Unique `broker.id` per pod (use ordinal index + 1)
- `advertised_host` set to the pod's DNS name (e.g., `horizon-0.horizon-headless.default.svc.cluster.local`)
- Seeds pointing to other pods' headless service DNS names
- PersistentVolumeClaims for `/data`

## Failure Scenarios

| Scenario | Behavior |
|----------|----------|
| **Node goes down** | Gossip detects failure after `failure_threshold_ms`. Controller reassigns partitions. Follower replicas on surviving nodes are promoted to leaders. |
| **Controller goes down** | All surviving nodes independently elect the new lowest-ID alive node as controller. New controller recomputes and broadcasts assignments. |
| **Network partition** | Nodes on each side of the partition may elect separate controllers. When the partition heals, gossip merges state and a single controller is re-elected. |
| **Graceful shutdown** | Node sends Leave message via gossip. Controller immediately reassigns partitions without waiting for failure timeout. |
| **New node joins** | Joins via seed gossip. Controller detects new member and redistributes partitions to include the new node. Replicator starts fetch loops for newly assigned follower partitions. |

## Internal Packages

| File | Size | Purpose |
|------|------|---------|
| `internal/cluster/cluster.go` | ~300 lines | Orchestrator, `ClusterRouter` interface implementation |
| `internal/cluster/state.go` | ~350 lines | Types, binary encoding, thread-safe state queries |
| `internal/cluster/gossip.go` | ~250 lines | UDP SWIM-lite membership protocol |
| `internal/cluster/rpc.go` | ~450 lines | TCP RPC server, client pool, message handlers |
| `internal/cluster/controller.go` | ~200 lines | Controller election & partition assignment |
| `internal/cluster/replicator.go` | ~200 lines | Follower fetch loops, offset acknowledgement |

## Wire Protocol

### Gossip (UDP)

```
[1 byte]   message type (ping=1, ack=2, join=3, leave=4)
[4 bytes]  sender node ID
[N bytes]  encoded node list (binary, variable length)
```

### RPC (TCP)

```
[4 bytes]  frame length (big-endian)
[1 byte]   message type (forwardProduce=1, forwardFetch=2, ...)
[4 bytes]  correlation ID (big-endian)
[N bytes]  payload (message-type-specific binary encoding)
```
