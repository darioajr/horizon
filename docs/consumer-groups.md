# Consumer Groups

This document describes the consumer group coordinator implemented in Horizon. The design follows the Apache Kafka consumer group protocol so that standard Kafka clients can join groups, receive partition assignments, commit offsets, and rebalance transparently.

## Overview

The consumer group protocol enables multiple consumers to collaboratively consume partitions of one or more topics. Horizon implements:

- **Group Coordinator** – Manages group membership, generation IDs, and protocol selection.
- **Blocking JoinGroup** – Handler goroutines block until the join phase completes.
- **Blocking SyncGroup** – Follower goroutines block until the leader provides assignments.
- **Session Timeout** – Per-member timers that expire inactive consumers.
- **Rebalance Protocol** – Full state machine from Empty through Stable.
- **Offset Management** – In-memory offset commits per group/topic/partition.
- **Admin APIs** – DescribeGroups, ListGroups, DeleteGroups.

## State Machine

```
                  ┌───────────────┐
        ┌────────▶│     Empty     │◀─── all members leave
        │         └───────┬───────┘     or expire
        │                 │
        │          first JoinGroup
        │                 │
        │                 ▼
        │    ┌────────────────────────┐
        │    │  PreparingRebalance    │◀── member join/leave/expire
        │    │  (waiting for joins)   │    while Stable or
        │    └────────────┬───────────┘    CompletingRebalance
        │                 │
        │    all members joined
        │    OR rebalance timeout
        │                 │
        │                 ▼
        │    ┌────────────────────────┐
        │    │ CompletingRebalance    │
        │    │ (waiting for leader    │
        │    │  SyncGroup)            │
        │    └────────────┬───────────┘
        │                 │
        │       leader sends SyncGroup
        │       with assignments
        │                 │
        │                 ▼
        │         ┌───────────────┐
        │         │    Stable     │── heartbeats keep it here
        │         └───────┬───────┘
        │                 │
        │    member join/leave/expire
        │                 │
        │                 ▼
        │    back to PreparingRebalance
        │
        │         ┌───────────────┐
        └─────────│     Dead      │◀── DeleteGroup / Close
                  └───────────────┘
```

### State Descriptions

| State | Description |
|-------|-------------|
| **Empty** | No members. Group retains committed offsets. |
| **PreparingRebalance** | Coordinator is collecting JoinGroup requests. A rebalance timer is running; when it fires (or all known members rejoin), the join phase completes. |
| **CompletingRebalance** | JoinGroup responses have been sent. The coordinator waits for the leader's SyncGroup to distribute assignments. |
| **Stable** | All members have received assignments and are actively consuming. Heartbeats keep the group in this state. |
| **Dead** | Group has been deleted or shut down. All pending operations receive `GROUP_ID_NOT_FOUND`. |

## Join Protocol

### Flow

```
Consumer A (first member)                 Coordinator
    │                                          │
    │── JoinGroup(memberID="", protocols) ────▶│
    │                                          │ state → PreparingRebalance
    │                                          │ start rebalance timer
    │                                          │
Consumer B                                     │
    │── JoinGroup(memberID="", protocols) ────▶│
    │                                          │ all members joined → complete
    │                                          │ state → CompletingRebalance
    │                                          │ bump generation
    │                                          │ select protocol
    │                                          │ elect leader (alphabetically first)
    │◀──── JoinResult(leader, members=[A,B]) ──│  ← leader gets member list
    │◀──── JoinResult(leader, members=[]) ─────│  ← follower gets empty list
```

### Key Behaviors

1. **Member ID generation**: If a client sends `memberID=""`, the coordinator generates a unique ID using `{clientID}-{timestamp}-{counter}`.

2. **Protocol type consistency**: All members must use the same `protocolType` (e.g., `"consumer"`). A mismatch returns `INCONSISTENT_GROUP_PROTOCOL`.

