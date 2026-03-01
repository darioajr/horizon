# Kafka Protocol Implementation

This document describes how Horizon implements the Apache Kafka wire protocol.

## Wire Format

Horizon speaks the **Kafka binary protocol** over TCP (default port 9092). Every message on the wire follows the same framing:

```
┌──────────┬────────────────────────────────────┐
│ int32    │  Payload                           │
│ (size)   │  (size bytes)                      │
└──────────┴────────────────────────────────────┘
```

### Request Header

```
┌────────────┬────────────┬────────────────┬──────────────┐
│ int16      │ int16      │ int32          │ nullable     │
│ api_key    │ api_version│ correlation_id │ client_id    │
└────────────┴────────────┴────────────────┴──────────────┘
```

### Response Header

```
┌────────────────┬──────────────────┐
│ int32          │ response body    │
│ correlation_id │ (api-specific)   │
└────────────────┴──────────────────┘
```

## Encoding Primitives

The `internal/protocol` package provides a `Reader` and `Writer` that implement:

| Type | Wire Size | Go Type | Description |
|------|-----------|---------|-------------|
| `int8` | 1 byte | `int8` | Signed 8-bit integer |
| `int16` | 2 bytes | `int16` | Signed 16-bit big-endian |
| `int32` | 4 bytes | `int32` | Signed 32-bit big-endian |
| `int64` | 8 bytes | `int64` | Signed 64-bit big-endian |
| `string` | 2 + N bytes | `string` | int16 length prefix + UTF-8 bytes |
| `nullable_string` | 2 + N bytes | `*string` | int16 length (-1 = null) + UTF-8 bytes |
| `bytes` | 4 + N bytes | `[]byte` | int32 length prefix + raw bytes |
| `array` | 4 + N×elem | `[]T` | int32 element count + elements |

> **Note:** Horizon currently supports the "classic" (non-flexible) encoding. Flexible versions (which use compact arrays and tagged fields) are intentionally kept below the flexible threshold for each API.

## Supported APIs

### Overview

| API | Key | Min Version | Max Version | Handler |
|-----|-----|-------------|-------------|---------|
| Produce | 0 | 0 | 8 | `handler_produce.go` |
| Fetch | 1 | 0 | 11 | `handler_fetch.go` |
| ListOffsets | 2 | 0 | 5 | `handler_offset.go` |
| Metadata | 3 | 0 | 8 | `handler_metadata.go` |
| OffsetCommit | 8 | 0 | 7 | `handler_offset.go` |
| OffsetFetch | 9 | 0 | 7 | `handler_offset.go` |
| FindCoordinator | 10 | 0 | 3 | `handler_group.go` |
| JoinGroup | 11 | 0 | 6 | `handler_group.go` |
| Heartbeat | 12 | 0 | 3 | `handler_group.go` |
| LeaveGroup | 13 | 0 | 4 | `handler_group.go` |
| SyncGroup | 14 | 0 | 4 | `handler_group.go` |
| DescribeGroups | 15 | 0 | 4 | `handler_group.go` |
| ListGroups | 16 | 0 | 3 | `handler_group.go` |
| ApiVersions | 18 | 0 | 2 | `handler_metadata.go` |
| CreateTopics | 19 | 0 | 4 | `handler_topic.go` |
| DeleteTopics | 20 | 0 | 4 | `handler_topic.go` |
| InitProducerId | 22 | 0 | 2 | `handler_init_producer.go` |
| DeleteGroups | 42 | 0 | 1 | `handler_group.go` |

### API Details

#### ApiVersions (Key 18)

Returns the list of all supported APIs and their version ranges. This is the first request a client sends to negotiate features.

- Versions 0-2 supported (v3+ uses flexible encoding, not supported).
- The response also includes throttle time for v1+.

#### Produce (Key 0)

Writes records to a topic-partition.

- **v0-v2**: Basic produce with acks.
- **v3+**: Adds transactional ID field (read and ignored).
- **v5+**: Adds record batch first timestamp.
- The handler calls `Broker.Produce()` which delegates to `StorageEngine.AppendRaw()` for the zero-copy fast path.

