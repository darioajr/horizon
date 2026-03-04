package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Partition represents a topic partition
type Partition struct {
	mu sync.RWMutex

	// Topic name
	topic string

	// Partition number
	partition int32

	// Directory for partition data
	dir string

	// Segments ordered by base offset
	segments []*Segment

	// Active segment for writes
	activeSegment *Segment

	// Segment configuration
	segmentConfig SegmentConfig

	// High watermark (last committed offset)
	highWatermark int64

	// Log start offset (first available offset)
	logStartOffset int64

	// Whether partition is closed
	closed bool
}

// PartitionConfig holds configuration for a partition
type PartitionConfig struct {
	SegmentConfig SegmentConfig
}

// DefaultPartitionConfig returns default partition configuration
func DefaultPartitionConfig() PartitionConfig {
	return PartitionConfig{
		SegmentConfig: DefaultSegmentConfig(),
	}
}

// NewPartition creates or opens a partition
func NewPartition(dir, topic string, partition int32, config PartitionConfig) (*Partition, error) {
	partitionDir := filepath.Join(dir, fmt.Sprintf("%s-%d", topic, partition))

	if err := os.MkdirAll(partitionDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create partition directory: %w", err)
	}

	p := &Partition{
		topic:         topic,
		partition:     partition,
		dir:           partitionDir,
		segmentConfig: config.SegmentConfig,
	}

	// Load existing segments
	if err := p.loadSegments(); err != nil {
		return nil, fmt.Errorf("failed to load segments: %w", err)
	}

	// Create initial segment if none exist
	if len(p.segments) == 0 {
		segment, err := NewSegment(partitionDir, 0, p.segmentConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create initial segment: %w", err)
		}
		p.segments = append(p.segments, segment)
	}

	// Active segment is the last one
	p.activeSegment = p.segments[len(p.segments)-1]

	// Set log start offset
	p.logStartOffset = p.segments[0].BaseOffset()

	// Set high watermark
	p.highWatermark = p.activeSegment.NextOffset()

	return p, nil
}

// loadSegments loads existing segments from disk
func (p *Partition) loadSegments() error {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return err
	}

	// Find all log files and extract base offsets
	var baseOffsets []int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, LogFileSuffix) {
			continue
		}

		// Parse base offset from filename
		offsetStr := strings.TrimSuffix(name, LogFileSuffix)
		offset, err := strconv.ParseInt(offsetStr, 10, 64)
		if err != nil {
			continue
		}
		baseOffsets = append(baseOffsets, offset)
	}

	// Sort by base offset
	sort.Slice(baseOffsets, func(i, j int) bool {
		return baseOffsets[i] < baseOffsets[j]
	})

	// Open segments
	for _, baseOffset := range baseOffsets {
		segment, err := NewSegment(p.dir, baseOffset, p.segmentConfig)
		if err != nil {
			return fmt.Errorf("failed to open segment %d: %w", baseOffset, err)
		}
		p.segments = append(p.segments, segment)
	}

	return nil
}

// Topic returns the topic name
func (p *Partition) Topic() string {
	return p.topic
}

// PartitionNum returns the partition number
func (p *Partition) PartitionNum() int32 {
	return p.partition
}

// HighWatermark returns the high watermark
func (p *Partition) HighWatermark() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.highWatermark
}

// LogStartOffset returns the log start offset
func (p *Partition) LogStartOffset() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.logStartOffset
}

// LogEndOffset returns the log end offset (next offset to be written)
func (p *Partition) LogEndOffset() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.activeSegment == nil {
		return 0
	}
	return p.activeSegment.NextOffset()
}

// AppendRaw writes raw record batch bytes to the partition without decode/re-encode.
func (p *Partition) AppendRaw(data []byte, recordCount int32, maxTimestamp int64) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return 0, ErrStorageClosed
	}

	// Check if we need to roll to a new segment (inline, no segment lock needed)
	if p.activeSegment.size >= p.activeSegment.maxBytes {
		if err := p.rollSegment(); err != nil {
			return 0, fmt.Errorf("failed to roll segment: %w", err)
		}
	}

	// Append raw bytes directly (skip segment lock — partition lock provides exclusion)
	baseOffset, err := p.activeSegment.appendRawLocked(data, recordCount, maxTimestamp)
	if err != nil {
		if err == ErrSegmentFull {
			if err := p.rollSegment(); err != nil {
				return 0, fmt.Errorf("failed to roll segment: %w", err)
			}
			baseOffset, err = p.activeSegment.appendRawLocked(data, recordCount, maxTimestamp)
			if err != nil {
				return 0, err
			}
		} else {
			return 0, err
		}
	}

	// Update high watermark (direct field access, no segment lock)
	p.highWatermark = p.activeSegment.nextOffset

	return baseOffset, nil
}

