package server

import (
	"horizon/internal/broker"
	"horizon/internal/protocol"
)

// handleApiVersions handles ApiVersions request
func (h *RequestHandler) handleApiVersions(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Supported API versions
	// Note: Keeping max versions below flexible version threshold
	// Flexible versions (v9+ for most APIs) use compact arrays/tagged fields
	type apiVersion struct {
		key     int16
		minVer  int16
		maxVer  int16
	}

	versions := []apiVersion{
		{int16(protocol.ApiKeyProduce), 0, 8},            // v9+ uses flexible
		{int16(protocol.ApiKeyFetch), 0, 11},             // v12+ uses flexible
		{int16(protocol.ApiKeyListOffsets), 0, 5},         // v6+ uses flexible
		{int16(protocol.ApiKeyMetadata), 0, 8},            // v9+ uses flexible
		{int16(protocol.ApiKeyOffsetCommit), 0, 7},        // v8+ uses flexible
		{int16(protocol.ApiKeyOffsetFetch), 0, 5},         // v6+ uses flexible
		{int16(protocol.ApiKeyFindCoordinator), 0, 2},     // v3+ uses flexible
		{int16(protocol.ApiKeyJoinGroup), 0, 5},           // v6+ uses flexible
		{int16(protocol.ApiKeyHeartbeat), 0, 3},           // v4+ uses flexible
		{int16(protocol.ApiKeyLeaveGroup), 0, 3},          // v4+ uses flexible
		{int16(protocol.ApiKeySyncGroup), 0, 3},           // v4+ uses flexible
		{int16(protocol.ApiKeyDescribeGroups), 0, 4},      // v5+ uses flexible
		{int16(protocol.ApiKeyListGroups), 0, 2},          // v3+ uses flexible
		{int16(protocol.ApiKeyApiVersions), 0, 2},         // v3+ uses flexible
		{int16(protocol.ApiKeyCreateTopics), 0, 4},        // v5+ uses flexible
		{int16(protocol.ApiKeyDeleteTopics), 0, 3},        // v4+ uses flexible
		{int16(protocol.ApiKeyInitProducerId), 0, 1},      // v2+ uses flexible
		{int16(protocol.ApiKeyDescribeConfigs), 0, 3},     // v4+ uses flexible
		{int16(protocol.ApiKeyDeleteGroups), 0, 1},        // v2+ uses flexible
	}

	// KIP-511: If the client requests a version we don't support (e.g. v3+
	// which uses flexible encoding), respond with UNSUPPORTED_VERSION using
	// the v0 response format so the client can fall back gracefully.
	if req.ApiVersion > 2 {
		w.WriteInt16(int16(protocol.ErrUnsupportedVersion))
	} else {
		w.WriteInt16(int16(protocol.ErrNone))
	}

	w.WriteArrayLen(int32(len(versions)))
	for _, v := range versions {
		w.WriteInt16(v.key)
		w.WriteInt16(v.minVer)
		w.WriteInt16(v.maxVer)
	}

	// Throttle time (v1+ only for supported versions; omit for v3+ error path
	// since we respond in v0 format)
	if req.ApiVersion >= 1 && req.ApiVersion <= 2 {
		w.WriteInt32(0)
	}

	return resp
}

// handleMetadata handles Metadata request
func (h *RequestHandler) handleMetadata(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read topics
	topicCount, _ := req.Reader.ReadArrayLen()
	var topics []string
	for i := int32(0); i < topicCount; i++ {
		topic, _ := req.Reader.ReadString()
		topics = append(topics, topic)
	}

	meta, err := h.broker.GetMetadata(topics)
	if err != nil {
		// Return minimal valid response on error
		meta = &broker.ClusterMetadata{
			Brokers:      []broker.BrokerInfo{},
			ControllerID: -1,
			Topics:       []broker.TopicMetadata{},
		}
	}

	// Ensure brokers is never nil
	if meta.Brokers == nil {
		meta.Brokers = []broker.BrokerInfo{}
	}

	// Throttle time (v3+)
	if req.ApiVersion >= 3 {
		w.WriteInt32(0)
	}

	// Write brokers (must always be present, never null)
	w.WriteArrayLen(int32(len(meta.Brokers)))
	for _, b := range meta.Brokers {
		w.WriteInt32(b.NodeID)
		w.WriteString(b.Host)
		w.WriteInt32(b.Port)
		if req.ApiVersion >= 1 {
			w.WriteNullableString(nil) // rack
		}
	}

	// Cluster ID (v2+)
	if req.ApiVersion >= 2 {
		w.WriteNullableString(nil)
	}

	// Controller ID (v1+)
	if req.ApiVersion >= 1 {
		w.WriteInt32(meta.ControllerID)
	}

	// Write topics
	w.WriteArrayLen(int32(len(meta.Topics)))
	for _, t := range meta.Topics {
		w.WriteInt16(int16(protocol.ErrNone)) // error code
		w.WriteString(t.Topic)
		if req.ApiVersion >= 1 {
			w.WriteBool(false) // is_internal
		}

		// Write partitions
		w.WriteArrayLen(int32(len(t.Partitions)))
		for _, p := range t.Partitions {
			w.WriteInt16(int16(protocol.ErrNone)) // error code
			w.WriteInt32(p.Partition)
			w.WriteInt32(p.Leader)

			// Leader epoch (v7+)
			if req.ApiVersion >= 7 {
				w.WriteInt32(p.LeaderEpoch)
			}

			// Replicas
			w.WriteArrayLen(int32(len(p.Replicas)))
			for _, r := range p.Replicas {
				w.WriteInt32(r)
			}

			// ISR
			w.WriteArrayLen(int32(len(p.ISR)))
			for _, r := range p.ISR {
				w.WriteInt32(r)
			}

			// Offline replicas (v5+)
			if req.ApiVersion >= 5 {
				w.WriteArrayLen(0)
			}
		}

		// Topic authorized operations (v8+)
		if req.ApiVersion >= 8 {
			w.WriteInt32(-2147483648) // INT32_MIN means not available
		}
	}

	// Cluster authorized operations (v8+)
	if req.ApiVersion >= 8 {
		w.WriteInt32(-2147483648) // INT32_MIN means not available
	}

	return resp
}
