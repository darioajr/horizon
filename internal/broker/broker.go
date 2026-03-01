// Package broker implements the Horizon message broker.
// It handles Kafka protocol requests and coordinates storage.
package broker

import (
	"fmt"
	"hash/crc32"
	"sync"
	"sync/atomic"
	"time"

	"horizon/internal/storage"
)

// BrokerConfig holds configuration for the broker
type BrokerConfig struct {
	// NodeID is the unique identifier for this broker
	NodeID int32

	// Host is the bind host
	Host string

	// AdvertisedHost is the host advertised to clients (defaults to Host)
	AdvertisedHost string

	// Port is the advertised port
	Port int32

	// DataDir is the directory for data storage
	DataDir string

	// LogRetentionBytes is the max size of log before cleanup
	LogRetentionBytes int64

	// LogRetentionMs is the max age of log before cleanup
	LogRetentionMs int64

	// SegmentBytes is the max size of a segment
	SegmentBytes int64

	// DefaultNumPartitions for auto-created topics
	DefaultNumPartitions int32

	// DefaultReplicationFactor for auto-created topics
	DefaultReplicationFactor int16

	// AutoCreateTopics enables auto topic creation
	AutoCreateTopics bool
}

// DefaultBrokerConfig returns default broker configuration
func DefaultBrokerConfig() BrokerConfig {
	return BrokerConfig{
		NodeID:                   0,
		Host:                     "localhost",
		Port:                     9092,
		DataDir:                  "./data",
		LogRetentionBytes:        -1, // unlimited
		LogRetentionMs:           7 * 24 * 60 * 60 * 1000, // 7 days
		SegmentBytes:             1024 * 1024 * 1024, // 1GB
		DefaultNumPartitions:     1,
		DefaultReplicationFactor: 1,
		AutoCreateTopics:         true,
	}
}

// ClusterRouter is an optional interface implemented by the cluster layer.
// When set on the broker, produce/fetch and metadata operations become
// cluster-aware (checking partition leadership, forwarding to remote nodes,
// returning all brokers in metadata responses, etc.).
//
// The interface lives in the broker package so the broker can use it without
// importing the cluster package (avoiding circular imports).
type ClusterRouter interface {
	// Partition routing
	IsPartitionLocal(topic string, partition int32) bool
	GetPartitionLeader(topic string, partition int32) (nodeID int32, host string, port int32, ok bool)

	// Forwarding
	ForwardProduce(nodeID int32, topic string, partition int32, data []byte, recordCount int32, maxTs int64) (int64, error)
	ForwardFetch(nodeID int32, topic string, partition int32, offset int64, maxBytes int64) ([]*storage.RecordBatch, error)

	// Cluster-wide metadata
	GetClusterBrokers() []BrokerInfo
	GetControllerID() int32
	GetPartitionAssignment(topic string, partition int32) (replicas []int32, isr []int32, leaderEpoch int32)

	// Notifications
	NotifyTopicCreated(topic string, numPartitions int32, replicationFactor int16)
	NotifyTopicDeleted(topic string)
}

// Broker is the main message broker
type Broker struct {
	mu sync.RWMutex

	config BrokerConfig

	// Storage layer (pluggable: file, S3, Redis, Infinispan, …)
	log storage.StorageEngine

	// Topic configurations
	topicConfigs map[string]*TopicConfig

	// Consumer group manager
	groupManager *GroupManager

	// Optional cluster router (nil in standalone mode)
	cluster ClusterRouter

	// Cluster metadata version
	metadataVersion int32

	// Round-robin counter for keyless partition assignment
	rrCounter atomic.Int64

	// Whether broker is running
	running bool

	// Shutdown channel
	shutdownCh chan struct{}
}

// TopicConfig holds configuration for a topic
type TopicConfig struct {
	Name              string
	NumPartitions     int32
	ReplicationFactor int16
	RetentionMs       int64
	RetentionBytes    int64
	CleanupPolicy     string // "delete" or "compact"
}

// New creates a new broker instance.
// If engine is nil the default file-based storage is created from config.DataDir.
func New(config BrokerConfig, engine ...storage.StorageEngine) (*Broker, error) {
	var log storage.StorageEngine

	if len(engine) > 0 && engine[0] != nil {
		log = engine[0]
	} else {
		// Fall back to the default file-based storage
		logConfig := storage.LogConfig{
			Dir: config.DataDir,
			PartitionConfig: storage.PartitionConfig{
				SegmentConfig: storage.SegmentConfig{
					MaxBytes:           config.SegmentBytes,
					IndexIntervalBytes: 4096,
				},
			},
		}

		var err error
		log, err = storage.NewLog(logConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create log: %w", err)
		}
	}

	b := &Broker{
		config:       config,
		log:          log,
		topicConfigs: make(map[string]*TopicConfig),
		groupManager: NewGroupManager(),
		shutdownCh:   make(chan struct{}),
	}

	return b, nil
}

