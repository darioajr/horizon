// Package cluster implements cluster membership, controller election,
// partition assignment, inter-broker RPC, and replication for Horizon.
//
// # Architecture overview
//
//   - **Gossip** (UDP) — SWIM-lite protocol for node discovery and failure
//     detection.  Every node periodically pings a random subset of peers and
//     merges the returned node list.
//
//   - **Controller** — the alive node with the lowest node ID is
//     deterministically elected.  It computes partition assignments (round-robin
//     with replicas) and broadcasts them via RPC.
//
//   - **RPC** (TCP) — length-prefixed binary protocol for produce/fetch
//     forwarding and replication traffic.
//
//   - **Replicator** — follower nodes run a fetch loop per partition they
//     replicate, pulling data from the leader and writing it to local storage.
//
// The Cluster struct implements broker.ClusterRouter so the broker can route
// produce/fetch requests to the correct node without importing this package
// directly.
package cluster

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"log"
	"sync/atomic"
	"time"

	"horizon/internal/broker"
	"horizon/internal/storage"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config configures the cluster subsystem.
type Config struct {
	NodeID            int32
	Host              string // advertised host for clients
	KafkaPort         int32
	RPCPort           int32
	HTTPPort          int32 // 0 = HTTP gateway disabled
	Seeds             []string
	GossipInterval    time.Duration
	FailureThreshold  time.Duration
	ReplicationFactor int16
}

// ---------------------------------------------------------------------------
// Cluster
// ---------------------------------------------------------------------------

// Cluster coordinates all cluster-level functionality.
type Cluster struct {
	cfg         Config
	state       *ClusterState
	localBroker *broker.Broker
	gossip      *Gossip
	rpcServer   *RPCServer
	rpcPool     *RPCPool
	replicator  *Replicator
	rrCounter   atomic.Int64

	running atomic.Bool
}

// New creates a new cluster coordinator.
func New(cfg Config, b *broker.Broker) *Cluster {
	state := NewClusterState(cfg.NodeID)

	c := &Cluster{
		cfg:         cfg,
		state:       state,
		localBroker: b,
	}
	c.rpcPool = NewRPCPool(state)
	c.rpcServer = NewRPCServer(c)
	c.replicator = NewReplicator(c)
	return c
}

// Start boots up gossip, RPC, and replicator.
func (c *Cluster) Start() error {
	if c.running.Load() {
		return nil
	}

	// Register this node in the state
	self := &NodeInfo{
		ID:        c.cfg.NodeID,
		Host:      c.cfg.Host,
		KafkaPort: c.cfg.KafkaPort,
		RPCPort:   c.cfg.RPCPort,
		HTTPPort:  c.cfg.HTTPPort,
		State:     NodeAlive,
		LastSeen:  time.Now(),
		Generation: time.Now().UnixMilli(),
	}

	// Start RPC server (TCP)
	bindRPC := fmt.Sprintf("0.0.0.0:%d", c.cfg.RPCPort)
	if err := c.rpcServer.Start(bindRPC); err != nil {
		return fmt.Errorf("cluster rpc: %w", err)
	}
	log.Printf("[cluster] RPC server listening on %s", bindRPC)

	// Start gossip (UDP)
	bindGossip := fmt.Sprintf("0.0.0.0:%d", c.cfg.RPCPort)
	gcfg := GossipConfig{
		BindAddr:         bindGossip,
		Seeds:            c.cfg.Seeds,
		Interval:         c.cfg.GossipInterval,
		FailureThreshold: c.cfg.FailureThreshold,
		OnMemberChange:   c.onMemberChange,
	}
	c.gossip = NewGossip(c.state, self, gcfg)
	if err := c.gossip.Start(bindGossip); err != nil {
		c.rpcServer.Stop()
		return fmt.Errorf("cluster gossip: %w", err)
	}
	log.Printf("[cluster] gossip started on %s, seeds=%v", bindGossip, c.cfg.Seeds)

	// Start replicator
	c.replicator.Start()

	// Initial controller election (may be the only node)
	c.electController()

	c.running.Store(true)
	log.Printf("[cluster] node %d started (host=%s kafka=%d rpc=%d)",
		c.cfg.NodeID, c.cfg.Host, c.cfg.KafkaPort, c.cfg.RPCPort)
	return nil
}

// Stop gracefully shuts down the cluster layer.
func (c *Cluster) Stop() {
	if !c.running.Load() {
		return
	}
	c.running.Store(false)
	c.replicator.Stop()
	c.gossip.Stop()
	c.rpcServer.Stop()
	c.rpcPool.CloseAll()
	log.Printf("[cluster] node %d stopped", c.cfg.NodeID)
}

// State returns the underlying cluster state (read-only by convention).
func (c *Cluster) State() *ClusterState { return c.state }

// ---------------------------------------------------------------------------
// broker.ClusterRouter implementation
// ---------------------------------------------------------------------------

// IsPartitionLocal returns true if this node is the leader for the partition.
func (c *Cluster) IsPartitionLocal(topic string, partition int32) bool {
	return c.state.IsPartitionLocal(topic, partition)
}

// GetPartitionLeader returns the leader for a partition.
func (c *Cluster) GetPartitionLeader(topic string, partition int32) (nodeID int32, host string, port int32, ok bool) {
	n, found := c.state.GetPartitionLeader(topic, partition)
	if !found {
		return 0, "", 0, false
	}
	return n.ID, n.Host, n.KafkaPort, true
}

