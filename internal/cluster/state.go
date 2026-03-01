// Package cluster implements cluster membership, partition assignment,
// inter-broker RPC, and replication for Horizon.
//
// When cluster mode is disabled the broker runs as a standalone node and
// none of the code in this package is loaded.
package cluster

import (
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Node state
// ---------------------------------------------------------------------------

// NodeState describes the lifecycle state of a cluster node.
type NodeState uint8

const (
	NodeAlive   NodeState = iota // healthy and reachable
	NodeSuspect                  // missed heartbeats, may be dead
	NodeDead                     // confirmed unreachable
	NodeLeft                     // gracefully left the cluster
)

func (s NodeState) String() string {
	switch s {
	case NodeAlive:
		return "alive"
	case NodeSuspect:
		return "suspect"
	case NodeDead:
		return "dead"
	case NodeLeft:
		return "left"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// NodeInfo
// ---------------------------------------------------------------------------

// NodeInfo describes a single node in the cluster.
type NodeInfo struct {
	ID         int32
	Host       string // advertised hostname / IP for clients
	KafkaPort  int32  // Kafka protocol TCP port
	RPCPort    int32  // inter-broker RPC TCP port (gossip shares the same number via UDP)
	HTTPPort   int32  // HTTP gateway port (0 = disabled)
	State      NodeState
	LastSeen   time.Time
	Generation int64 // bumped on each restart of the same node ID
}

// clone returns a deep copy.
func (n *NodeInfo) clone() *NodeInfo {
	c := *n
	return &c
}

// ---------------------------------------------------------------------------
// PartitionAssignment
// ---------------------------------------------------------------------------

// PartitionAssignment describes who owns a topic-partition.
type PartitionAssignment struct {
	Topic       string
	Partition   int32
	Leader      int32   // node ID of leader (-1 = no leader)
	Replicas    []int32 // ordered replica set (includes leader)
	ISR         []int32 // in-sync replicas
	LeaderEpoch int32   // bumped on each leader change
}

func (a *PartitionAssignment) clone() *PartitionAssignment {
	c := *a
	c.Replicas = append([]int32(nil), a.Replicas...)
	c.ISR = append([]int32(nil), a.ISR...)
	return &c
}

// ---------------------------------------------------------------------------
// ClusterState  – the global view of the cluster held by every node
// ---------------------------------------------------------------------------

// ClusterState holds the full cluster state visible to this node.
type ClusterState struct {
	mu           sync.RWMutex
	localNodeID  int32
	nodes        map[int32]*NodeInfo
	assignments  map[string]map[int32]*PartitionAssignment // topic → partition → assignment
	controllerID int32
	version      int64 // monotonically increasing epoch
}

// NewClusterState creates a new empty state for a given local node.
func NewClusterState(localNodeID int32) *ClusterState {
	return &ClusterState{
		localNodeID:  localNodeID,
		nodes:        make(map[int32]*NodeInfo),
		assignments:  make(map[string]map[int32]*PartitionAssignment),
		controllerID: -1,
	}
}

// ---------------------------------------------------------------------------
// Node queries
// ---------------------------------------------------------------------------

// GetNode returns a copy of the requested node (nil if unknown).
func (cs *ClusterState) GetNode(id int32) *NodeInfo {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if n, ok := cs.nodes[id]; ok {
		return n.clone()
	}
	return nil
}

// AliveNodes returns a sorted snapshot of all alive nodes.
func (cs *ClusterState) AliveNodes() []*NodeInfo {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]*NodeInfo, 0, len(cs.nodes))
	for _, n := range cs.nodes {
		if n.State == NodeAlive {
			out = append(out, n.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AllNodes returns every node regardless of state.
func (cs *ClusterState) AllNodes() []*NodeInfo {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]*NodeInfo, 0, len(cs.nodes))
	for _, n := range cs.nodes {
		out = append(out, n.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SetNode inserts or updates a node in the state.
func (cs *ClusterState) SetNode(n *NodeInfo) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.nodes[n.ID] = n.clone()
}

// MarkNode changes the state of a node.
func (cs *ClusterState) MarkNode(id int32, state NodeState) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if n, ok := cs.nodes[id]; ok {
		n.State = state
	}
}

// RemoveNode removes a node.
func (cs *ClusterState) RemoveNode(id int32) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.nodes, id)
}

// ---------------------------------------------------------------------------
// Partition queries
// ---------------------------------------------------------------------------

// IsPartitionLocal returns true if this node is the leader for the partition.
// If no assignment exists the partition is treated as local (standalone fallback).
func (cs *ClusterState) IsPartitionLocal(topic string, partition int32) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if parts, ok := cs.assignments[topic]; ok {
		if a, ok := parts[partition]; ok {
			return a.Leader == cs.localNodeID
		}
	}
	return true // no assignment → treat as local
}

// GetPartitionLeader returns the leader node info for a partition.
func (cs *ClusterState) GetPartitionLeader(topic string, partition int32) (*NodeInfo, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if parts, ok := cs.assignments[topic]; ok {
		if a, ok := parts[partition]; ok {
			if node, ok := cs.nodes[a.Leader]; ok && node.State == NodeAlive {
				return node.clone(), true
			}
		}
	}
	return nil, false
}

// GetAssignment returns a copy of the assignment for a topic-partition.
func (cs *ClusterState) GetAssignment(topic string, partition int32) *PartitionAssignment {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if parts, ok := cs.assignments[topic]; ok {
		if a, ok := parts[partition]; ok {
			return a.clone()
		}
	}
	return nil
}

// GetTopicAssignments returns all assignments for a topic.
func (cs *ClusterState) GetTopicAssignments(topic string) []*PartitionAssignment {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	parts, ok := cs.assignments[topic]
	if !ok {
		return nil
	}
	out := make([]*PartitionAssignment, 0, len(parts))
	for _, a := range parts {
		out = append(out, a.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Partition < out[j].Partition })
	return out
}

// SetAssignments replaces the full partition assignment table.
func (cs *ClusterState) SetAssignments(table map[string]map[int32]*PartitionAssignment, controllerID int32, version int64) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.assignments = table
	cs.controllerID = controllerID
	cs.version = version
}

