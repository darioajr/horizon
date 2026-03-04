package server

import (
	"horizon/internal/broker"
	"horizon/internal/protocol"
)

// mapGroupError translates broker group errors to Kafka protocol error codes.
func mapGroupError(err error) protocol.ErrorCode {
	switch err {
	case nil:
		return protocol.ErrNone
	case broker.ErrUnknownMember:
		return protocol.ErrUnknownMemberId
	case broker.ErrIllegalGeneration:
		return protocol.ErrIllegalGeneration
	case broker.ErrInconsistentProtocol:
		return protocol.ErrInconsistentGroupProtocol
	case broker.ErrRebalanceInProgress:
		return protocol.ErrRebalanceInProgress
	case broker.ErrGroupNotFound:
		return protocol.ErrGroupIdNotFound
	case broker.ErrGroupNotEmpty:
		return protocol.ErrNonEmptyGroup
	default:
		return protocol.ErrUnknown
	}
}

// handleFindCoordinator handles FindCoordinator request
func (h *RequestHandler) handleFindCoordinator(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read key (group ID or transaction ID)
	_, _ = req.Reader.ReadString()

	// Throttle time (v1+)
	if req.ApiVersion >= 1 {
		w.WriteInt32(0)
	}

	// Error code
	w.WriteInt16(int16(protocol.ErrNone))

	// Error message (v1+)
	if req.ApiVersion >= 1 {
		w.WriteNullableString(nil)
	}

	// Coordinator info (this broker)
	meta, _ := h.broker.GetMetadata(nil)
	if len(meta.Brokers) > 0 {
		b := meta.Brokers[0]
		w.WriteInt32(b.NodeID)
		w.WriteString(b.Host)
		w.WriteInt32(b.Port)
	} else {
		w.WriteInt32(0)
		w.WriteString("localhost")
		w.WriteInt32(9092)
	}

	return resp
}

// handleJoinGroup handles JoinGroup request (blocks until join phase completes)
func (h *RequestHandler) handleJoinGroup(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read request
	groupID, _ := req.Reader.ReadString()
	sessionTimeoutMs, _ := req.Reader.ReadInt32()
	var rebalanceTimeoutMs int32 = sessionTimeoutMs
	if req.ApiVersion >= 1 {
		rebalanceTimeoutMs, _ = req.Reader.ReadInt32()
	}
	memberID, _ := req.Reader.ReadString()
	if req.ApiVersion >= 5 {
		_, _ = req.Reader.ReadNullableString() // group_instance_id
	}
	protocolType, _ := req.Reader.ReadString()

	// Read protocols
	protocolCount, _ := req.Reader.ReadArrayLen()
	var protocols []broker.GroupProtocol
	for i := int32(0); i < protocolCount; i++ {
		name, _ := req.Reader.ReadString()
		metadata, _ := req.Reader.ReadBytes()
		protocols = append(protocols, broker.GroupProtocol{
			Name:     name,
			Metadata: metadata,
		})
	}

	// Join group – this call blocks until the coordinator finishes the
	// join phase (all members joined or rebalance timeout expired).
	group := h.broker.GetGroupManager().GetOrCreateGroup(groupID)
	result := group.JoinGroup(
		memberID, req.ClientID, "", protocolType,
		sessionTimeoutMs, rebalanceTimeoutMs, protocols,
	)

	// Throttle time (v2+)
	if req.ApiVersion >= 2 {
		w.WriteInt32(0)
	}

	// Error code
	errCode := mapGroupError(result.Error)
	w.WriteInt16(int16(errCode))
	w.WriteInt32(result.Generation)
	// Protocol type (v7+)
	if req.ApiVersion >= 7 {
		if errCode == protocol.ErrNone {
			w.WriteNullableString(&result.ProtocolType)
		} else {
			w.WriteNullableString(nil)
		}
	}
	w.WriteString(result.Protocol)
	w.WriteString(result.LeaderID)
	w.WriteString(result.MemberID)

	// Members (only populated for the leader)
	w.WriteArrayLen(int32(len(result.Members)))
	for _, m := range result.Members {
		w.WriteString(m.MemberID)
		if req.ApiVersion >= 5 {
			w.WriteNullableString(nil) // group_instance_id
		}
		// Find metadata for the selected protocol
		var meta []byte
		for _, p := range m.Protocols {
			if p.Name == result.Protocol {
				meta = p.Metadata
				break
			}
		}
		if meta == nil && len(m.Protocols) > 0 {
			meta = m.Protocols[0].Metadata
		}
		w.WriteBytes(meta)
	}

	return resp
}

