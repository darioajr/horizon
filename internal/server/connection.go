package server

import (
	"bufio"
	"encoding/binary"
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
	// defaultPipelineDepth is the default response channel size.
	defaultPipelineDepth = 64
)

// reqBufPool pools request data buffers to reduce GC pressure.
// Most produce requests are ~1MB (batch.size), so start with 1MB buffers.
var reqBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 1024*1024)
		return &b
	},
}

// pipelinedResponse pairs a response with its request buffer for deferred recycling.
type pipelinedResponse struct {
	resp   *Response
	bufPtr *[]byte
}

// Connection represents a client connection
type Connection struct {
	id            int64
	conn          net.Conn
	reader        *bufio.Reader
	writer        *bufio.Writer
	handler       *RequestHandler
	closed        bool
	mu            sync.Mutex
	sizeBuf       [4]byte // reusable buffer for size prefix
	pipelineDepth int     // response pipeline depth (0 = legacy sequential)
}

// NewConnection creates a new connection
func NewConnection(id int64, conn net.Conn, handler *RequestHandler) *Connection {
	// Set TCP_NODELAY to minimize latency (disable Nagle's algorithm)
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	return &Connection{
		id:            id,
		conn:          conn,
		reader:        bufio.NewReaderSize(conn, connReadBufSize),
		writer:        bufio.NewWriterSize(conn, connWriteBufSize),
		handler:       handler,
		pipelineDepth: defaultPipelineDepth,
	}
}

// NewConnectionWithPipeline creates a connection with a custom pipeline depth.
func NewConnectionWithPipeline(id int64, conn net.Conn, handler *RequestHandler, depth int) *Connection {
	c := NewConnection(id, conn, handler)
	if depth > 0 {
		c.pipelineDepth = depth
	}
	return c
}

// Handle handles requests on the connection using an adaptive pipeline.
//
// Two goroutines cooperate:
//   - Reader goroutine: reads requests, dispatches to handler, sends
//     responses to the pipeline channel.
//   - Writer goroutine: drains the pipeline channel and flushes to the
//     socket. It adaptively batches: only calls Flush() when no more
//     responses are immediately available (or when the write buffer is
//     large enough), achieving automatic coalescing under high load
//     while maintaining low latency under light load.
func (c *Connection) Handle() {
	defer c.conn.Close()

	respCh := make(chan pipelinedResponse, c.pipelineDepth)

	// Writer goroutine — drains respCh and flushes adaptively.
	var writerDone sync.WaitGroup
	writerDone.Add(1)
	go func() {
		defer writerDone.Done()
		c.writeLoop(respCh)
	}()

	// Reader goroutine (runs on this goroutine).
	for {
		req, err := c.readRequest()
		if err != nil {
			close(respCh)
			writerDone.Wait()
			return
		}

		resp := c.handler.HandleRequest(req)

		respCh <- pipelinedResponse{resp: resp, bufPtr: req.bufPtr}
		req.bufPtr = nil // ownership transferred to writer
	}
}

// writeLoop is the writer goroutine. It writes responses and flushes
// adaptively: if more responses are queued it keeps writing before
// flushing, coalescing into fewer TCP segments under load.
func (c *Connection) writeLoop(respCh <-chan pipelinedResponse) {
	for pr := range respCh {
		if err := c.writeResponseData(pr); err != nil {
			// Drain remaining to unblock senders and recycle buffers.
			for pr := range respCh {
				c.recyclePipelined(pr)
			}
			return
		}

		// Adaptive flush: keep writing if more responses are queued.
		flushed := false
		for !flushed {
			select {
			case pr, ok := <-respCh:
				if !ok {
					// Channel closed — flush final data.
					_ = c.writer.Flush()
					return
				}
				if err := c.writeResponseData(pr); err != nil {
					for pr := range respCh {
						c.recyclePipelined(pr)
					}
					return
				}
			default:
				// No more queued responses — flush now for low latency.
				if err := c.writer.Flush(); err != nil {
					return
				}
				flushed = true
			}
		}
	}
}

// writeResponseData writes a single response to the buffered writer
// (without flushing) and recycles its resources.
func (c *Connection) writeResponseData(pr pipelinedResponse) error {
	data := pr.resp.Writer.Bytes()

	binary.BigEndian.PutUint32(c.sizeBuf[:], uint32(len(data)))
	if _, err := c.writer.Write(c.sizeBuf[:]); err != nil {
		c.recyclePipelined(pr)
		return err
	}
	if _, err := c.writer.Write(data); err != nil {
		c.recyclePipelined(pr)
		return err
	}

	// Recycle resources.
	pr.resp.Release()
	if pr.bufPtr != nil {
		reqBufPool.Put(pr.bufPtr)
	}
	return nil
}

// recyclePipelined returns pooled resources without writing.
func (c *Connection) recyclePipelined(pr pipelinedResponse) {
	if pr.resp != nil {
		pr.resp.Release()
	}
	if pr.bufPtr != nil {
		reqBufPool.Put(pr.bufPtr)
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
