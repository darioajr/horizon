// Package storage implements the persistent storage layer for Horizon.
// It provides log-structured storage similar to Kafka's segment files.
package storage

import (
	"encoding/binary"
	"hash/crc32"
	"time"
)

// crc32cTable is the Castagnoli CRC-32C table used by Kafka
var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

// CompressionType represents message compression
type CompressionType int8

const (
	CompressionNone   CompressionType = 0
	CompressionGzip   CompressionType = 1
	CompressionSnappy CompressionType = 2
	CompressionLZ4    CompressionType = 3
	CompressionZstd   CompressionType = 4
)

// Record represents a single message in a batch
type Record struct {
	// Offset delta from base offset of the batch
	OffsetDelta int32
	// Timestamp delta from first timestamp of the batch
	TimestampDelta int64
	// Key of the record (can be nil)
	Key []byte
	// Value of the record
	Value []byte
	// Headers are key-value pairs attached to the record
	Headers []RecordHeader
}

// RecordHeader represents a key-value pair attached to a record
type RecordHeader struct {
	Key   string
	Value []byte
}

// RecordBatch represents a batch of records (Kafka message format v2)
type RecordBatch struct {
	// BaseOffset is the offset of the first record
	BaseOffset int64
	// BatchLength is the length in bytes of the batch
	BatchLength int32
	// PartitionLeaderEpoch for replication
	PartitionLeaderEpoch int32
	// Magic version (currently 2)
	Magic int8
	// CRC32 checksum of the batch (from attributes to end)
	CRC uint32
	// Attributes contains compression, timestamp type, etc.
	Attributes int16
	// LastOffsetDelta is the offset of the last record
	LastOffsetDelta int32
	// FirstTimestamp is the timestamp of the first record
	FirstTimestamp int64
	// MaxTimestamp is the max timestamp in the batch
	MaxTimestamp int64
	// ProducerId for idempotent/transactional producers
	ProducerId int64
	// ProducerEpoch for idempotent/transactional producers
	ProducerEpoch int16
	// BaseSequence for idempotent producers
	BaseSequence int32
	// Records in this batch
	Records []Record
}

// RecordBatchHeader size in bytes (fixed header before variable records)
const RecordBatchHeaderSize = 61

// NewRecordBatch creates a new record batch
func NewRecordBatch(baseOffset int64, records []Record) *RecordBatch {
	now := time.Now().UnixMilli()
	batch := &RecordBatch{
		BaseOffset:           baseOffset,
		PartitionLeaderEpoch: 0,
		Magic:                2,
		Attributes:           0, // no compression, create time
		FirstTimestamp:       now,
		MaxTimestamp:         now,
		ProducerId:           -1,
		ProducerEpoch:        -1,
		BaseSequence:         -1,
		Records:              records,
	}
	if len(records) > 0 {
		batch.LastOffsetDelta = records[len(records)-1].OffsetDelta
	}
	return batch
}