// handleSyncGroup handles SyncGroup request (blocks for followers until
// the leader provides assignments)
func (h *RequestHandler) handleSyncGroup(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read request
	groupID, _ := req.Reader.ReadString()
	generation, _ := req.Reader.ReadInt32()
	memberID, _ := req.Reader.ReadString()
	if req.ApiVersion >= 3 {
		_, _ = req.Reader.ReadNullableString() // group_instance_id
	}
	if req.ApiVersion >= 5 {
		_, _ = req.Reader.ReadNullableString() // protocol_type
		_, _ = req.Reader.ReadNullableString() // protocol_name
	}

	// Read assignments
	assignmentCount, _ := req.Reader.ReadArrayLen()
	assignments := make(map[string][]byte)
	for i := int32(0); i < assignmentCount; i++ {
		mid, _ := req.Reader.ReadString()
		assignment, _ := req.Reader.ReadBytes()
		assignments[mid] = assignment
	}

	// Sync group – may block for followers.
	group := h.broker.GetGroupManager().GetGroup(groupID)

	var result broker.SyncResult
	if group == nil {
		result = broker.SyncResult{Error: broker.ErrGroupNotFound}
	} else {
		result = group.SyncGroup(memberID, generation, assignments)
	}

	// Throttle time (v1+)
	if req.ApiVersion >= 1 {
		w.WriteInt32(0)
	}

	errCode := mapGroupError(result.Error)
	w.WriteInt16(int16(errCode))
	if req.ApiVersion >= 5 {
		w.WriteNullableString(nil) // protocol_type
		w.WriteNullableString(nil) // protocol_name
	}
	w.WriteBytes(result.Assignment)

	return resp
}

// handleHeartbeat handles Heartbeat request
func (h *RequestHandler) handleHeartbeat(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read request
	groupID, _ := req.Reader.ReadString()
	generation, _ := req.Reader.ReadInt32()
	memberID, _ := req.Reader.ReadString()

	// Process heartbeat
	group := h.broker.GetGroupManager().GetGroup(groupID)
	var err error

	if group == nil {
		err = broker.ErrGroupNotFound
	} else {
		err = group.Heartbeat(memberID, generation)
	}

	// Throttle time (v1+)
	if req.ApiVersion >= 1 {
		w.WriteInt32(0)
	}

	// Error code
	if err != nil {
		if err == broker.ErrRebalanceInProgress {
			w.WriteInt16(int16(protocol.ErrRebalanceInProgress))
		} else if err == broker.ErrUnknownMember {
			w.WriteInt16(int16(protocol.ErrUnknownMemberId))
		} else if err == broker.ErrIllegalGeneration {
			w.WriteInt16(int16(protocol.ErrIllegalGeneration))
		} else {
			w.WriteInt16(int16(protocol.ErrUnknown))
		}
	} else {
		w.WriteInt16(int16(protocol.ErrNone))
	}

	return resp
}

// handleLeaveGroup handles LeaveGroup request
func (h *RequestHandler) handleLeaveGroup(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read request
	groupID, _ := req.Reader.ReadString()

	group := h.broker.GetGroupManager().GetGroup(groupID)
	var groupErr error

	type memberResult struct {
		memberID  string
		errorCode protocol.ErrorCode
	}
	var memberResults []memberResult

	if req.ApiVersion >= 3 {
		// v3+: batch leave with members array
		memberCount, _ := req.Reader.ReadArrayLen()
		for i := int32(0); i < memberCount; i++ {
			memberID, _ := req.Reader.ReadString()
			if req.ApiVersion >= 4 {
				_, _ = req.Reader.ReadNullableString() // group_instance_id
			}

			mr := memberResult{memberID: memberID}
			if group == nil {
				mr.errorCode = protocol.ErrUnknown
			} else if err := group.LeaveGroup(memberID); err != nil {
				mr.errorCode = protocol.ErrUnknown
			} else {
				mr.errorCode = protocol.ErrNone
			}
			memberResults = append(memberResults, mr)
		}
	} else {
		// v0-v2: single member leave
		memberID, _ := req.Reader.ReadString()
		if group == nil {
			groupErr = broker.ErrGroupNotFound
		} else {
			groupErr = group.LeaveGroup(memberID)
		}
	}

	// Throttle time (v1+)
	if req.ApiVersion >= 1 {
		w.WriteInt32(0)
	}

	// Error code
	if req.ApiVersion >= 3 {
		w.WriteInt16(int16(protocol.ErrNone))
		// Member responses (v3+)
		w.WriteArrayLen(int32(len(memberResults)))
		for _, mr := range memberResults {
			w.WriteString(mr.memberID)
			w.WriteNullableString(nil) // group_instance_id (v3+)
			w.WriteInt16(int16(mr.errorCode))
		}
	} else {
		if groupErr != nil {
			w.WriteInt16(int16(protocol.ErrUnknown))
		} else {
			w.WriteInt16(int16(protocol.ErrNone))
		}
	}

	return resp
}

