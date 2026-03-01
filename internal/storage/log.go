package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Log manages all topics and partitions
type Log struct {
	mu sync.RWMutex

	// Base directory for data
	dir string

	// Map of topic -> partition number -> partition
	partitions map[string]map[int32]*Partition

	// Default configuration for new partitions
	partitionConfig PartitionConfig

	// Whether log is closed
	closed bool
}

// LogConfig holds configuration for the log manager
type LogConfig struct {
	// Data directory
	Dir string

	// Partition configuration
	PartitionConfig PartitionConfig
}

// DefaultLogConfig returns default log configuration
func DefaultLogConfig(dir string) LogConfig {
	return LogConfig{
		Dir:             dir,
		PartitionConfig: DefaultPartitionConfig(),
	}
}

// NewLog creates a new log manager
func NewLog(config LogConfig) (*Log, error) {
	if err := os.MkdirAll(config.Dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	l := &Log{
		dir:             config.Dir,
		partitions:      make(map[string]map[int32]*Partition),
		partitionConfig: config.PartitionConfig,
	}

	// Load existing topics/partitions
	if err := l.loadExisting(); err != nil {
		return nil, fmt.Errorf("failed to load existing data: %w", err)
	}

	return l, nil
}

// loadExisting loads existing topics and partitions from disk
func (l *Log) loadExisting() error {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Parse topic-partition format
		lastDash := strings.LastIndex(name, "-")
		if lastDash == -1 {
			continue
		}

		topic := name[:lastDash]
		partitionStr := name[lastDash+1:]
		var partition int32
		if _, err := fmt.Sscanf(partitionStr, "%d", &partition); err != nil {
			continue
		}

		// Open partition
		p, err := NewPartition(l.dir, topic, partition, l.partitionConfig)
		if err != nil {
			return fmt.Errorf("failed to open partition %s-%d: %w", topic, partition, err)
		}

		if l.partitions[topic] == nil {
			l.partitions[topic] = make(map[int32]*Partition)
		}
		l.partitions[topic][partition] = p
	}

	return nil
}

// CreateTopic creates a new topic with the specified number of partitions
func (l *Log) CreateTopic(topic string, numPartitions int32) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return ErrStorageClosed
	}

	if _, exists := l.partitions[topic]; exists {
		return ErrTopicExists
	}

	l.partitions[topic] = make(map[int32]*Partition)

	for i := int32(0); i < numPartitions; i++ {
		p, err := NewPartition(l.dir, topic, i, l.partitionConfig)
		if err != nil {
			// Cleanup on failure
			for _, p := range l.partitions[topic] {
				p.Close()
			}
			delete(l.partitions, topic)
			return fmt.Errorf("failed to create partition %d: %w", i, err)
		}
		l.partitions[topic][i] = p
	}

	return nil
}

// DeleteTopic deletes a topic and all its partitions
func (l *Log) DeleteTopic(topic string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return ErrStorageClosed
	}

	partitions, exists := l.partitions[topic]
	if !exists {
		return ErrTopicNotFound
	}

	// Close and delete partitions
	for partNum, p := range partitions {
		if err := p.Close(); err != nil {
			return fmt.Errorf("failed to close partition %d: %w", partNum, err)
		}

		partDir := filepath.Join(l.dir, fmt.Sprintf("%s-%d", topic, partNum))
		if err := os.RemoveAll(partDir); err != nil {
			return fmt.Errorf("failed to delete partition directory: %w", err)
		}
	}

	delete(l.partitions, topic)
	return nil
}

// getPartition returns the concrete *Partition (internal use by Log methods).
func (l *Log) getPartition(topic string, partition int32) (*Partition, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.closed {
		return nil, ErrStorageClosed
	}

	partitions, exists := l.partitions[topic]
	if !exists {
		return nil, ErrTopicNotFound
	}

	p, exists := partitions[partition]
	if !exists {
		return nil, ErrPartitionNotFound
	}

	return p, nil
}

// GetPartition returns a partition by topic and partition number
func (l *Log) GetPartition(topic string, partition int32) (PartitionReader, error) {
	return l.getPartition(topic, partition)
}