// Encode serializes the record batch to bytes
func (b *RecordBatch) Encode() []byte {
	// Encode records first
	recordsData := b.encodeRecords()

	// Calculate total size
	// baseOffset(8) + batchLength(4) + partitionLeaderEpoch(4) + magic(1) 
	// + crc(4) + attributes(2) + lastOffsetDelta(4) + firstTimestamp(8)
	// + maxTimestamp(8) + producerId(8) + producerEpoch(2) + baseSequence(4)
	// + recordCount(4) + records
	headerSize := 8 + 4 + 4 + 1 + 4 + 2 + 4 + 8 + 8 + 8 + 2 + 4 + 4
	totalSize := headerSize + len(recordsData)

	buf := make([]byte, totalSize)
	pos := 0

	// Base offset
	binary.BigEndian.PutUint64(buf[pos:], uint64(b.BaseOffset))
	pos += 8

	// Batch length (from partition leader epoch to end)
	b.BatchLength = int32(totalSize - 12) // everything after baseOffset and batchLength
	binary.BigEndian.PutUint32(buf[pos:], uint32(b.BatchLength))
	pos += 4

	// Partition leader epoch
	binary.BigEndian.PutUint32(buf[pos:], uint32(b.PartitionLeaderEpoch))
	pos += 4

	// Magic
	buf[pos] = byte(b.Magic)
	pos++

	// CRC placeholder (will fill after)
	crcPos := pos
	pos += 4

	// Attributes
	binary.BigEndian.PutUint16(buf[pos:], uint16(b.Attributes))
	pos += 2

	// Last offset delta
	binary.BigEndian.PutUint32(buf[pos:], uint32(b.LastOffsetDelta))
	pos += 4

	// First timestamp
	binary.BigEndian.PutUint64(buf[pos:], uint64(b.FirstTimestamp))
	pos += 8

	// Max timestamp
	binary.BigEndian.PutUint64(buf[pos:], uint64(b.MaxTimestamp))
	pos += 8

	// Producer ID
	binary.BigEndian.PutUint64(buf[pos:], uint64(b.ProducerId))
	pos += 8

	// Producer epoch
	binary.BigEndian.PutUint16(buf[pos:], uint16(b.ProducerEpoch))
	pos += 2

	// Base sequence
	binary.BigEndian.PutUint32(buf[pos:], uint32(b.BaseSequence))
	pos += 4

	// Record count
	binary.BigEndian.PutUint32(buf[pos:], uint32(len(b.Records)))
	pos += 4

	// Records
	copy(buf[pos:], recordsData)

	// Calculate and fill CRC-32C / Castagnoli (from attributes to end)
	b.CRC = crc32.Checksum(buf[crcPos+4:], crc32cTable)
	binary.BigEndian.PutUint32(buf[crcPos:], b.CRC)

	return buf
}

// encodeRecords serializes records using Kafka record format
func (b *RecordBatch) encodeRecords() []byte {
	var result []byte
	for _, r := range b.Records {
		result = append(result, encodeRecord(&r)...)
	}
	return result
}

// encodeRecord serializes a single record
func encodeRecord(r *Record) []byte {
	// Calculate size
	keyLen := len(r.Key)
	if r.Key == nil {
		keyLen = -1
	}
	valueLen := len(r.Value)
	if r.Value == nil {
		valueLen = -1
	}

	// Build record body first
	var body []byte

	// Attributes (int8, unused in v2)
	body = append(body, 0)

	// Timestamp delta (varint)
	body = appendVarint(body, r.TimestampDelta)

	// Offset delta (varint)
	body = appendVarint(body, int64(r.OffsetDelta))

	// Key length (varint)
	body = appendVarint(body, int64(keyLen))

	// Key
	if r.Key != nil {
		body = append(body, r.Key...)
	}

	// Value length (varint)
	body = appendVarint(body, int64(valueLen))

	// Value
	if r.Value != nil {
		body = append(body, r.Value...)
	}

	// Headers count (varint)
	body = appendVarint(body, int64(len(r.Headers)))

	// Headers
	for _, h := range r.Headers {
		// Header key length (varint)
		body = appendVarint(body, int64(len(h.Key)))
		// Header key
		body = append(body, h.Key...)
		// Header value length (varint)
		if h.Value == nil {
			body = appendVarint(body, -1)
		} else {
			body = appendVarint(body, int64(len(h.Value)))
			body = append(body, h.Value...)
		}
	}

	// Prepend length
	var result []byte
	result = appendVarint(result, int64(len(body)))
	result = append(result, body...)

	return result
}

// appendVarint appends a zigzag-encoded varint
func appendVarint(buf []byte, val int64) []byte {
	// Zigzag encode
	uval := uint64((val << 1) ^ (val >> 63))
	for uval >= 0x80 {
		buf = append(buf, byte(uval)|0x80)
		uval >>= 7
	}
	buf = append(buf, byte(uval))
	return buf
}

