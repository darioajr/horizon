package protocol

import (
	"encoding/binary"
	"errors"
	"io"
)

// ErrBufferTooShort indicates insufficient data in the buffer
var ErrBufferTooShort = errors.New("buffer too short")

// Reader provides methods for reading Kafka protocol data types
type Reader struct {
	data []byte
	pos  int
}

// NewReader creates a new protocol reader from a byte slice
func NewReader(data []byte) *Reader {
	return &Reader{data: data, pos: 0}
}

// Remaining returns the number of unread bytes
func (r *Reader) Remaining() int {
	return len(r.data) - r.pos
}

// Position returns the current read position
func (r *Reader) Position() int {
	return r.pos
}

// Skip advances the read position
func (r *Reader) Skip(n int) error {
	if r.pos+n > len(r.data) {
		return ErrBufferTooShort
	}
	r.pos += n
	return nil
}

// Peek returns bytes without advancing position
func (r *Reader) Peek(n int) ([]byte, error) {
	if r.pos+n > len(r.data) {
		return nil, ErrBufferTooShort
	}
	return r.data[r.pos : r.pos+n], nil
}

// ReadInt8 reads a signed 8-bit integer
func (r *Reader) ReadInt8() (int8, error) {
	if r.pos+1 > len(r.data) {
		return 0, ErrBufferTooShort
	}
	val := int8(r.data[r.pos])
	r.pos++
	return val, nil
}

// ReadInt16 reads a signed 16-bit integer (big-endian)
func (r *Reader) ReadInt16() (int16, error) {
	if r.pos+2 > len(r.data) {
		return 0, ErrBufferTooShort
	}
	val := int16(binary.BigEndian.Uint16(r.data[r.pos:]))
	r.pos += 2
	return val, nil
}

// ReadInt32 reads a signed 32-bit integer (big-endian)
func (r *Reader) ReadInt32() (int32, error) {
	if r.pos+4 > len(r.data) {
		return 0, ErrBufferTooShort
	}
	val := int32(binary.BigEndian.Uint32(r.data[r.pos:]))
	r.pos += 4
	return val, nil
}

// ReadInt64 reads a signed 64-bit integer (big-endian)
func (r *Reader) ReadInt64() (int64, error) {
	if r.pos+8 > len(r.data) {
		return 0, ErrBufferTooShort
	}
	val := int64(binary.BigEndian.Uint64(r.data[r.pos:]))
	r.pos += 8
	return val, nil
}

// ReadUint16 reads an unsigned 16-bit integer (big-endian)
func (r *Reader) ReadUint16() (uint16, error) {
	if r.pos+2 > len(r.data) {
		return 0, ErrBufferTooShort
	}
	val := binary.BigEndian.Uint16(r.data[r.pos:])
	r.pos += 2
	return val, nil
}

// ReadUint32 reads an unsigned 32-bit integer (big-endian)
func (r *Reader) ReadUint32() (uint32, error) {
	if r.pos+4 > len(r.data) {
		return 0, ErrBufferTooShort
	}
	val := binary.BigEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return val, nil
}

// ReadVarint reads a variable-length signed integer
func (r *Reader) ReadVarint() (int32, error) {
	uval, err := r.ReadUvarint()
	if err != nil {
		return 0, err
	}
	// Zigzag decode
	return int32((uval >> 1) ^ -(uval & 1)), nil
}

// ReadVarlong reads a variable-length signed 64-bit integer
func (r *Reader) ReadVarlong() (int64, error) {
	uval, err := r.ReadUvarlong()
	if err != nil {
		return 0, err
	}
	// Zigzag decode
	return int64((uval >> 1) ^ -(uval & 1)), nil
}

// ReadUvarint reads an unsigned variable-length integer
func (r *Reader) ReadUvarint() (uint32, error) {
	var result uint32
	var shift uint
	for {
		if r.pos >= len(r.data) {
			return 0, ErrBufferTooShort
		}
		b := r.data[r.pos]
		r.pos++
		result |= uint32(b&0x7F) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
		if shift >= 35 {
			return 0, errors.New("varint overflow")
		}
	}
	return result, nil
}

// ReadUvarlong reads an unsigned variable-length 64-bit integer
func (r *Reader) ReadUvarlong() (uint64, error) {
	var result uint64
	var shift uint
	for {
		if r.pos >= len(r.data) {
			return 0, ErrBufferTooShort
		}
		b := r.data[r.pos]
		r.pos++
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
		if shift >= 70 {
			return 0, errors.New("varlong overflow")
		}
	}
	return result, nil
}

// ReadString reads a length-prefixed string (INT16 length)
func (r *Reader) ReadString() (string, error) {
	length, err := r.ReadInt16()
	if err != nil {
		return "", err
	}
	if length < 0 {
		return "", nil // null string
	}
	if r.pos+int(length) > len(r.data) {
		return "", ErrBufferTooShort
	}
	s := string(r.data[r.pos : r.pos+int(length)])
	r.pos += int(length)
	return s, nil
}