// Start starts the broker
func (b *Broker) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.running {
		return nil
	}

	b.running = true

	// Start background tasks
	go b.backgroundTasks()

	return nil
}

// Stop stops the broker
func (b *Broker) Stop() error {
	b.mu.Lock()
	if !b.running {
		b.mu.Unlock()
		return nil
	}
	b.running = false
	close(b.shutdownCh)
	b.mu.Unlock()

	// Close storage
	if err := b.log.Close(); err != nil {
		return fmt.Errorf("failed to close log: %w", err)
	}

	return nil
}

// backgroundTasks runs periodic background tasks
func (b *Broker) backgroundTasks() {
	syncTicker := time.NewTicker(5 * time.Second)
	defer syncTicker.Stop()

	for {
		select {
		case <-b.shutdownCh:
			return
		case <-syncTicker.C:
			b.log.Sync()
		}
	}
}

// ProduceRaw writes raw record batch bytes to a topic partition
// without decoding/re-encoding. This is the fast-path for produce requests.
func (b *Broker) ProduceRaw(topic string, partition int32, data []byte, recordCount int32, maxTimestamp int64) (int64, error) {
	// Single RLock for both running check and topic existence check
	b.mu.RLock()
	if !b.running {
		b.mu.RUnlock()
		return 0, fmt.Errorf("broker not running")
	}
	autoCreate := b.config.AutoCreateTopics
	needCreate := autoCreate && b.topicConfigs[topic] == nil
	b.mu.RUnlock()

	// Auto-create topic only when missing (avoids second RLock in ensureTopic)
	if needCreate {
		if err := b.ensureTopic(topic); err != nil {
			return 0, err
		}
	}

	return b.log.AppendRaw(topic, partition, data, recordCount, maxTimestamp)
}

// Produce writes records to a topic partition
func (b *Broker) Produce(topic string, partition int32, records []storage.Record) (int64, error) {
	b.mu.RLock()
	if !b.running {
		b.mu.RUnlock()
		return 0, fmt.Errorf("broker not running")
	}
	autoCreate := b.config.AutoCreateTopics
	b.mu.RUnlock()

	// Auto-create topic if needed
	if autoCreate {
		if err := b.ensureTopic(topic); err != nil {
			return 0, err
		}
	}

	return b.log.Append(topic, partition, records)
}

// ProduceAutoPartition writes records to a topic, choosing the partition
// automatically. When a non-nil key is provided the partition is determined
// by hash(key) % numPartitions (consistent hashing). When key is nil a
// simple round-robin is used.
//
// The number of partitions is resolved in order:
//  1. topicConfigs (in-memory topic registry)
//  2. storage engine metadata (covers topics that exist on disk/backend)
//  3. DefaultNumPartitions (only when auto-creating a brand new topic)
func (b *Broker) ProduceAutoPartition(topic string, key []byte, records []storage.Record) (partition int32, baseOffset int64, err error) {
	b.mu.RLock()
	if !b.running {
		b.mu.RUnlock()
		return 0, 0, fmt.Errorf("broker not running")
	}
	autoCreate := b.config.AutoCreateTopics
	numPartitions := int32(0)
	if tc := b.topicConfigs[topic]; tc != nil {
		numPartitions = tc.NumPartitions
	}
	b.mu.RUnlock()

	// Auto-create topic if it doesn't exist yet
	if autoCreate {
		if err := b.ensureTopic(topic); err != nil {
			return 0, 0, err
		}
		// Re-read after potential creation
		b.mu.RLock()
		if tc := b.topicConfigs[topic]; tc != nil {
			numPartitions = tc.NumPartitions
		}
		b.mu.RUnlock()
	}

	// Fallback: query the storage engine for the real partition count.
	// This covers topics that were created externally or existed before
	// the broker started and weren't yet loaded into topicConfigs.
	if numPartitions <= 0 {
		if meta, mErr := b.log.GetTopicMetadata(topic); mErr == nil && len(meta.Partitions) > 0 {
			numPartitions = int32(len(meta.Partitions))
		}
	}

	// Ultimate fallback
	if numPartitions <= 0 {
		numPartitions = b.config.DefaultNumPartitions
	}
	if numPartitions <= 0 {
		numPartitions = 1
	}

	if key != nil && len(key) > 0 {
		// Consistent hash: crc32 of the key mod numPartitions
		h := crc32.ChecksumIEEE(key)
		partition = int32(h % uint32(numPartitions))
	} else {
		// Round-robin
		n := b.rrCounter.Add(1)
		partition = int32(n % int64(numPartitions))
	}

	baseOffset, err = b.log.Append(topic, partition, records)
	return partition, baseOffset, err
}