// ValidateRecordBatchHeader validates CRC and extracts header fields from raw batch bytes
// without decoding individual records. Returns recordCount, maxTimestamp, lastOffsetDelta.
func ValidateRecordBatchHeader(data []byte) (recordCount int32, maxTimestamp int64, lastOffsetDelta int32, err error) {
	if len(data) < RecordBatchHeaderSize {
		return 0, 0, 0, ErrCorruptedData
	}

	// Batch length check
	batchLength := int32(binary.BigEndian.Uint32(data[8:]))
	totalLen := 12 + int(batchLength)
	if totalLen > len(data) {
		return 0, 0, 0, ErrCorruptedData
	}

	// CRC validation (covers from attributes at offset 21 to end)
	storedCRC := binary.BigEndian.Uint32(data[17:])
	computedCRC := crc32.Checksum(data[21:totalLen], crc32cTable)
	if computedCRC != storedCRC {
		return 0, 0, 0, ErrCRCMismatch
	}

	// Extract fields from fixed header positions
	lastOffsetDelta = int32(binary.BigEndian.Uint32(data[23:]))
	maxTimestamp = int64(binary.BigEndian.Uint64(data[35:]))
	recordCount = int32(binary.BigEndian.Uint32(data[57:]))

	return recordCount, maxTimestamp, lastOffsetDelta, nil
}

// PatchBaseOffset overwrites the base offset in raw record batch data (bytes 0-7).
// BaseOffset is NOT covered by CRC, so no CRC recalculation is needed.
func PatchBaseOffset(data []byte, baseOffset int64) {
	binary.BigEndian.PutUint64(data[0:8], uint64(baseOffset))
}

// DecodeRecordBatch decodes a record batch from bytes
func DecodeRecordBatch(data []byte) (*RecordBatch, error) {
	if len(data) < RecordBatchHeaderSize {
		return nil, ErrCorruptedData
	}

	batch := &RecordBatch{}
	pos := 0

	// Base offset
	batch.BaseOffset = int64(binary.BigEndian.Uint64(data[pos:]))
	pos += 8

	// Batch length
	batch.BatchLength = int32(binary.BigEndian.Uint32(data[pos:]))
	pos += 4

	// Partition leader epoch
	batch.PartitionLeaderEpoch = int32(binary.BigEndian.Uint32(data[pos:]))
	pos += 4

	// Magic
	batch.Magic = int8(data[pos])
	pos++

	// CRC
	batch.CRC = binary.BigEndian.Uint32(data[pos:])
	pos += 4

	// Verify CRC-32C (Castagnoli) - used by Kafka
	// CRC covers from attributes to end of batch (batchLength - 9 bytes)
	// batchLength counts from partitionLeaderEpoch(4) + magic(1) + crc(4) + payload
	crcPayloadEnd := 12 + int(batch.BatchLength) // 12 = baseOffset(8) + batchLength(4)
	if crcPayloadEnd > len(data) {
		return nil, ErrCorruptedData
	}
	computedCRC := crc32.Checksum(data[pos:crcPayloadEnd], crc32cTable)
	if computedCRC != batch.CRC {
		return nil, ErrCRCMismatch
	}

	// Attributes
	batch.Attributes = int16(binary.BigEndian.Uint16(data[pos:]))
	pos += 2

	// Last offset delta
	batch.LastOffsetDelta = int32(binary.BigEndian.Uint32(data[pos:]))
	pos += 4

	// First timestamp
	batch.FirstTimestamp = int64(binary.BigEndian.Uint64(data[pos:]))
	pos += 8

	// Max timestamp
	batch.MaxTimestamp = int64(binary.BigEndian.Uint64(data[pos:]))
	pos += 8

	// Producer ID
	batch.ProducerId = int64(binary.BigEndian.Uint64(data[pos:]))
	pos += 8

	// Producer epoch
	batch.ProducerEpoch = int16(binary.BigEndian.Uint16(data[pos:]))
	pos += 2

	// Base sequence
	batch.BaseSequence = int32(binary.BigEndian.Uint32(data[pos:]))
	pos += 4

	// Record count
	recordCount := int32(binary.BigEndian.Uint32(data[pos:]))
	pos += 4

	// Decode records
	batch.Records = make([]Record, 0, recordCount)
	for i := int32(0); i < recordCount; i++ {
		record, bytesRead, err := decodeRecord(data[pos:])
		if err != nil {
			return nil, err
		}
		batch.Records = append(batch.Records, *record)
		pos += bytesRead
	}

	return batch, nil
}