#### Fetch (Key 1)

Reads records from topic-partitions starting at a given offset.

- **v0-v3**: Basic fetch.
- **v4+**: Adds isolation level.
- **v7+**: Adds session ID and epoch.
- Returns `highWatermark`, `logStartOffset`, and record batches.

#### JoinGroup (Key 11)

Adds a member to a consumer group and blocks until the join phase completes.

- **v0**: Basic join.
- **v1+**: Adds `rebalance_timeout_ms`.
- **v5+**: Adds `group_instance_id` for static membership.
- **v7+**: Adds `protocol_type` in response (not supported – kept below v7).
- The handler goroutine **blocks** until all members have joined or the rebalance timeout fires.

#### SyncGroup (Key 14)

The leader provides partition assignments; followers block until assignments are ready.

- **v0-v2**: Basic sync.
- **v3+**: Adds `group_instance_id`.
- **v5+**: Adds `protocol_type`/`protocol_name` in request/response.
- Followers' handler goroutines block on a channel until the leader's SyncGroup call distributes assignments.

#### Heartbeat (Key 12)

Keeps a member's session alive. Returns `REBALANCE_IN_PROGRESS` if the group is rebalancing.

#### LeaveGroup (Key 13)

Removes a member from the group.

- **v0-v2**: Single member leave.
- **v3+**: Batch leave with member array and per-member error codes.

#### DescribeGroups (Key 15)

Returns detailed information about one or more groups including state, members, and their assignments.

#### ListGroups (Key 16)

Returns a list of all groups with their protocol type.

#### DeleteGroups (Key 42)

Deletes one or more empty groups. Returns `NON_EMPTY_GROUP` (error code 68) if a group still has active members.

## Error Codes

Horizon uses standard Kafka error codes as defined in `internal/protocol/types.go`:

| Code | Name | When Returned |
|------|------|---------------|
| 0 | NONE | Success |
| -1 | UNKNOWN_SERVER_ERROR | Unexpected internal error |
| 3 | UNKNOWN_TOPIC_OR_PARTITION | Topic or partition does not exist |
| 22 | ILLEGAL_GENERATION | Generation ID in request doesn't match group |
| 23 | INCONSISTENT_GROUP_PROTOCOL | Member's protocol type doesn't match group |
| 25 | UNKNOWN_MEMBER_ID | Member ID not found in group |
| 27 | REBALANCE_IN_PROGRESS | Group is rebalancing |
| 35 | UNSUPPORTED_VERSION | API version not supported |
| 36 | TOPIC_ALREADY_EXISTS | Topic already exists on create |
| 68 | NON_EMPTY_GROUP | Cannot delete group with active members |
| 69 | GROUP_ID_NOT_FOUND | Group does not exist |

## Version Negotiation

Kafka clients typically follow this flow:

1. Send `ApiVersions` request (always v0 for maximum compatibility).
2. Receive the supported version ranges.
3. For each subsequent API call, use `min(client_max, server_max)`.

Horizon advertises conservative max versions to stay below the "flexible versions" threshold of each API, avoiding the need for compact array / tagged field encoding.

## Connection Model

```
Client TCP Connection
        │
        ▼
  ┌─────────────────┐
  │   Connection    │  (1 goroutine per connection)
  │   Handle() loop │
  │                 │  Sequential: read → dispatch → write → read → …
  │  bufio.Reader   │  64 KB read buffer
  │  bufio.Writer   │  64 KB write buffer
  │  sync.Pool buf  │  1 MB request buffer pool
  └─────────────────┘
```

Each connection is handled by a single goroutine that processes requests sequentially. This matches Kafka's per-connection ordering guarantee and avoids the complexity of out-of-order response handling.

**Blocking APIs** (JoinGroup, SyncGroup) block the connection's goroutine. This is intentional – the client expects to wait for the coordinator's response, and other connections are unaffected since each has its own goroutine.
