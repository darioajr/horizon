package server

import (
	"sync"

	"horizon/internal/protocol"
)

// writerPool reuses protocol Writers to reduce GC pressure
var writerPool = sync.Pool{
	New: func() any {
		return protocol.NewWriter(1024)
	},
}

// Request represents a Kafka protocol request
type Request struct {
	ApiKey        protocol.ApiKey
	ApiVersion    int16
	CorrelationID int32
	ClientID      string
	Reader        *protocol.Reader
	bufPtr        *[]byte // reference to pooled buffer for recycling
}

// Response represents a Kafka protocol response
type Response struct {
	CorrelationID int32
	Writer        *protocol.Writer
}

// NewResponse creates a new response using a pooled Writer
func NewResponse(correlationID int32) *Response {
	w := writerPool.Get().(*protocol.Writer)
	w.Reset()
	w.WriteInt32(correlationID)
	return &Response{
		CorrelationID: correlationID,
		Writer:        w,
	}
}

// Release returns the Writer to the pool for reuse
func (r *Response) Release() {
	if r.Writer != nil {
		writerPool.Put(r.Writer)
		r.Writer = nil
	}
}
