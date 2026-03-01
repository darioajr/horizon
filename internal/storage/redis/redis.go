// Package redis implements a StorageEngine backed by Redis.
//
// Data layout:
//
//	horizon:<topic>:<partition>:log      – Redis Stream holding raw record batches
//	horizon:<topic>:<partition>:meta     – Hash with highWatermark, logStartOffset, nextOffset
//	horizon:topics                       – Set of topic names
//	horizon:<topic>:partitions           – Set of partition numbers
package redis

import (
	"fmt"
	"sync"

	"horizon/internal/storage"
)

// Compile-time check.
var _ storage.StorageEngine = (*Storage)(nil)

// Config holds Redis-specific configuration.
type Config struct {
	// Addr is the Redis server address (e.g. "localhost:6379").
	Addr string `yaml:"addr"`

	// Password for Redis AUTH.
	Password string `yaml:"password"`

	// DB is the Redis database number.
	DB int `yaml:"db"`

	// KeyPrefix is prepended to every key (default "horizon").
	KeyPrefix string `yaml:"key_prefix"`

	// SegmentMaxBytes limits how much data is buffered per partition before
	// rotating to a new Redis Stream entry range.
	SegmentMaxBytes int64 `yaml:"segment_max_bytes"`
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Addr:            "localhost:6379",
		DB:              0,
		KeyPrefix:       "horizon",
		SegmentMaxBytes: 1024 * 1024 * 1024,
	}
}

// Storage is the Redis-backed StorageEngine.
type Storage struct {
	mu     sync.RWMutex
	config Config
	closed bool

	// In-memory topic registry (synced with Redis sets on startup).
	topics map[string]map[int32]*partition

	// TODO: add *redis.Client field here once go-redis is imported.
	// client *redis.Client
}

// New creates a new Redis-backed StorageEngine.
func New(cfg Config) (*Storage, error) {
	s := &Storage{
		config: cfg,
		topics: make(map[string]map[int32]*partition),
	}

	// TODO: create Redis client:
	//   s.client = redis.NewClient(&redis.Options{Addr: cfg.Addr, Password: cfg.Password, DB: cfg.DB})
	//   if err := s.client.Ping(ctx).Err(); err != nil { return nil, err }

	// TODO: load existing topics/partitions from Redis sets.

	return s, nil
}

// ---------------------------------------------------------------------------
// Topic management
// ---------------------------------------------------------------------------

func (s *Storage) CreateTopic(topic string, numPartitions int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return storage.ErrStorageClosed
	}
	if _, exists := s.topics[topic]; exists {
		return storage.ErrTopicExists
	}

	s.topics[topic] = make(map[int32]*partition)
	for i := int32(0); i < numPartitions; i++ {
		p := newPartition(topic, i, s.config)
		s.topics[topic][i] = p
	}

	// TODO: SADD horizon:topics <topic>
	// TODO: for each partition SADD horizon:<topic>:partitions <i>

	return nil
}

func (s *Storage) DeleteTopic(topic string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return storage.ErrStorageClosed
	}
	if _, exists := s.topics[topic]; !exists {
		return storage.ErrTopicNotFound
	}

	// TODO: DEL all keys matching horizon:<topic>:*
	// TODO: SREM horizon:topics <topic>

	delete(s.topics, topic)
	return nil
}

func (s *Storage) ListTopics() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]string, 0, len(s.topics))
	for t := range s.topics {
		out = append(out, t)
	}
	return out
}

func (s *Storage) GetTopicPartitions(topic string) ([]int32, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	parts, ok := s.topics[topic]
	if !ok {
		return nil, storage.ErrTopicNotFound
	}
	nums := make([]int32, 0, len(parts))
	for n := range parts {
		nums = append(nums, n)
	}
	return nums, nil
}

func (s *Storage) GetTopicMetadata(topic string) (*storage.TopicMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	parts, ok := s.topics[topic]
	if !ok {
		return nil, storage.ErrTopicNotFound
	}

	meta := &storage.TopicMetadata{Topic: topic}
	for num, p := range parts {
		meta.Partitions = append(meta.Partitions, storage.PartitionMetadata{
			Partition:      num,
			HighWatermark:  p.HighWatermark(),
			LogStartOffset: p.LogStartOffset(),
			LogEndOffset:   p.LogEndOffset(),
		})
	}
	return meta, nil
}

// ---------------------------------------------------------------------------
// Partition access
// ---------------------------------------------------------------------------

