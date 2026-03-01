package server

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"

	"horizon/internal/protocol"
)

const (
	// connReadBufSize is the read buffer size (64KB)
	connReadBufSize = 64 * 1024
	// connWriteBufSize is the write buffer size (64KB)
	connWriteBufSize = 64 * 1024
)

// reqBufPool pools request data buffers to reduce GC pressure.
// Most produce requests are ~1MB (batch.size), so start with 1MB buffers.
var reqBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 1024*1024)
		return &b
	},
}

// Connection represents a client connection
type Connection struct {
	id      int64
	conn    net.Conn
	reader  *bufio.Reader
	writer  *bufio.Writer
	handler *RequestHandler
	closed  bool
	mu      sync.Mutex
	sizeBuf [4]byte // reusable buffer for size prefix
}

// NewConnection creates a new connection
func NewConnection(id int64, conn net.Conn, handler *RequestHandler) *Connection {
	// Set TCP_NODELAY to minimize latency (disable Nagle's algorithm)
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	return &Connection{
		id:      id,
		conn:    conn,
		reader:  bufio.NewReaderSize(conn, connReadBufSize),
		writer:  bufio.NewWriterSize(conn, connWriteBufSize),
		handler: handler,
	}
}

// Handle handles requests on the connection.
// Sequential processing with buffered I/O provides optimal throughput
// for the common case of single-partition requests (sticky partitioning).
func (c *Connection) Handle() {
	defer c.conn.Close()

	for {
		req, err := c.readRequest()
		if err != nil {
			return
		}

		resp := c.handler.HandleRequest(req)

		// Return request buffer to pool
		if req.bufPtr != nil {
			reqBufPool.Put(req.bufPtr)
			req.bufPtr = nil
		}

		if err := c.writeResponse(resp); err != nil {
			return
		}
	}
}

// readRequest reads a request from the connection
func (c *Connection) readRequest() (*Request, error) {
	// Read size (4 bytes) using reusable buffer
	if _, err := io.ReadFull(c.reader, c.sizeBuf[:]); err != nil {
		return nil, err
	}
	size := int32(binary.BigEndian.Uint32(c.sizeBuf[:]))

	// Get buffer from pool or allocate if too small
	bufPtr := reqBufPool.Get().(*[]byte)
	buf := *bufPtr
	if int32(len(buf)) < size {
		buf = make([]byte, size)
	}
	data := buf[:size]
	if _, err := io.ReadFull(c.reader, data); err != nil {
		*bufPtr = buf
		reqBufPool.Put(bufPtr)
		return nil, err
	}

	reader := protocol.NewReader(data)

	// Parse header
	apiKey, _ := reader.ReadInt16()
	apiVersion, _ := reader.ReadInt16()
	correlationID, _ := reader.ReadInt32()
	clientID, _ := reader.ReadNullableString()

	clientIDStr := ""
	if clientID != nil {
		clientIDStr = *clientID
	}

	// Store buffer pointer for pool recycling
	*bufPtr = buf

	return &Request{
		ApiKey:        protocol.ApiKey(apiKey),
		ApiVersion:    apiVersion,
		CorrelationID: correlationID,
		ClientID:      clientIDStr,
		Reader:        reader,
		bufPtr:        bufPtr,
	}, nil
}

// writeResponse writes a response to the connection
func (c *Connection) writeResponse(resp *Response) error {
	data := resp.Writer.Bytes()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("connection closed")
	}

	// Write size prefix + data to buffered writer (single flush)
	binary.BigEndian.PutUint32(c.sizeBuf[:], uint32(len(data)))
	if _, err := c.writer.Write(c.sizeBuf[:]); err != nil {
		return err
	}
	if _, err := c.writer.Write(data); err != nil {
		return err
	}
	// Return writer to pool after data is written to bufio
	resp.Release()
	// Flush to socket
	return c.writer.Flush()
}

// Close closes the connection
func (c *Connection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}
