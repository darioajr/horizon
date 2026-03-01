package storage

// Compile-time checks: the default file-based implementation must satisfy the interfaces.
var (
	_ StorageEngine  = (*Log)(nil)
	_ PartitionReader = (*Partition)(nil)
)

// StorageEngine is the main interface for the persistence layer.
// Implementations can back the storage with local files, S3, Redis, Infinispan, etc.
type StorageEngine interface {
	// --- Topic management ---

	// CreateTopic creates a new topic with the specified number of partitions.
	CreateTopic(topic string, numPartitions int32) error

	// DeleteTopic deletes a topic and all its data.
	DeleteTopic(topic string) error

	// ListTopics returns the names of all existing topics.
	ListTopics() []string

	// GetTopicPartitions returns the partition numbers for a topic.
	GetTopicPartitions(topic string) ([]int32, error)

	// GetTopicMetadata returns metadata (offsets, watermarks) for a topic.
	GetTopicMetadata(topic string) (*TopicMetadata, error)

	// --- Partition access ---

	// GetPartition returns a read-only handle to an existing partition.
	GetPartition(topic string, partition int32) (PartitionReader, error)

	// GetOrCreatePartition returns (or auto-creates) a partition handle.
	GetOrCreatePartition(topic string, partition int32) (PartitionReader, error)

	// --- Data operations ---

	// AppendRaw writes a pre-encoded record batch (Kafka wire format) to a
	// partition without decoding / re-encoding. This is the fast-path used
	// by the produce handler.
	AppendRaw(topic string, partition int32, data []byte, recordCount int32, maxTimestamp int64) (int64, error)

	// Append encodes the given records and writes them to a partition.
	Append(topic string, partition int32, records []Record) (int64, error)

	// Fetch reads record batches starting at the given offset.
	Fetch(topic string, partition int32, offset int64, maxBytes int64) ([]*RecordBatch, error)

	// --- Lifecycle ---

	// Sync flushes any buffered data to the underlying store.
	Sync() error

	// Close releases all resources held by the storage engine.
	Close() error
}

// PartitionReader provides read-only access to partition metadata.
type PartitionReader interface {
	// Topic returns the topic name.
	Topic() string

	// PartitionNum returns the partition number.
	PartitionNum() int32

	// HighWatermark returns the offset of the last committed record + 1.
	HighWatermark() int64

	// LogStartOffset returns the earliest available offset.
	LogStartOffset() int64

	// LogEndOffset returns the next offset that will be written.
	LogEndOffset() int64

	// GetOffsetByTime returns the offset for a given timestamp.
	// Special timestamps: -1 = latest, -2 = earliest.
	GetOffsetByTime(timestamp int64) (int64, error)
}