// getOrCreatePartition returns the concrete *Partition (internal use by Log methods).
func (l *Log) getOrCreatePartition(topic string, partition int32) (*Partition, error) {
	// Fast path: read-lock for existing partitions (common case)
	l.mu.RLock()
	if l.closed {
		l.mu.RUnlock()
		return nil, ErrStorageClosed
	}
	if partitions, exists := l.partitions[topic]; exists {
		if p, exists := partitions[partition]; exists {
			l.mu.RUnlock()
			return p, nil
		}
	}
	l.mu.RUnlock()

	// Slow path: write-lock to create new partition
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil, ErrStorageClosed
	}

	// Double-check after acquiring write lock
	if partitions, exists := l.partitions[topic]; exists {
		if p, exists := partitions[partition]; exists {
			return p, nil
		}
	} else {
		l.partitions[topic] = make(map[int32]*Partition)
	}

	// Create new partition
	p, err := NewPartition(l.dir, topic, partition, l.partitionConfig)
	if err != nil {
		return nil, err
	}

	l.partitions[topic][partition] = p
	return p, nil
}

// GetOrCreatePartition gets an existing partition or creates it
func (l *Log) GetOrCreatePartition(topic string, partition int32) (PartitionReader, error) {
	return l.getOrCreatePartition(topic, partition)
}

// AppendRaw writes raw record batch bytes to a partition without decode/re-encode.
func (l *Log) AppendRaw(topic string, partition int32, data []byte, recordCount int32, maxTimestamp int64) (int64, error) {
	p, err := l.getOrCreatePartition(topic, partition)
	if err != nil {
		return 0, err
	}

	return p.AppendRaw(data, recordCount, maxTimestamp)
}

// Append writes records to a partition
func (l *Log) Append(topic string, partition int32, records []Record) (int64, error) {
	p, err := l.getOrCreatePartition(topic, partition)
	if err != nil {
		return 0, err
	}

	return p.Append(records)
}

// Fetch reads records from a partition
func (l *Log) Fetch(topic string, partition int32, offset int64, maxBytes int64) ([]*RecordBatch, error) {
	p, err := l.getPartition(topic, partition)
	if err != nil {
		return nil, err
	}

	return p.Fetch(offset, maxBytes)
}

// ListTopics returns all topic names
func (l *Log) ListTopics() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	topics := make([]string, 0, len(l.partitions))
	for topic := range l.partitions {
		topics = append(topics, topic)
	}
	return topics
}

// GetTopicPartitions returns partition numbers for a topic
func (l *Log) GetTopicPartitions(topic string) ([]int32, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	partitions, exists := l.partitions[topic]
	if !exists {
		return nil, ErrTopicNotFound
	}

	nums := make([]int32, 0, len(partitions))
	for num := range partitions {
		nums = append(nums, num)
	}
	return nums, nil
}

// TopicMetadata contains metadata about a topic
type TopicMetadata struct {
	Topic      string
	Partitions []PartitionMetadata
}

// PartitionMetadata contains metadata about a partition
type PartitionMetadata struct {
	Partition      int32
	HighWatermark  int64
	LogStartOffset int64
	LogEndOffset   int64
}

// GetTopicMetadata returns metadata for a topic
func (l *Log) GetTopicMetadata(topic string) (*TopicMetadata, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	partitions, exists := l.partitions[topic]
	if !exists {
		return nil, ErrTopicNotFound
	}

	meta := &TopicMetadata{
		Topic:      topic,
		Partitions: make([]PartitionMetadata, 0, len(partitions)),
	}

	for num, p := range partitions {
		meta.Partitions = append(meta.Partitions, PartitionMetadata{
			Partition:      num,
			HighWatermark:  p.HighWatermark(),
			LogStartOffset: p.LogStartOffset(),
			LogEndOffset:   p.LogEndOffset(),
		})
	}

	return meta, nil
}

// Sync flushes all data to disk
func (l *Log) Sync() error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.closed {
		return ErrStorageClosed
	}

	for _, partitions := range l.partitions {
		for _, p := range partitions {
			if err := p.Sync(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close closes all partitions
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}

	l.closed = true

	for _, partitions := range l.partitions {
		for _, p := range partitions {
			if err := p.Close(); err != nil {
				return err
			}
		}
	}

	return nil
}