// ForwardProduce sends a produce request to the leader of a partition.
func (c *Cluster) ForwardProduce(nodeID int32, topic string, partition int32, data []byte, recordCount int32, maxTs int64) (int64, error) {
	client, err := c.rpcPool.Get(nodeID)
	if err != nil {
		return 0, fmt.Errorf("forward produce: %w", err)
	}
	payload := encodeProducePayload(topic, partition, data, recordCount, maxTs)
	resp, err := client.Call(rpcForwardProduce, payload)
	if err != nil {
		c.rpcPool.Remove(nodeID)
		return 0, fmt.Errorf("forward produce rpc: %w", err)
	}
	if len(resp) < 9 {
		return 0, fmt.Errorf("forward produce: short response")
	}
	if resp[0] != rpcErrNone {
		return 0, fmt.Errorf("forward produce: remote error %d", resp[0])
	}
	return int64(binary.BigEndian.Uint64(resp[1:])), nil
}

// ForwardFetch sends a fetch request to the partition leader.
func (c *Cluster) ForwardFetch(nodeID int32, topic string, partition int32, offset int64, maxBytes int64) ([]*storage.RecordBatch, error) {
	client, err := c.rpcPool.Get(nodeID)
	if err != nil {
		return nil, fmt.Errorf("forward fetch: %w", err)
	}
	payload := encodeFetchPayload(topic, partition, offset, maxBytes)
	resp, err := client.Call(rpcForwardFetch, payload)
	if err != nil {
		c.rpcPool.Remove(nodeID)
		return nil, fmt.Errorf("forward fetch rpc: %w", err)
	}
	if len(resp) < 5 || resp[0] != rpcErrNone {
		return nil, fmt.Errorf("forward fetch: remote error")
	}
	batchCount := int(binary.BigEndian.Uint32(resp[1:5]))
	off := 5
	var batches []*storage.RecordBatch
	for i := 0; i < batchCount; i++ {
		if off+4 > len(resp) {
			break
		}
		batchLen := int(binary.BigEndian.Uint32(resp[off:]))
		off += 4
		if off+batchLen > len(resp) {
			break
		}
		batch, err := storage.DecodeRecordBatch(resp[off : off+batchLen])
		if err == nil {
			batches = append(batches, batch)
		}
		off += batchLen
	}
	return batches, nil
}

// GetClusterBrokers returns all alive nodes as BrokerInfo.
func (c *Cluster) GetClusterBrokers() []broker.BrokerInfo {
	alive := c.state.AliveNodes()
	out := make([]broker.BrokerInfo, len(alive))
	for i, n := range alive {
		out[i] = broker.BrokerInfo{
			NodeID: n.ID,
			Host:   n.Host,
			Port:   n.KafkaPort,
		}
	}
	return out
}

// GetControllerID returns the current controller node ID.
func (c *Cluster) GetControllerID() int32 {
	return c.state.ControllerID()
}

// GetPartitionAssignment returns replica and ISR info for a partition.
func (c *Cluster) GetPartitionAssignment(topic string, partition int32) (replicas []int32, isr []int32, leaderEpoch int32) {
	a := c.state.GetAssignment(topic, partition)
	if a == nil {
		return nil, nil, 0
	}
	return a.Replicas, a.ISR, a.LeaderEpoch
}

// NotifyTopicCreated tells the cluster that a new topic was created.
// If this node is the controller, it computes assignments immediately.
func (c *Cluster) NotifyTopicCreated(topic string, numPartitions int32, replicationFactor int16) {
	if c.state.IsController() {
		c.computeAndBroadcastAssignments()
	}
}

// NotifyTopicDeleted tells the cluster that a topic was deleted.
func (c *Cluster) NotifyTopicDeleted(topic string) {
	c.state.RemoveTopicAssignments(topic)
	if c.state.IsController() {
		c.computeAndBroadcastAssignments()
	}
}

// ---------------------------------------------------------------------------
// Auto-partition (cluster-aware)
// ---------------------------------------------------------------------------

// ProduceAutoPartition selects a partition (hash or round-robin), routes to
// the leader, and returns the partition number and base offset.
func (c *Cluster) ProduceAutoPartition(topic string, key []byte, records []storage.Record) (partition int32, baseOffset int64, err error) {
	// Determine number of partitions
	numPartitions := int32(c.state.TopicPartitionCount(topic))
	if numPartitions <= 0 {
		// Fallback: query local broker
		if tc := c.localBroker.GetTopicConfig(topic); tc != nil {
			numPartitions = tc.NumPartitions
		}
	}
	if numPartitions <= 0 {
		numPartitions = 1
	}

	// Select partition
	if key != nil && len(key) > 0 {
		h := crc32.ChecksumIEEE(key)
		partition = int32(h % uint32(numPartitions))
	} else {
		n := c.rrCounter.Add(1)
		partition = int32(n % int64(numPartitions))
	}

	// Route to leader
	if c.state.IsPartitionLocal(topic, partition) {
		baseOffset, err = c.localBroker.Produce(topic, partition, records)
		return
	}

	leaderNode, ok := c.state.GetPartitionLeader(topic, partition)
	if !ok {
		// No leader known — try local
		baseOffset, err = c.localBroker.Produce(topic, partition, records)
		return
	}

	// Encode records into a batch, forward via RPC
	batch := storage.NewRecordBatch(0, records)
	data := batch.Encode()
	recordCount := int32(len(records))
	maxTs := batch.MaxTimestamp

	baseOffset, err = c.ForwardProduce(leaderNode.ID, topic, partition, data, recordCount, maxTs)
	return
}