// Fetch reads records from a topic partition
func (b *Broker) Fetch(topic string, partition int32, offset int64, maxBytes int64) ([]*storage.RecordBatch, error) {
	b.mu.RLock()
	if !b.running {
		b.mu.RUnlock()
		return nil, fmt.Errorf("broker not running")
	}
	b.mu.RUnlock()

	return b.log.Fetch(topic, partition, offset, maxBytes)
}

// CreateTopic creates a new topic
func (b *Broker) CreateTopic(name string, numPartitions int32, replicationFactor int16) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.running {
		return fmt.Errorf("broker not running")
	}

	if _, exists := b.topicConfigs[name]; exists {
		return storage.ErrTopicExists
	}

	// Create in storage
	if err := b.log.CreateTopic(name, numPartitions); err != nil {
		return err
	}

	// Save config
	b.topicConfigs[name] = &TopicConfig{
		Name:              name,
		NumPartitions:     numPartitions,
		ReplicationFactor: replicationFactor,
		RetentionMs:       b.config.LogRetentionMs,
		RetentionBytes:    b.config.LogRetentionBytes,
		CleanupPolicy:     "delete",
	}

	b.metadataVersion++

	// Notify cluster so the controller can compute partition assignments
	if b.cluster != nil {
		go b.cluster.NotifyTopicCreated(name, numPartitions, replicationFactor)
	}

	return nil
}

// DeleteTopic deletes a topic
func (b *Broker) DeleteTopic(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.running {
		return fmt.Errorf("broker not running")
	}

	if err := b.log.DeleteTopic(name); err != nil {
		return err
	}

	delete(b.topicConfigs, name)
	b.metadataVersion++

	// Notify cluster
	if b.cluster != nil {
		go b.cluster.NotifyTopicDeleted(name)
	}

	return nil
}

// PurgeTopic deletes all data in a topic but preserves the topic and its
// configuration. It does this by deleting and re-creating the topic in the
// storage layer with the same number of partitions.
func (b *Broker) PurgeTopic(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.running {
		return fmt.Errorf("broker not running")
	}

	tc, exists := b.topicConfigs[name]
	if !exists {
		return storage.ErrTopicNotFound
	}

	// Delete from storage
	if err := b.log.DeleteTopic(name); err != nil {
		return fmt.Errorf("purge: failed to delete topic data: %w", err)
	}

	// Re-create with the same partition count
	if err := b.log.CreateTopic(name, tc.NumPartitions); err != nil {
		return fmt.Errorf("purge: failed to re-create topic: %w", err)
	}

	b.metadataVersion++
	return nil
}

// UpdateTopicConfig updates mutable fields of a topic's configuration.
// Only non-zero values in the provided struct are applied.
func (b *Broker) UpdateTopicConfig(name string, retentionMs *int64, cleanupPolicy *string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.running {
		return fmt.Errorf("broker not running")
	}

	tc, exists := b.topicConfigs[name]
	if !exists {
		return storage.ErrTopicNotFound
	}

	if retentionMs != nil {
		tc.RetentionMs = *retentionMs
	}
	if cleanupPolicy != nil && (*cleanupPolicy == "delete" || *cleanupPolicy == "compact") {
		tc.CleanupPolicy = *cleanupPolicy
	}

	b.metadataVersion++
	return nil
}

// ensureTopic creates topic if it doesn't exist
func (b *Broker) ensureTopic(topic string) error {
	// Fast path: read-lock check (common case - topic already exists)
	b.mu.RLock()
	_, exists := b.topicConfigs[topic]
	b.mu.RUnlock()
	if exists {
		return nil
	}

	// Slow path: write-lock to create
	b.mu.Lock()
	defer b.mu.Unlock()

	// Double-check after acquiring write lock
	if _, exists := b.topicConfigs[topic]; exists {
		return nil
	}

	// Create with defaults
	if err := b.log.CreateTopic(topic, b.config.DefaultNumPartitions); err != nil {
		if err == storage.ErrTopicExists {
			return nil
		}
		return err
	}

	b.topicConfigs[topic] = &TopicConfig{
		Name:              topic,
		NumPartitions:     b.config.DefaultNumPartitions,
		ReplicationFactor: b.config.DefaultReplicationFactor,
		RetentionMs:       b.config.LogRetentionMs,
		RetentionBytes:    b.config.LogRetentionBytes,
		CleanupPolicy:     "delete",
	}

	b.metadataVersion++

	return nil
}

