package server

import (
	"horizon/internal/protocol"
	"horizon/internal/storage"
)

// handleCreateTopics handles CreateTopics request
func (h *RequestHandler) handleCreateTopics(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read request
	topicCount, _ := req.Reader.ReadArrayLen()

	type createResult struct {
		name              string
		numPartitions     int32
		replicationFactor int16
		errorCode         protocol.ErrorCode
		errorMsg          string
	}

	var results []createResult

	for i := int32(0); i < topicCount; i++ {
		name, _ := req.Reader.ReadString()
		numPartitions, _ := req.Reader.ReadInt32()
		replicationFactor, _ := req.Reader.ReadInt16()

		// Skip assignments
		assignCount, _ := req.Reader.ReadArrayLen()
		for j := int32(0); j < assignCount; j++ {
			_, _ = req.Reader.ReadInt32() // partition
			replicaCount, _ := req.Reader.ReadArrayLen()
			for k := int32(0); k < replicaCount; k++ {
				_, _ = req.Reader.ReadInt32() // replica
			}
		}

		// Skip configs
		configCount, _ := req.Reader.ReadArrayLen()
		for j := int32(0); j < configCount; j++ {
			_, _ = req.Reader.ReadString()         // name
			_, _ = req.Reader.ReadNullableString()  // value
		}

		// Create topic
		err := h.broker.CreateTopic(name, numPartitions, replicationFactor)

		r := createResult{name: name, numPartitions: numPartitions, replicationFactor: replicationFactor}
		if err != nil {
			if err == storage.ErrTopicExists {
				r.errorCode = protocol.ErrTopicAlreadyExists
				r.errorMsg = "Topic '" + name + "' already exists."
			} else {
				r.errorCode = protocol.ErrUnknown
				r.errorMsg = err.Error()
			}
		} else {
			r.errorCode = protocol.ErrNone
		}
		results = append(results, r)
	}

	// Read remaining request fields
	_, _ = req.Reader.ReadInt32() // timeout_ms
	if req.ApiVersion >= 1 {
		_, _ = req.Reader.ReadBool() // validate_only
	}

	// Throttle time (v2+)
	if req.ApiVersion >= 2 {
		w.WriteInt32(0)
	}

	// Write response
	w.WriteArrayLen(int32(len(results)))
	for _, r := range results {
		w.WriteString(r.name)
		w.WriteInt16(int16(r.errorCode))
		if req.ApiVersion >= 1 {
			if r.errorMsg != "" {
				w.WriteNullableString(&r.errorMsg)
			} else {
				w.WriteNullableString(nil)
			}
		}
		// Additional fields (v5+)
		if req.ApiVersion >= 5 {
			w.WriteInt32(r.numPartitions)       // num_partitions
			w.WriteInt16(r.replicationFactor)   // replication_factor
			w.WriteArrayLen(0)               // configs (empty)
		}
	}

	return resp
}

// handleDeleteTopics handles DeleteTopics request
func (h *RequestHandler) handleDeleteTopics(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read request
	topicCount, _ := req.Reader.ReadArrayLen()

	type deleteResult struct {
		name      string
		errorCode protocol.ErrorCode
	}

	var results []deleteResult

	for i := int32(0); i < topicCount; i++ {
		name, _ := req.Reader.ReadString()

		err := h.broker.DeleteTopic(name)

		r := deleteResult{name: name}
		if err != nil {
			if err == storage.ErrTopicNotFound {
				r.errorCode = protocol.ErrUnknownTopicOrPartition
			} else {
				r.errorCode = protocol.ErrUnknown
			}
		} else {
			r.errorCode = protocol.ErrNone
		}
		results = append(results, r)
	}

	// Throttle time (v1+)
	if req.ApiVersion >= 1 {
		w.WriteInt32(0)
	}

	// Write response
	w.WriteArrayLen(int32(len(results)))
	for _, r := range results {
		w.WriteString(r.name)
		w.WriteInt16(int16(r.errorCode))
	}

	return resp
}