// SetSingleAssignment sets or updates one topic-partition assignment.
func (cs *ClusterState) SetSingleAssignment(a *PartitionAssignment) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.assignments[a.Topic] == nil {
		cs.assignments[a.Topic] = make(map[int32]*PartitionAssignment)
	}
	cs.assignments[a.Topic][a.Partition] = a.clone()
}

// RemoveTopicAssignments removes all assignments for a topic.
func (cs *ClusterState) RemoveTopicAssignments(topic string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.assignments, topic)
}

// ControllerID returns the current controller node ID.
func (cs *ClusterState) ControllerID() int32 {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.controllerID
}

// SetControllerID sets the controller.
func (cs *ClusterState) SetControllerID(id int32) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.controllerID = id
}

// Version returns the cluster state version.
func (cs *ClusterState) Version() int64 {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.version
}

// BumpVersion atomically increments the version.
func (cs *ClusterState) BumpVersion() int64 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.version++
	return cs.version
}

// IsController returns true if this node is the controller.
func (cs *ClusterState) IsController() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.controllerID == cs.localNodeID
}

// ---------------------------------------------------------------------------
// TopicPartitionCount returns the number of assigned partitions for a topic.
func (cs *ClusterState) TopicPartitionCount(topic string) int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return len(cs.assignments[topic])
}

// LocalLeaderPartitions returns all topic-partitions this node leads.
func (cs *ClusterState) LocalLeaderPartitions() []*PartitionAssignment {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	var out []*PartitionAssignment
	for _, parts := range cs.assignments {
		for _, a := range parts {
			if a.Leader == cs.localNodeID {
				out = append(out, a.clone())
			}
		}
	}
	return out
}