// decodeRecord decodes a single record
func decodeRecord(data []byte) (*Record, int, error) {
	pos := 0

	// Record length
	recordLen, n := readVarint(data[pos:])
	if n <= 0 {
		return nil, 0, ErrCorruptedData
	}
	pos += n

	startPos := pos

	// Attributes (skip)
	pos++

	// Timestamp delta
	timestampDelta, n := readVarint(data[pos:])
	if n <= 0 {
		return nil, 0, ErrCorruptedData
	}
	pos += n

	// Offset delta
	offsetDelta, n := readVarint(data[pos:])
	if n <= 0 {
		return nil, 0, ErrCorruptedData
	}
	pos += n

	// Key length
	keyLen, n := readVarint(data[pos:])
	if n <= 0 {
		return nil, 0, ErrCorruptedData
	}
	pos += n

	// Key
	var key []byte
	if keyLen >= 0 {
		key = make([]byte, keyLen)
		copy(key, data[pos:pos+int(keyLen)])
		pos += int(keyLen)
	}

	// Value length
	valueLen, n := readVarint(data[pos:])
	if n <= 0 {
		return nil, 0, ErrCorruptedData
	}
	pos += n

	// Value
	var value []byte
	if valueLen >= 0 {
		value = make([]byte, valueLen)
		copy(value, data[pos:pos+int(valueLen)])
		pos += int(valueLen)
	}

	// Headers count
	headerCount, n := readVarint(data[pos:])
	if n <= 0 {
		return nil, 0, ErrCorruptedData
	}
	pos += n

	// Headers
	headers := make([]RecordHeader, 0, headerCount)
	for i := int64(0); i < headerCount; i++ {
		// Header key length
		hKeyLen, n := readVarint(data[pos:])
		if n <= 0 {
			return nil, 0, ErrCorruptedData
		}
		pos += n

		// Header key
		hKey := string(data[pos : pos+int(hKeyLen)])
		pos += int(hKeyLen)

		// Header value length
		hValueLen, n := readVarint(data[pos:])
		if n <= 0 {
			return nil, 0, ErrCorruptedData
		}
		pos += n

		// Header value
		var hValue []byte
		if hValueLen >= 0 {
			hValue = make([]byte, hValueLen)
			copy(hValue, data[pos:pos+int(hValueLen)])
			pos += int(hValueLen)
		}

		headers = append(headers, RecordHeader{Key: hKey, Value: hValue})
	}

	// Verify record length
	if int64(pos-startPos) != recordLen {
		return nil, 0, ErrCorruptedData
	}

	return &Record{
		OffsetDelta:    int32(offsetDelta),
		TimestampDelta: timestampDelta,
		Key:            key,
		Value:          value,
		Headers:        headers,
	}, pos, nil
}

// readVarint reads a zigzag-encoded varint
func readVarint(data []byte) (int64, int) {
	var result uint64
	var shift uint
	var pos int

	for {
		if pos >= len(data) {
			return 0, -1
		}
		b := data[pos]
		pos++
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
		if shift >= 64 {
			return 0, -1
		}
	}

	// Zigzag decode
	val := int64((result >> 1) ^ -(result & 1))
	return val, pos
}