func (s *Storage) GetPartition(topic string, partNum int32) (storage.PartitionReader, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, storage.ErrStorageClosed
	}
	parts, ok := s.topics[topic]
	if !ok {
		return nil, storage.ErrTopicNotFound
	}
	p, ok := parts[partNum]
	if !ok {
		return nil, storage.ErrPartitionNotFound
	}
	return p, nil
}

func (s *Storage) GetOrCreatePartition(topic string, partNum int32) (storage.PartitionReader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, storage.ErrStorageClosed
	}
	if s.topics[topic] == nil {
		s.topics[topic] = make(map[int32]*partition)
	}
	p, ok := s.topics[topic][partNum]
	if !ok {
		p = newPartition(topic, partNum, s.config)
		s.topics[topic][partNum] = p
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// Data operations
// ---------------------------------------------------------------------------

func (s *Storage) AppendRaw(topic string, part int32, data []byte, recordCount int32, maxTimestamp int64) (int64, error) {
	pr, err := s.GetOrCreatePartition(topic, part)
	if err != nil {
		return 0, err
	}
	p := pr.(*partition)
	return p.appendRaw(data, recordCount, maxTimestamp)
}

func (s *Storage) Append(topic string, part int32, records []storage.Record) (int64, error) {
	pr, err := s.GetOrCreatePartition(topic, part)
	if err != nil {
		return 0, err
	}
	p := pr.(*partition)
	batch := storage.NewRecordBatch(0, records)
	encoded := batch.Encode()
	return p.appendRaw(encoded, int32(len(records)), batch.MaxTimestamp)
}

func (s *Storage) Fetch(topic string, part int32, offset int64, maxBytes int64) ([]*storage.RecordBatch, error) {
	pr, err := s.GetPartition(topic, part)
	if err != nil {
		return nil, err
	}
	p := pr.(*partition)
	return p.fetch(offset, maxBytes)
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func (s *Storage) Sync() error {
	// Redis writes are durable once acknowledged (if AOF/RDB persistence is enabled).
	return nil
}

func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	// TODO: s.client.Close()
	return nil
}

// ---------------------------------------------------------------------------
// Internal partition type
// ---------------------------------------------------------------------------

type partition struct {
	mu             sync.RWMutex
	topic          string
	partitionNum   int32
	config         Config
	highWatermark  int64
	logStartOffset int64
	nextOffset     int64
}

func newPartition(topic string, num int32, cfg Config) *partition {
	return &partition{
		topic:        topic,
		partitionNum: num,
		config:       cfg,
	}
}

func (p *partition) Topic() string        { return p.topic }
func (p *partition) PartitionNum() int32   { return p.partitionNum }

func (p *partition) HighWatermark() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.highWatermark
}

func (p *partition) LogStartOffset() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.logStartOffset
}

func (p *partition) LogEndOffset() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.nextOffset
}

func (p *partition) GetOffsetByTime(timestamp int64) (int64, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	const (
		latestTimestamp   = -1
		earliestTimestamp = -2
	)
	switch timestamp {
	case latestTimestamp:
		return p.nextOffset, nil
	case earliestTimestamp:
		return p.logStartOffset, nil
	default:
		// TODO: use a Redis sorted set (timestamp → offset) for time-based lookup.
		return p.logStartOffset, nil
	}
}

func (p *partition) appendRaw(data []byte, recordCount int32, _ int64) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	baseOffset := p.nextOffset

	// Patch the Kafka base offset in the raw bytes.
	storage.PatchBaseOffset(data, baseOffset)

	// TODO: XADD to the Redis Stream for this partition:
	//   streamKey := fmt.Sprintf("%s:%s:%d:log", p.config.KeyPrefix, p.topic, p.partitionNum)
	//   client.XAdd(ctx, &redis.XAddArgs{Stream: streamKey, Values: map[string]interface{}{"d": data}})

	p.nextOffset += int64(recordCount)
	p.highWatermark = p.nextOffset

	return baseOffset, nil
}

func (p *partition) fetch(offset int64, _ int64) ([]*storage.RecordBatch, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if offset < p.logStartOffset || offset >= p.nextOffset {
		return nil, storage.ErrOffsetOutOfRange
	}

	// TODO: XRANGE on the Redis Stream:
	//   streamKey := fmt.Sprintf("%s:%s:%d:log", p.config.KeyPrefix, p.topic, p.partitionNum)
	//   msgs, _ := client.XRange(ctx, streamKey, startID, "+").Result()
	//   decode each msg["d"] into *storage.RecordBatch

	return nil, fmt.Errorf("redis: fetch not yet implemented")
}
