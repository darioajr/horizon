package server

import (
	"horizon/internal/protocol"
	"horizon/internal/storage"
)

// handleFetch handles Fetch request
func (h *RequestHandler) handleFetch(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read request
	_, _ = req.Reader.ReadInt32() // replica_id
	_, _ = req.Reader.ReadInt32() // max_wait
	_, _ = req.Reader.ReadInt32() // min_bytes

	var maxBytes int32 = 1024 * 1024 // default 1MB
	if req.ApiVersion >= 3 {
		maxBytes, _ = req.Reader.ReadInt32()
	}
	if req.ApiVersion >= 4 {
		_, _ = req.Reader.ReadInt8() // isolation_level
	}
	if req.ApiVersion >= 7 {
		_, _ = req.Reader.ReadInt32() // session_id
		_, _ = req.Reader.ReadInt32() // session_epoch
	}

	topicCount, _ := req.Reader.ReadArrayLen()

	type fetchPartition struct {
		partition int32
		offset    int64
		maxBytes  int32
		batches   []*storage.RecordBatch
		errorCode protocol.ErrorCode
		hwm       int64
	}
	type fetchTopic struct {
		topic      string
		partitions []fetchPartition
	}

	var results []fetchTopic

	// Get cluster router (nil in standalone mode)
	cluster := h.broker.GetCluster()

	for i := int32(0); i < topicCount; i++ {
		topic, _ := req.Reader.ReadString()
		partCount, _ := req.Reader.ReadArrayLen()

		ft := fetchTopic{topic: topic}

		for j := int32(0); j < partCount; j++ {
			partition, _ := req.Reader.ReadInt32()
			if req.ApiVersion >= 9 {
				_, _ = req.Reader.ReadInt32() // current_leader_epoch
			}
			offset, _ := req.Reader.ReadInt64()
			if req.ApiVersion >= 5 {
				_, _ = req.Reader.ReadInt64() // log_start_offset
			}
			partMaxBytes, _ := req.Reader.ReadInt32()

			fp := fetchPartition{
				partition: partition,
				offset:    offset,
				maxBytes:  partMaxBytes,
			}

			// In cluster mode, if this node is not the leader, forward or error
			if cluster != nil && !cluster.IsPartitionLocal(topic, partition) {
				leaderID, _, _, ok := cluster.GetPartitionLeader(topic, partition)
				if !ok {
					fp.errorCode = protocol.ErrNotLeaderForPartition
				} else {
					remoteBatches, fwdErr := cluster.ForwardFetch(leaderID, topic, partition, offset, int64(partMaxBytes))
					if fwdErr != nil {
						fp.errorCode = protocol.ErrNotLeaderForPartition
					} else {
						fp.batches = remoteBatches
						fp.errorCode = protocol.ErrNone
					}
				}
			} else {
				// Local fetch
				batches, err := h.broker.Fetch(topic, partition, offset, int64(partMaxBytes))
				if err != nil {
					if err == storage.ErrOffsetOutOfRange {
						fp.errorCode = protocol.ErrOffsetOutOfRange
					} else if err == storage.ErrTopicNotFound || err == storage.ErrPartitionNotFound {
						fp.errorCode = protocol.ErrUnknownTopicOrPartition
					} else {
						fp.errorCode = protocol.ErrUnknown
					}
				} else {
					fp.batches = batches
					fp.errorCode = protocol.ErrNone
				}
				// Set high watermark from partition metadata
				if pr, pErr := h.broker.GetPartition(topic, partition); pErr == nil {
					fp.hwm = pr.HighWatermark()
				}
			}

			ft.partitions = append(ft.partitions, fp)
		}

		results = append(results, ft)
	}

	// Forgotten topics data (v7+)
	if req.ApiVersion >= 7 {
		forgottenCount, _ := req.Reader.ReadArrayLen()
		for i := int32(0); i < forgottenCount; i++ {
			_, _ = req.Reader.ReadString()       // topic
			partCount, _ := req.Reader.ReadArrayLen()
			for j := int32(0); j < partCount; j++ {
				_, _ = req.Reader.ReadInt32() // partition
			}
		}
	}

	// Rack ID (v11+)
	if req.ApiVersion >= 11 {
		_, _ = req.Reader.ReadString()
	}

	// Throttle time (v1+)
	if req.ApiVersion >= 1 {
		w.WriteInt32(0)
	}

	// Error code (v7+)
	if req.ApiVersion >= 7 {
		w.WriteInt16(int16(protocol.ErrNone))
		w.WriteInt32(0) // session_id
	}

	// Write topics
	w.WriteArrayLen(int32(len(results)))
	for _, ft := range results {
		w.WriteString(ft.topic)
		w.WriteArrayLen(int32(len(ft.partitions)))
		for _, fp := range ft.partitions {
			w.WriteInt32(fp.partition)
			w.WriteInt16(int16(fp.errorCode))
			w.WriteInt64(fp.hwm)

			// Last stable offset (v4+)
			if req.ApiVersion >= 4 {
				w.WriteInt64(fp.hwm)
			}

			// Log start offset (v5+)
			if req.ApiVersion >= 5 {
				w.WriteInt64(0)
			}

			// Aborted transactions (v4+)
			if req.ApiVersion >= 4 {
				w.WriteArrayLen(0)
			}

			// Preferred read replica (v11+)
			if req.ApiVersion >= 11 {
				w.WriteInt32(-1) // no preference
			}

			// Record batches
			if len(fp.batches) > 0 {
				var data []byte
				for _, batch := range fp.batches {
					data = append(data, batch.Encode()...)
				}
				w.WriteBytes(data)
			} else {
				w.WriteBytes(nil)
			}
		}
	}

	_ = maxBytes
	return resp
}

