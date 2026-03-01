package server

import (
	"horizon/internal/protocol"
	"horizon/internal/storage"
)

// handleProduce handles Produce request
func (h *RequestHandler) handleProduce(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read request
	if req.ApiVersion >= 3 {
		_, _ = req.Reader.ReadNullableString() // transactional_id
	}
	_, _ = req.Reader.ReadInt16() // acks
	_, _ = req.Reader.ReadInt32() // timeout

	topicCount, _ := req.Reader.ReadArrayLen()

	type partitionResult struct {
		partition  int32
		errorCode  protocol.ErrorCode
		baseOffset int64
	}
	type topicResult struct {
		topic      string
		partitions []partitionResult
	}

	results := make([]topicResult, 0, topicCount)

	// Get cluster router (nil in standalone mode)
	cluster := h.broker.GetCluster()

	for i := int32(0); i < topicCount; i++ {
		topic, _ := req.Reader.ReadString()
		partCount, _ := req.Reader.ReadArrayLen()

		tr := topicResult{topic: topic, partitions: make([]partitionResult, 0, partCount)}

		for j := int32(0); j < partCount; j++ {
			partition, _ := req.Reader.ReadInt32()
			recordData, _ := req.Reader.ReadSlice()

			pr := partitionResult{partition: partition}

			if recordData != nil {
				recordCount, maxTimestamp, _, err := storage.ValidateRecordBatchHeader(recordData)
				if err != nil {
					pr.errorCode = protocol.ErrCorruptMessage
				} else if cluster != nil && !cluster.IsPartitionLocal(topic, partition) {
					// Cluster mode: forward to the partition leader
					leaderID, _, _, ok := cluster.GetPartitionLeader(topic, partition)
					if !ok {
						pr.errorCode = protocol.ErrNotLeaderForPartition
					} else {
						baseOffset, fwdErr := cluster.ForwardProduce(leaderID, topic, partition, recordData, recordCount, maxTimestamp)
						if fwdErr != nil {
							pr.errorCode = protocol.ErrNotLeaderForPartition
						} else {
							pr.baseOffset = baseOffset
							pr.errorCode = protocol.ErrNone
						}
					}
				} else {
					baseOffset, err := h.broker.ProduceRaw(topic, partition, recordData, recordCount, maxTimestamp)
					if err != nil {
						pr.errorCode = protocol.ErrUnknown
					} else {
						pr.baseOffset = baseOffset
						pr.errorCode = protocol.ErrNone
					}
				}
			}

			tr.partitions = append(tr.partitions, pr)
		}

		results = append(results, tr)
	}

	// Write response
	w.WriteArrayLen(int32(len(results)))
	for _, tr := range results {
		w.WriteString(tr.topic)
		w.WriteArrayLen(int32(len(tr.partitions)))
		for _, pr := range tr.partitions {
			w.WriteInt32(pr.partition)
			w.WriteInt16(int16(pr.errorCode))
			w.WriteInt64(pr.baseOffset)
			if req.ApiVersion >= 2 {
				w.WriteInt64(-1) // log append time
			}
			if req.ApiVersion >= 5 {
				w.WriteInt64(0) // log start offset
			}
			if req.ApiVersion >= 8 {
				w.WriteArrayLen(0)       // record errors
				w.WriteNullableString(nil) // error message
			}
		}
	}

	if req.ApiVersion >= 1 {
		w.WriteInt32(0) // throttle time
	}

	return resp
}