3. **Protocol selection**: The coordinator picks the first protocol that every member supports (intersection by order of the first member's preferences).

4. **Leader election**: The member with the alphabetically smallest member ID becomes the leader. This is deterministic and consistent.

5. **Rebalance timer**: Starts when transitioning to `PreparingRebalance`. Duration = max `rebalanceTimeoutMs` across all members (default 5 s). If all members rejoin before the timer fires, the phase completes immediately.

6. **Blocking semantics**: The handler goroutine blocks on a buffered channel (`chan JoinResult`). The channel receives the result when `completeJoinPhaseLocked()` runs.

## Sync Protocol

### Flow

```
Leader (Consumer A)                        Coordinator
    │                                          │
    │── SyncGroup(assignments={A:..., B:...}) ▶│
    │                                          │ store assignments
    │                                          │ state → Stable
    │◀──── SyncResult(assignment=A's) ─────────│
    │                                          │
Follower (Consumer B)                          │
    │── SyncGroup(assignments={}) ────────────▶│
    │          (blocks until leader syncs)      │
    │◀──── SyncResult(assignment=B's) ─────────│
```

### Key Behaviors

1. **Leader provides assignments**: The leader calls SyncGroup with a `map[memberID][]byte` of serialized partition assignments.

2. **Followers block**: Follower goroutines block on a `chan SyncResult` until the leader's SyncGroup triggers a state transition to Stable.

3. **Already stable**: If a member calls SyncGroup when the group is already Stable (e.g., reconnect), it immediately receives its stored assignment.

4. **Rebalance in progress**: If SyncGroup is called during `PreparingRebalance`, it returns `REBALANCE_IN_PROGRESS`.

## Session Timeout & Heartbeat

Each member has a **session timer** (`time.AfterFunc`) that fires after `sessionTimeoutMs` milliseconds without a heartbeat.

```
Consumer                    Coordinator
    │                           │
    │── Heartbeat ─────────────▶│ reset timer
    │◀── OK ────────────────────│
    │                           │
    │   (no heartbeat for       │
    │    sessionTimeoutMs)      │
    │                           │ timer fires → expireMember()
    │                           │   remove member
    │                           │   trigger rebalance
```

### Heartbeat Response Codes

| Scenario | Error Code |
|----------|------------|
| Normal operation | `NONE` (0) |
| Group is rebalancing | `REBALANCE_IN_PROGRESS` (27) |
| Member not in group | `UNKNOWN_MEMBER_ID` (25) |
| Wrong generation | `ILLEGAL_GENERATION` (22) |

When a consumer receives `REBALANCE_IN_PROGRESS`, it should immediately re-join the group by sending a new JoinGroup request.

## Leave Group

```
Consumer                    Coordinator
    │                           │
    │── LeaveGroup ────────────▶│ remove member
    │◀── OK ────────────────────│ trigger rebalance if not empty
```

- **v0-v2**: Single member leave (memberID in request body).
- **v3+**: Batch leave with an array of `{memberID, groupInstanceID}` and per-member error codes.
- If the departing member was the leader, a new leader is elected.

## Offset Management

Committed offsets are stored **in memory** per group.

```go
// Structure: group → topic → partition → OffsetAndMetadata
Offsets map[string]map[int32]*OffsetAndMetadata

type OffsetAndMetadata struct {
    Offset         int64
    Metadata       string
    CommitTimestamp int64
}
```

### OffsetCommit (Key 8)

Stores the committed offset for each requested topic-partition.

### OffsetFetch (Key 9)

Returns the last committed offset (or -1 if none) for each requested topic-partition.

> **Note**: Offsets are currently in-memory only. A future enhancement will persist offsets to the storage engine so they survive broker restarts.

## Admin APIs

### DescribeGroups (Key 15)

Returns full state for one or more groups:

```
GroupDescription {
    GroupID      string
    State        string  // "Empty", "PreparingRebalance", "CompletingRebalance", "Stable", "Dead"
    ProtocolType string  // e.g. "consumer"
    Protocol     string  // e.g. "range" or "roundrobin"
    Members []MemberDescription {
        MemberID   string
        ClientID   string
        ClientHost string
        Metadata   []byte  // subscription metadata
        Assignment []byte  // current partition assignment
    }
}
```

### ListGroups (Key 16)

Returns a lightweight list of all known groups:

```
GroupInfo {
    GroupID      string
    ProtocolType string
}
```

### DeleteGroups (Key 42)

Deletes one or more groups. A group must be **Empty** or **Dead** (no active members) to be deleted. Returns `NON_EMPTY_GROUP` (error code 68) otherwise.

## Implementation Details

### Thread Safety

`ConsumerGroup` uses a single `sync.Mutex` (not `RWMutex`) because most operations mutate state. The lock is held for short durations; blocking waits happen **outside** the lock via channels.

### Channel-Based Blocking

```go
// JoinGroup creates a buffered channel, registers it, then blocks:
ch := make(chan JoinResult, 1)
g.pendingJoins[memberID] = ch
g.mu.Unlock()
return <-ch   // blocks until completeJoinPhaseLocked() sends

// completeJoinPhaseLocked() sends to all waiting channels:
for memberID, ch := range g.pendingJoins {
    ch <- JoinResult{...}
}
```

Buffer size 1 ensures the sender never blocks even if the receiver hasn't started listening yet.

### Member Expiry

When a session timer fires:

1. Lock the group mutex.
2. Remove the member (cancel pending joins/syncs).
3. If the group was Stable or CompletingRebalance, transition to PreparingRebalance.
4. If no members remain, transition to Empty.

### Cleanup on Close

`ConsumerGroup.Close()`:
- Sets state to Dead.
- Stops rebalance timer and all session timers.
- Sends error results to all pending join/sync channels to unblock waiting goroutines.
