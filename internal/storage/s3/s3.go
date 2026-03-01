// Package s3 implements a StorageEngine backed by Amazon S3 (or any
// S3-compatible object store such as MinIO).
//
// Data layout in the bucket:
//
//	<prefix>/<topic>-<partition>/segments/<baseOffset>.log
//	<prefix>/<topic>-<partition>/segments/<baseOffset>.index
//	<prefix>/<topic>-<partition>/segments/<baseOffset>.timeindex
//	<prefix>/<topic>-<partition>/meta.json   (offsets, watermarks)
package s3

import (
	"fmt"
	"sync"

	"horizon/internal/storage"
)

// Compile-time check.
var _ storage.StorageEngine = (*Storage)(nil)

// Config holds S3-specific configuration.
type Config struct {
	// Bucket is the S3 bucket name.
	Bucket string `yaml:"bucket"`

	// Prefix is an optional key prefix inside the bucket.
	Prefix string `yaml:"prefix"`

	// Region is the AWS region (e.g. "us-east-1").
	Region string `yaml:"region"`

	// Endpoint overrides the default AWS endpoint (useful for MinIO).
	Endpoint string `yaml:"endpoint"`

	// AccessKey and SecretKey for authentication.
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`

	// SegmentMaxBytes is the maximum size of a single segment object.
	SegmentMaxBytes int64 `yaml:"segment_max_bytes"`
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Bucket:          "horizon-data",
		Region:          "us-east-1",
		SegmentMaxBytes: 1024 * 1024 * 1024, // 1 GB
	}
}

// Storage is the S3-backed StorageEngine.
type Storage struct {
	mu     sync.RWMutex
	config Config
	closed bool

	// In-memory topic registry (loaded from S3 on startup).
	topics map[string]map[int32]*partition
}

// New creates a new S3-backed StorageEngine.
func New(cfg Config) (*Storage, error) {
	// TODO: initialise the AWS/S3 client using cfg.
	s := &Storage{
		config: cfg,
		topics: make(map[string]map[int32]*partition),
	}

	// TODO: list existing "<prefix>/<topic>-<partition>/" prefixes in the
	// bucket and populate s.topics.

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
		// TODO: create the initial meta.json object in S3.
	}
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

	// TODO: delete all S3 objects under <prefix>/<topic>-*/
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
	// S3 writes are durable once PutObject returns – nothing to flush.
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
		// TODO: implement time-based lookup via S3 time-index objects.
		return p.logStartOffset, nil
	}
}

func (p *partition) appendRaw(data []byte, recordCount int32, _ int64) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	baseOffset := p.nextOffset

	// Patch the base offset in the raw Kafka record batch bytes.
	storage.PatchBaseOffset(data, baseOffset)

	// TODO: upload data as a new segment object or append to the current
	// segment object in S3. For example:
	//   key := fmt.Sprintf("%s/%s-%d/segments/%020d.log", cfg.Prefix, p.topic, p.partitionNum, baseOffset)
	//   s3Client.PutObject(ctx, &s3.PutObjectInput{Bucket: &cfg.Bucket, Key: &key, Body: bytes.NewReader(data)})
	_ = fmt.Sprintf("s3://%s/%s-%d/%020d.log", p.config.Bucket, p.topic, p.partitionNum, baseOffset)

	p.nextOffset += int64(recordCount)
	p.highWatermark = p.nextOffset

	return baseOffset, nil
}

func (p *partition) fetch(offset int64, maxBytes int64) ([]*storage.RecordBatch, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if offset < p.logStartOffset || offset >= p.nextOffset {
		return nil, storage.ErrOffsetOutOfRange
	}

	// TODO: download the relevant segment object(s) from S3 and decode.
	//   key := findSegmentKey(offset)
	//   resp, _ := s3Client.GetObject(ctx, &s3.GetObjectInput{Bucket: &cfg.Bucket, Key: &key, Range: ...})
	//   data, _ := io.ReadAll(resp.Body)
	//   decode data into []*storage.RecordBatch

	return nil, fmt.Errorf("s3: fetch not yet implemented")
}