// ListTopics returns all registered TopicConfig entries.
func (b *Broker) ListTopics() []*TopicConfig {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]*TopicConfig, 0, len(b.topicConfigs))
	for _, tc := range b.topicConfigs {
		out = append(out, tc)
	}
	return out
}

// GetTopicConfig returns the configuration for a single topic, or nil.
func (b *Broker) GetTopicConfig(name string) *TopicConfig {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.topicConfigs[name]
}

// GetMetadata returns cluster metadata.
// In cluster mode this returns all brokers and accurate partition leadership.
// In standalone mode it returns only this broker as leader of everything.
func (b *Broker) GetMetadata(topics []string) (*ClusterMetadata, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.running {
		return nil, fmt.Errorf("broker not running")
	}

	// Use advertised host if set, otherwise fallback to localhost for 0.0.0.0
	advertisedHost := b.config.AdvertisedHost
	if advertisedHost == "" {
		if b.config.Host == "0.0.0.0" || b.config.Host == "" {
			advertisedHost = "localhost"
		} else {
			advertisedHost = b.config.Host
		}
	}

	// Cluster mode: return all brokers from the cluster router
	var meta *ClusterMetadata
	if b.cluster != nil {
		meta = &ClusterMetadata{
			Brokers:      b.cluster.GetClusterBrokers(),
			ControllerID: b.cluster.GetControllerID(),
		}
	} else {
		// Standalone mode: only this broker
		meta = &ClusterMetadata{
			Brokers: []BrokerInfo{{
				NodeID: b.config.NodeID,
				Host:   advertisedHost,
				Port:   b.config.Port,
			}},
			ControllerID: b.config.NodeID,
		}
	}

	// If no topics specified, return all
	if len(topics) == 0 {
		topics = b.log.ListTopics()
	}

	for _, topic := range topics {
		topicMeta, err := b.log.GetTopicMetadata(topic)
		if err != nil {
			continue
		}

		tm := TopicMetadata{
			Topic: topic,
		}

		for _, pm := range topicMeta.Partitions {
			pmeta := PartitionMetadata{
				Partition:     pm.Partition,
				HighWatermark: pm.HighWatermark,
			}
			if b.cluster != nil {
				replicas, isr, leaderEpoch := b.cluster.GetPartitionAssignment(topic, pm.Partition)
				leaderID, _, _, ok := b.cluster.GetPartitionLeader(topic, pm.Partition)
				if ok {
					pmeta.Leader = leaderID
				} else {
					pmeta.Leader = -1
				}
				pmeta.Replicas = replicas
				pmeta.ISR = isr
				pmeta.LeaderEpoch = leaderEpoch
			} else {
				pmeta.Leader = b.config.NodeID
				pmeta.Replicas = []int32{b.config.NodeID}
				pmeta.ISR = []int32{b.config.NodeID}
			}
			tm.Partitions = append(tm.Partitions, pmeta)
		}

		meta.Topics = append(meta.Topics, tm)
	}

	return meta, nil
}

// ListOffsets returns offsets for a partition
func (b *Broker) ListOffsets(topic string, partition int32, timestamp int64) (int64, error) {
	b.mu.RLock()
	if !b.running {
		b.mu.RUnlock()
		return 0, fmt.Errorf("broker not running")
	}
	b.mu.RUnlock()

	p, err := b.log.GetPartition(topic, partition)
	if err != nil {
		return 0, err
	}

	return p.GetOffsetByTime(timestamp)
}

// SetCluster wires an optional cluster router into the broker.
// When set, metadata and produce/fetch become cluster-aware.
func (b *Broker) SetCluster(c ClusterRouter) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cluster = c
}

// GetCluster returns the cluster router (nil in standalone mode).
func (b *Broker) GetCluster() ClusterRouter {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cluster
}

// GetPartition returns a read-only handle to a partition.
func (b *Broker) GetPartition(topic string, partition int32) (storage.PartitionReader, error) {
	return b.log.GetPartition(topic, partition)
}

// GetGroupManager returns the consumer group manager
func (b *Broker) GetGroupManager() *GroupManager {
	return b.groupManager
}

// ClusterMetadata contains cluster metadata
type ClusterMetadata struct {
	Brokers      []BrokerInfo
	ControllerID int32
	Topics       []TopicMetadata
}

// BrokerInfo contains broker information
type BrokerInfo struct {
	NodeID int32
	Host   string
	Port   int32
}

// TopicMetadata contains topic metadata
type TopicMetadata struct {
	Topic      string
	Partitions []PartitionMetadata
}

// PartitionMetadata contains partition metadata
type PartitionMetadata struct {
	Partition     int32
	Leader        int32
	LeaderEpoch   int32
	Replicas      []int32
	ISR           []int32
	HighWatermark int64
}