// Append writes records to the partition
func (p *Partition) Append(records []Record) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return 0, ErrStorageClosed
	}

	// Check if we need to roll to a new segment
	if p.activeSegment.IsFull() {
		if err := p.rollSegment(); err != nil {
			return 0, fmt.Errorf("failed to roll segment: %w", err)
		}
	}

	// Create batch
	batch := NewRecordBatch(0, records)

	// Append to active segment
	baseOffset, err := p.activeSegment.Append(batch)
	if err != nil {
		if err == ErrSegmentFull {
			// Roll and retry
			if err := p.rollSegment(); err != nil {
				return 0, fmt.Errorf("failed to roll segment: %w", err)
			}
			baseOffset, err = p.activeSegment.Append(batch)
			if err != nil {
				return 0, err
			}
		} else {
			return 0, err
		}
	}

	// Update high watermark
	p.highWatermark = p.activeSegment.NextOffset()

	return baseOffset, nil
}

// rollSegment creates a new segment and makes it active
func (p *Partition) rollSegment() error {
	// Sync current segment
	if err := p.activeSegment.Sync(); err != nil {
		return err
	}

	// Create new segment
	nextOffset := p.activeSegment.NextOffset()
	segment, err := NewSegment(p.dir, nextOffset, p.segmentConfig)
	if err != nil {
		return err
	}

	p.segments = append(p.segments, segment)
	p.activeSegment = segment

	return nil
}

// Fetch reads records starting from the given offset
func (p *Partition) Fetch(offset int64, maxBytes int64) ([]*RecordBatch, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return nil, ErrStorageClosed
	}

	if offset < p.logStartOffset {
		return nil, ErrOffsetOutOfRange
	}

	// If offset is at or beyond the high watermark, consumer is caught up — return empty, no error.
	// Returning ErrOffsetOutOfRange here would cause clients to reset their offset and re-read.
	if offset >= p.highWatermark {
		return nil, nil
	}

	// Find the segment containing this offset
	segment := p.findSegment(offset)
	if segment == nil {
		return nil, ErrOffsetOutOfRange
	}

	return segment.Read(offset, maxBytes)
}

// findSegment finds the segment that contains the given offset
func (p *Partition) findSegment(offset int64) *Segment {
	// Binary search for the right segment
	idx := sort.Search(len(p.segments), func(i int) bool {
		return p.segments[i].BaseOffset() > offset
	})

	if idx == 0 {
		// Check if it's in the first segment
		if len(p.segments) > 0 && offset >= p.segments[0].BaseOffset() {
			return p.segments[0]
		}
		return nil
	}

	// Return the segment before the one found
	return p.segments[idx-1]
}

// Sync flushes all data to disk
func (p *Partition) Sync() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return ErrStorageClosed
	}

	for _, segment := range p.segments {
		if err := segment.Sync(); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the partition
func (p *Partition) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	p.closed = true

	for _, segment := range p.segments {
		if err := segment.Close(); err != nil {
			return err
		}
	}

	return nil
}

// TruncateTo truncates the log to the given offset
func (p *Partition) TruncateTo(offset int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return ErrStorageClosed
	}

	// Find segments to delete
	var toDelete []*Segment
	var remaining []*Segment

	for _, segment := range p.segments {
		if segment.NextOffset() <= offset {
			toDelete = append(toDelete, segment)
		} else {
			remaining = append(remaining, segment)
		}
	}

	// Delete old segments
	for _, segment := range toDelete {
		if err := segment.Delete(); err != nil {
			return fmt.Errorf("failed to delete segment: %w", err)
		}
	}

	p.segments = remaining

	// Update log start offset
	if len(p.segments) > 0 {
		p.logStartOffset = p.segments[0].BaseOffset()
	} else {
		// Create new segment at the truncation point
		segment, err := NewSegment(p.dir, offset, p.segmentConfig)
		if err != nil {
			return err
		}
		p.segments = []*Segment{segment}
		p.activeSegment = segment
		p.logStartOffset = offset
	}

	return nil
}

// GetOffsetByTime finds the offset for a given timestamp
func (p *Partition) GetOffsetByTime(timestamp int64) (int64, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return 0, ErrStorageClosed
	}

	// Special values
	const (
		LatestTimestamp   = -1
		EarliestTimestamp = -2
	)

	switch timestamp {
	case LatestTimestamp:
		return p.activeSegment.NextOffset(), nil
	case EarliestTimestamp:
		return p.logStartOffset, nil
	default:
		// TODO: Implement time-based lookup using time index
		// For now, return log start offset
		return p.logStartOffset, nil
	}
}