// ReadNullableString reads a nullable length-prefixed string
func (r *Reader) ReadNullableString() (*string, error) {
	length, err := r.ReadInt16()
	if err != nil {
		return nil, err
	}
	if length < 0 {
		return nil, nil // null
	}
	if r.pos+int(length) > len(r.data) {
		return nil, ErrBufferTooShort
	}
	s := string(r.data[r.pos : r.pos+int(length)])
	r.pos += int(length)
	return &s, nil
}

// ReadCompactString reads a compact string (UNSIGNED_VARINT length)
func (r *Reader) ReadCompactString() (string, error) {
	length, err := r.ReadUvarint()
	if err != nil {
		return "", err
	}
	if length == 0 {
		return "", nil // empty string
	}
	actualLen := int(length - 1)
	if r.pos+actualLen > len(r.data) {
		return "", ErrBufferTooShort
	}
	s := string(r.data[r.pos : r.pos+actualLen])
	r.pos += actualLen
	return s, nil
}

// ReadCompactNullableString reads a compact nullable string
func (r *Reader) ReadCompactNullableString() (*string, error) {
	length, err := r.ReadUvarint()
	if err != nil {
		return nil, err
	}
	if length == 0 {
		return nil, nil // null
	}
	actualLen := int(length - 1)
	if r.pos+actualLen > len(r.data) {
		return nil, ErrBufferTooShort
	}
	s := string(r.data[r.pos : r.pos+actualLen])
	r.pos += actualLen
	return &s, nil
}

// ReadBytes reads length-prefixed bytes (INT32 length)
func (r *Reader) ReadBytes() ([]byte, error) {
	length, err := r.ReadInt32()
	if err != nil {
		return nil, err
	}
	if length < 0 {
		return nil, nil // null bytes
	}
	if r.pos+int(length) > len(r.data) {
		return nil, ErrBufferTooShort
	}
	bytes := make([]byte, length)
	copy(bytes, r.data[r.pos:r.pos+int(length)])
	r.pos += int(length)
	return bytes, nil
}

// ReadSlice returns a sub-slice of the underlying buffer without copying.
// The returned slice is valid only as long as the Reader's data is not reused.
// Use for zero-copy paths where the caller writes data to disk before the buffer is freed.
func (r *Reader) ReadSlice() ([]byte, error) {
	length, err := r.ReadInt32()
	if err != nil {
		return nil, err
	}
	if length < 0 {
		return nil, nil
	}
	if r.pos+int(length) > len(r.data) {
		return nil, ErrBufferTooShort
	}
	slice := r.data[r.pos : r.pos+int(length)]
	r.pos += int(length)
	return slice, nil
}

// ReadNullableBytes reads nullable length-prefixed bytes
func (r *Reader) ReadNullableBytes() ([]byte, error) {
	return r.ReadBytes()
}

// ReadCompactBytes reads compact bytes (UNSIGNED_VARINT length)
func (r *Reader) ReadCompactBytes() ([]byte, error) {
	length, err := r.ReadUvarint()
	if err != nil {
		return nil, err
	}
	if length == 0 {
		return nil, nil // empty/null
	}
	actualLen := int(length - 1)
	if r.pos+actualLen > len(r.data) {
		return nil, ErrBufferTooShort
	}
	bytes := make([]byte, actualLen)
	copy(bytes, r.data[r.pos:r.pos+actualLen])
	r.pos += actualLen
	return bytes, nil
}

// ReadRawBytes reads exactly n bytes without length prefix
func (r *Reader) ReadRawBytes(n int) ([]byte, error) {
	if r.pos+n > len(r.data) {
		return nil, ErrBufferTooShort
	}
	bytes := make([]byte, n)
	copy(bytes, r.data[r.pos:r.pos+n])
	r.pos += n
	return bytes, nil
}

// ReadBool reads a boolean value
func (r *Reader) ReadBool() (bool, error) {
	val, err := r.ReadInt8()
	if err != nil {
		return false, err
	}
	return val != 0, nil
}

// ReadArrayLen reads array length (INT32)
func (r *Reader) ReadArrayLen() (int32, error) {
	return r.ReadInt32()
}

// ReadCompactArrayLen reads compact array length (UNSIGNED_VARINT)
func (r *Reader) ReadCompactArrayLen() (int, error) {
	length, err := r.ReadUvarint()
	if err != nil {
		return 0, err
	}
	if length == 0 {
		return -1, nil // null array
	}
	return int(length - 1), nil
}

// ReadTaggedFields reads tagged fields and discards them
func (r *Reader) ReadTaggedFields() error {
	numTags, err := r.ReadUvarint()
	if err != nil {
		return err
	}
	for i := uint32(0); i < numTags; i++ {
		// Read tag
		if _, err := r.ReadUvarint(); err != nil {
			return err
		}
		// Read size and skip data
		size, err := r.ReadUvarint()
		if err != nil {
			return err
		}
		if err := r.Skip(int(size)); err != nil {
			return err
		}
	}
	return nil
}

// ReadFromStream reads n bytes from an io.Reader
func ReadFromStream(reader io.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	_, err := io.ReadFull(reader, buf)
	if err != nil {
		return nil, err
	}
	return buf, nil
}