// LocalFollowerPartitions returns all topic-partitions this node replicates but does not lead.
func (cs *ClusterState) LocalFollowerPartitions() []*PartitionAssignment {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	var out []*PartitionAssignment
	for _, parts := range cs.assignments {
		for _, a := range parts {
			if a.Leader == cs.localNodeID {
				continue
			}
			for _, r := range a.Replicas {
				if r == cs.localNodeID {
					out = append(out, a.clone())
					break
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Binary encoding for gossip messages (node list)
// ---------------------------------------------------------------------------

// EncodeNodeList serialises a list of NodeInfo into a binary blob.
func EncodeNodeList(nodes []*NodeInfo) []byte {
	// Calculate total size
	size := 4 // node count
	for _, n := range nodes {
		size += 4 + 2 + len(n.Host) + 4 + 4 + 4 + 1 + 8 + 8 // see layout below
	}
	buf := make([]byte, size)
	off := 0
	binary.BigEndian.PutUint32(buf[off:], uint32(len(nodes)))
	off += 4
	for _, n := range nodes {
		binary.BigEndian.PutUint32(buf[off:], uint32(n.ID))
		off += 4
		binary.BigEndian.PutUint16(buf[off:], uint16(len(n.Host)))
		off += 2
		copy(buf[off:], n.Host)
		off += len(n.Host)
		binary.BigEndian.PutUint32(buf[off:], uint32(n.KafkaPort))
		off += 4
		binary.BigEndian.PutUint32(buf[off:], uint32(n.RPCPort))
		off += 4
		binary.BigEndian.PutUint32(buf[off:], uint32(n.HTTPPort))
		off += 4
		buf[off] = byte(n.State)
		off++
		binary.BigEndian.PutUint64(buf[off:], uint64(n.LastSeen.UnixMilli()))
		off += 8
		binary.BigEndian.PutUint64(buf[off:], uint64(n.Generation))
		off += 8
	}
	return buf[:off]
}

// DecodeNodeList reads back a binary-encoded node list.
func DecodeNodeList(data []byte) ([]*NodeInfo, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("node list too short")
	}
	count := int(binary.BigEndian.Uint32(data[0:4]))
	off := 4
	nodes := make([]*NodeInfo, 0, count)
	for i := 0; i < count; i++ {
		if off+4+2 > len(data) {
			return nil, fmt.Errorf("truncated node entry %d", i)
		}
		n := &NodeInfo{}
		n.ID = int32(binary.BigEndian.Uint32(data[off:]))
		off += 4
		hostLen := int(binary.BigEndian.Uint16(data[off:]))
		off += 2
		if off+hostLen+4+4+4+1+8+8 > len(data) {
			return nil, fmt.Errorf("truncated node entry %d", i)
		}
		n.Host = string(data[off : off+hostLen])
		off += hostLen
		n.KafkaPort = int32(binary.BigEndian.Uint32(data[off:]))
		off += 4
		n.RPCPort = int32(binary.BigEndian.Uint32(data[off:]))
		off += 4
		n.HTTPPort = int32(binary.BigEndian.Uint32(data[off:]))
		off += 4
		n.State = NodeState(data[off])
		off++
		n.LastSeen = time.UnixMilli(int64(binary.BigEndian.Uint64(data[off:])))
		off += 8
		n.Generation = int64(binary.BigEndian.Uint64(data[off:]))
		off += 8
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// ---------------------------------------------------------------------------
// Binary encoding for partition assignment table
// ---------------------------------------------------------------------------

// EncodeAssignments serialises the full assignment table.
func EncodeAssignments(table map[string]map[int32]*PartitionAssignment, controllerID int32, version int64) []byte {
	// Count total assignments
	total := 0
	for _, parts := range table {
		total += len(parts)
	}
	// Estimate size: header(16) + per-entry(2+topic+4+4+4+4+replicas+isrs)
	buf := make([]byte, 0, 16+total*64)
	hdr := make([]byte, 16)
	binary.BigEndian.PutUint32(hdr[0:], uint32(controllerID))
	binary.BigEndian.PutUint64(hdr[4:], uint64(version))
	binary.BigEndian.PutUint32(hdr[12:], uint32(total))
	buf = append(buf, hdr...)

	for _, parts := range table {
		for _, a := range parts {
			entry := encodeAssignment(a)
			buf = append(buf, entry...)
		}
	}
	return buf
}

func encodeAssignment(a *PartitionAssignment) []byte {
	size := 2 + len(a.Topic) + 4 + 4 + 4 + 4 + len(a.Replicas)*4 + 4 + len(a.ISR)*4
	buf := make([]byte, size)
	off := 0
	binary.BigEndian.PutUint16(buf[off:], uint16(len(a.Topic)))
	off += 2
	copy(buf[off:], a.Topic)
	off += len(a.Topic)
	binary.BigEndian.PutUint32(buf[off:], uint32(a.Partition))
	off += 4
	binary.BigEndian.PutUint32(buf[off:], uint32(a.Leader))
	off += 4
	binary.BigEndian.PutUint32(buf[off:], uint32(a.LeaderEpoch))
	off += 4
	binary.BigEndian.PutUint32(buf[off:], uint32(len(a.Replicas)))
	off += 4
	for _, r := range a.Replicas {
		binary.BigEndian.PutUint32(buf[off:], uint32(r))
		off += 4
	}
	binary.BigEndian.PutUint32(buf[off:], uint32(len(a.ISR)))
	off += 4
	for _, r := range a.ISR {
		binary.BigEndian.PutUint32(buf[off:], uint32(r))
		off += 4
	}
	return buf[:off]
}

// DecodeAssignments reads back a binary-encoded assignment table.
func DecodeAssignments(data []byte) (table map[string]map[int32]*PartitionAssignment, controllerID int32, version int64, err error) {
	if len(data) < 16 {
		return nil, 0, 0, fmt.Errorf("assignment data too short")
	}
	controllerID = int32(binary.BigEndian.Uint32(data[0:]))
	version = int64(binary.BigEndian.Uint64(data[4:]))
	total := int(binary.BigEndian.Uint32(data[12:]))
	off := 16
	table = make(map[string]map[int32]*PartitionAssignment)
	for i := 0; i < total; i++ {
		a, n, e := decodeAssignment(data[off:])
		if e != nil {
			return nil, 0, 0, fmt.Errorf("assignment %d: %w", i, e)
		}
		off += n
		if table[a.Topic] == nil {
			table[a.Topic] = make(map[int32]*PartitionAssignment)
		}
		table[a.Topic][a.Partition] = a
	}
	return table, controllerID, version, nil
}

func decodeAssignment(data []byte) (*PartitionAssignment, int, error) {
	if len(data) < 2 {
		return nil, 0, fmt.Errorf("truncated")
	}
	a := &PartitionAssignment{}
	off := 0
	topicLen := int(binary.BigEndian.Uint16(data[off:]))
	off += 2
	if off+topicLen+4+4+4+4 > len(data) {
		return nil, 0, fmt.Errorf("truncated")
	}
	a.Topic = string(data[off : off+topicLen])
	off += topicLen
	a.Partition = int32(binary.BigEndian.Uint32(data[off:]))
	off += 4
	a.Leader = int32(binary.BigEndian.Uint32(data[off:]))
	off += 4
	a.LeaderEpoch = int32(binary.BigEndian.Uint32(data[off:]))
	off += 4
	repCount := int(binary.BigEndian.Uint32(data[off:]))
	off += 4
	if off+repCount*4+4 > len(data) {
		return nil, 0, fmt.Errorf("truncated replicas")
	}
	a.Replicas = make([]int32, repCount)
	for i := range a.Replicas {
		a.Replicas[i] = int32(binary.BigEndian.Uint32(data[off:]))
		off += 4
	}
	isrCount := int(binary.BigEndian.Uint32(data[off:]))
	off += 4
	if off+isrCount*4 > len(data) {
		return nil, 0, fmt.Errorf("truncated isr")
	}
	a.ISR = make([]int32, isrCount)
	for i := range a.ISR {
		a.ISR[i] = int32(binary.BigEndian.Uint32(data[off:]))
		off += 4
	}
	return a, off, nil
}
