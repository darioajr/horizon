// Package infinispan implements a StorageEngine backed by Infinispan
// (Red Hat Data Grid). Communication uses the Infinispan REST API v2.
//
// Data layout:
//
//	cache: horizon-meta          – JSON entries keyed by "<topic>/<partition>"
//	cache: horizon-<topic>-<partition>  – binary entries keyed by offset, value = raw batch bytes
package infinispan

import (
	"fmt"
	"sync"

	"horizon/internal/storage"
)

// Compile-time check.
var _ storage.StorageEngine = (*Storage)(nil)

// Config holds Infinispan-specific configuration.
type Config struct {
	// URL is the base URL of the Infinispan REST endpoint, e.g.
	// "http://localhost:11222".
	URL string `yaml:"url"`

	// CacheName is the prefix used for cache names (default "horizon").
	CacheName string `yaml:"cache_name"`

	// Username and Password for HTTP Basic authentication.
	Username string `yaml:"username"`
	Password string `yaml:"password"`

	// SegmentMaxBytes limits per-partition data before eviction/compaction.
	SegmentMaxBytes int64 `yaml:"segment_max_bytes"`
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		URL:             "http://localhost:11222",
		CacheName:       "horizon",
		SegmentMaxBytes: 1024 * 1024 * 1024,
	}
}

// Storage is the Infinispan-backed StorageEngine.
type Storage struct {
	mu     sync.RWMutex
	config Config
	closed bool

	topics map[string]map[int32]*partition

	// TODO: add *http.Client or a dedicated Infinispan client here.
}

// New creates a new Infinispan-backed StorageEngine.
func New(cfg Config) (*Storage, error) {
	s := &Storage{
		config: cfg,
		topics: make(map[string]map[int32]*partition),
	}

	// TODO: create HTTP client, verify connectivity:
	//   GET <url>/rest/v2/caches
	// TODO: auto-create caches if they don't exist:
	//   POST <url>/rest/v2/caches/<cacheName>

	// TODO: load existing topic/partition metadata from the meta cache.

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

	// TODO: PUT metadata in the meta cache:
	//   PUT <url>/rest/v2/caches/horizon-meta/<topic>

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

	// TODO: DELETE metadata and data caches for this topic.

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
	// Infinispan handles persistence internally; nothing to flush.
	return nil
}

func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
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
		// TODO: implement time-based lookup via Infinispan query.
		return p.logStartOffset, nil
	}
}

func (p *partition) appendRaw(data []byte, recordCount int32, _ int64) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	baseOffset := p.nextOffset

	storage.PatchBaseOffset(data, baseOffset)

	// TODO: PUT the raw batch bytes in the data cache:
	//   PUT <url>/rest/v2/caches/horizon-<topic>-<partition>/<offset>
	//   Content-Type: application/octet-stream
	//   Body: data
	_ = fmt.Sprintf("infinispan://%s-%d/%d", p.topic, p.partitionNum, baseOffset)

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

	// TODO: GET entries from the data cache starting at offset:
	//   GET <url>/rest/v2/caches/horizon-<topic>-<partition>/<offset>
	//   Decode response body into []*storage.RecordBatch

	return nil, fmt.Errorf("infinispan: fetch not yet implemented")
}