// handleListOffsets handles ListOffsets request
func (h *RequestHandler) handleListOffsets(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read request
	_, _ = req.Reader.ReadInt32() // replica_id
	if req.ApiVersion >= 2 {
		_, _ = req.Reader.ReadInt8() // isolation_level
	}

	topicCount, _ := req.Reader.ReadArrayLen()

	type offsetPartition struct {
		partition int32
		timestamp int64
		offset    int64
		errorCode protocol.ErrorCode
	}
	type offsetTopic struct {
		topic      string
		partitions []offsetPartition
	}

	var results []offsetTopic

	for i := int32(0); i < topicCount; i++ {
		topic, _ := req.Reader.ReadString()
		partCount, _ := req.Reader.ReadArrayLen()

		ot := offsetTopic{topic: topic}

		for j := int32(0); j < partCount; j++ {
			partition, _ := req.Reader.ReadInt32()
			if req.ApiVersion >= 4 {
				_, _ = req.Reader.ReadInt32() // current_leader_epoch
			}
			timestamp, _ := req.Reader.ReadInt64()

			op := offsetPartition{
				partition: partition,
				timestamp: timestamp,
			}

			offset, err := h.broker.ListOffsets(topic, partition, timestamp)
			if err != nil {
				op.errorCode = protocol.ErrUnknownTopicOrPartition
			} else {
				op.offset = offset
				op.errorCode = protocol.ErrNone
			}

			ot.partitions = append(ot.partitions, op)
		}

		results = append(results, ot)
	}

	// Throttle time (v2+)
	if req.ApiVersion >= 2 {
		w.WriteInt32(0)
	}

	// Write response
	w.WriteArrayLen(int32(len(results)))
	for _, ot := range results {
		w.WriteString(ot.topic)
		w.WriteArrayLen(int32(len(ot.partitions)))
		for _, op := range ot.partitions {
			w.WriteInt32(op.partition)
			w.WriteInt16(int16(op.errorCode))
			if req.ApiVersion >= 1 {
				w.WriteInt64(op.timestamp)
			}
			w.WriteInt64(op.offset)
			// Leader epoch (v4+)
			if req.ApiVersion >= 4 {
				w.WriteInt32(0)
			}
		}
	}

	return resp
}
