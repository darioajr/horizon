package server

import (
	"horizon/internal/broker"
	"horizon/internal/protocol"
)

// RequestHandler handles Kafka protocol requests
type RequestHandler struct {
	broker *broker.Broker
}

// NewRequestHandler creates a new request handler
func NewRequestHandler(b *broker.Broker) *RequestHandler {
	return &RequestHandler{broker: b}
}

// HandleRequest dispatches a request to the appropriate handler
func (h *RequestHandler) HandleRequest(req *Request) *Response {
	switch req.ApiKey {
	case protocol.ApiKeyApiVersions:
		return h.handleApiVersions(req)
	case protocol.ApiKeyMetadata:
		return h.handleMetadata(req)
	case protocol.ApiKeyProduce:
		return h.handleProduce(req)
	case protocol.ApiKeyFetch:
		return h.handleFetch(req)
	case protocol.ApiKeyListOffsets:
		return h.handleListOffsets(req)
	case protocol.ApiKeyFindCoordinator:
		return h.handleFindCoordinator(req)
	case protocol.ApiKeyJoinGroup:
		return h.handleJoinGroup(req)
	case protocol.ApiKeySyncGroup:
		return h.handleSyncGroup(req)
	case protocol.ApiKeyHeartbeat:
		return h.handleHeartbeat(req)
	case protocol.ApiKeyLeaveGroup:
		return h.handleLeaveGroup(req)
	case protocol.ApiKeyOffsetCommit:
		return h.handleOffsetCommit(req)
	case protocol.ApiKeyOffsetFetch:
		return h.handleOffsetFetch(req)
	case protocol.ApiKeyCreateTopics:
		return h.handleCreateTopics(req)
	case protocol.ApiKeyDeleteTopics:
		return h.handleDeleteTopics(req)
	case protocol.ApiKeyInitProducerId:
		return h.handleInitProducerId(req)
	case protocol.ApiKeyDescribeConfigs:
		return h.handleDescribeConfigs(req)
	case protocol.ApiKeyDescribeGroups:
		return h.handleDescribeGroups(req)
	case protocol.ApiKeyListGroups:
		return h.handleListGroups(req)
	case protocol.ApiKeyDeleteGroups:
		return h.handleDeleteGroups(req)
	default:
		return h.handleUnsupported(req)
	}
}

// handleUnsupported handles unsupported API requests
func (h *RequestHandler) handleUnsupported(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	resp.Writer.WriteInt16(int16(protocol.ErrUnsupportedVersion))
	return resp
}