// ---------------------------------------------------------------------------
// DescribeGroups (API key 15, v0-v4)
// ---------------------------------------------------------------------------

// handleDescribeGroups handles DescribeGroups request
func (h *RequestHandler) handleDescribeGroups(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Throttle time (v1+)
	if req.ApiVersion >= 1 {
		w.WriteInt32(0)
	}

	// Read group IDs
	groupCount, _ := req.Reader.ReadArrayLen()
	groupIDs := make([]string, groupCount)
	for i := int32(0); i < groupCount; i++ {
		groupIDs[i], _ = req.Reader.ReadString()
	}

	w.WriteArrayLen(groupCount)
	for _, gid := range groupIDs {
		group := h.broker.GetGroupManager().GetGroup(gid)
		if group == nil {
			w.WriteInt16(int16(protocol.ErrGroupIdNotFound))
			w.WriteString(gid)
			w.WriteString("")  // state
			w.WriteString("")  // protocol_type
			w.WriteString("")  // protocol
			w.WriteArrayLen(0) // members
			if req.ApiVersion >= 3 {
				w.WriteInt32(-2147483648) // authorized_operations (unknown)
			}
			continue
		}

		desc := group.Describe()
		w.WriteInt16(int16(protocol.ErrNone))
		w.WriteString(desc.GroupID)
		w.WriteString(desc.State.String())
		w.WriteString(desc.ProtocolType)
		w.WriteString(desc.Protocol)

		w.WriteArrayLen(int32(len(desc.Members)))
		for _, m := range desc.Members {
			w.WriteString(m.MemberID)
			if req.ApiVersion >= 4 {
				w.WriteNullableString(nil) // group_instance_id
			}
			w.WriteString(m.ClientID)
			w.WriteString(m.ClientHost)
			w.WriteBytes(m.Metadata)
			w.WriteBytes(m.Assignment)
		}

		if req.ApiVersion >= 3 {
			w.WriteInt32(-2147483648) // authorized_operations (not implemented)
		}
	}

	return resp
}

// ---------------------------------------------------------------------------
// ListGroups (API key 16, v0-v3)
// ---------------------------------------------------------------------------

// handleListGroups handles ListGroups request
func (h *RequestHandler) handleListGroups(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Throttle time (v1+)
	if req.ApiVersion >= 1 {
		w.WriteInt32(0)
	}

	w.WriteInt16(int16(protocol.ErrNone)) // error_code

	groups := h.broker.GetGroupManager().ListGroups()
	w.WriteArrayLen(int32(len(groups)))
	for _, g := range groups {
		w.WriteString(g.GroupID)
		w.WriteString(g.ProtocolType)
	}

	return resp
}

// ---------------------------------------------------------------------------
// DeleteGroups (API key 42, v0-v1)
// ---------------------------------------------------------------------------

// handleDeleteGroups handles DeleteGroups request
func (h *RequestHandler) handleDeleteGroups(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read group IDs
	groupCount, _ := req.Reader.ReadArrayLen()
	groupIDs := make([]string, groupCount)
	for i := int32(0); i < groupCount; i++ {
		groupIDs[i], _ = req.Reader.ReadString()
	}

	// Throttle time
	w.WriteInt32(0)

	// Results
	w.WriteArrayLen(groupCount)
	for _, gid := range groupIDs {
		w.WriteString(gid)
		err := h.broker.GetGroupManager().DeleteGroup(gid)
		w.WriteInt16(int16(mapGroupError(err)))
	}

	return resp
}
