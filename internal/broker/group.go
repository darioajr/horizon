package broker

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

// ConsumerGroupState represents the state of a consumer group.
type ConsumerGroupState int

const (
	// GroupStateEmpty means no members have joined.
	GroupStateEmpty ConsumerGroupState = iota
	// GroupStatePreparingRebalance means the coordinator is waiting for
	// all members to (re-)join before completing the join phase.
	GroupStatePreparingRebalance
	// GroupStateCompletingRebalance means JoinGroup responses have been
	// sent and the coordinator is waiting for the leader's SyncGroup.
	GroupStateCompletingRebalance
	// GroupStateStable means the group is actively consuming.
	GroupStateStable
	// GroupStateDead means the group has been removed.
	GroupStateDead
)

func (s ConsumerGroupState) String() string {
	switch s {
	case GroupStateEmpty:
		return "Empty"
	case GroupStatePreparingRebalance:
		return "PreparingRebalance"
	case GroupStateCompletingRebalance:
		return "CompletingRebalance"
	case GroupStateStable:
		return "Stable"
	case GroupStateDead:
		return "Dead"
	default:
		return "Unknown"
	}
}

// ---------------------------------------------------------------------------
// Member / protocol types
// ---------------------------------------------------------------------------

// GroupMember represents a member of a consumer group.
type GroupMember struct {
	MemberID           string
	ClientID           string
	ClientHost         string
	SessionTimeoutMs   int32
	RebalanceTimeoutMs int32
	ProtocolType       string
	Protocols          []GroupProtocol
	Assignment         []byte
	LastHeartbeat      time.Time
}

// GroupProtocol represents one of the assignment strategies a member supports.
type GroupProtocol struct {
	Name     string
	Metadata []byte
}

// OffsetAndMetadata holds a committed offset plus optional metadata.
type OffsetAndMetadata struct {
	Offset         int64
	Metadata       string
	CommitTimestamp int64
}

// ---------------------------------------------------------------------------
// Results sent back to handler goroutines (blocking calls)
// ---------------------------------------------------------------------------

// JoinResult is the result of a JoinGroup call.
type JoinResult struct {
	MemberID     string
	Generation   int32
	ProtocolType string
	Protocol     string
	LeaderID     string
	Members      []GroupMember // non-empty only for the leader
	Error        error
}

// SyncResult is the result of a SyncGroup call.
type SyncResult struct {
	Assignment []byte
	Error      error
}

// ---------------------------------------------------------------------------
// ConsumerGroup – full Kafka-compatible group coordinator
// ---------------------------------------------------------------------------

// ConsumerGroup implements the Kafka consumer group protocol including
// blocking JoinGroup / SyncGroup and session-timeout-based member expiry.
type ConsumerGroup struct {
	mu sync.Mutex

	GroupID      string
	State        ConsumerGroupState
	Generation   int32
	ProtocolType string
	Protocol     string
	LeaderID     string

	// Members keyed by member ID.
	Members map[string]*GroupMember

	// Committed offsets: topic → partition → offset.
	Offsets map[string]map[int32]*OffsetAndMetadata

	// Pending JoinGroup responses (memberID → buffered channel).
	pendingJoins map[string]chan JoinResult

	// Pending SyncGroup responses (memberID → buffered channel).
	pendingSyncs map[string]chan SyncResult

	// Rebalance timer – fires to complete the join phase.
	rebalanceTimer *time.Timer

	// Per-member session timers.
	sessionTimers map[string]*time.Timer
}

// newConsumerGroup creates a fresh, empty consumer group.
func newConsumerGroup(groupID string) *ConsumerGroup {
	return &ConsumerGroup{
		GroupID:       groupID,
		State:         GroupStateEmpty,
		Members:       make(map[string]*GroupMember),
		Offsets:       make(map[string]map[int32]*OffsetAndMetadata),
		pendingJoins:  make(map[string]chan JoinResult),
		pendingSyncs:  make(map[string]chan SyncResult),
		sessionTimers: make(map[string]*time.Timer),
	}
}

// ---------------------------------------------------------------------------
// JoinGroup (blocking – returns when the join phase is complete)
// ---------------------------------------------------------------------------

// JoinGroup adds (or updates) a member and blocks until the coordinator
// completes the join phase.  The leader receives the full member list;
// followers receive an empty list.
func (g *ConsumerGroup) JoinGroup(
	memberID, clientID, clientHost, protocolType string,
	sessionTimeoutMs, rebalanceTimeoutMs int32,
	protocols []GroupProtocol,
) JoinResult {
	g.mu.Lock()

	if g.State == GroupStateDead {
		g.mu.Unlock()
		return JoinResult{MemberID: memberID, Error: ErrGroupNotFound}
	}

	// Generate member ID if empty (Kafka pre-KIP-394 behaviour).
	if memberID == "" {
		memberID = generateMemberID(clientID)
	}

	// Protocol type must be consistent across the group.
	if g.ProtocolType != "" && g.ProtocolType != protocolType {
		g.mu.Unlock()
		return JoinResult{MemberID: memberID, Error: ErrInconsistentProtocol}
	}
	if g.ProtocolType == "" {
		g.ProtocolType = protocolType
	}

	// Cancel the old pending join channel for this member (if any) to
	// unblock a stale handler goroutine (e.g. client reconnected).
	if oldCh, ok := g.pendingJoins[memberID]; ok {
		select {
		case oldCh <- JoinResult{MemberID: memberID, Error: ErrUnknownMember}:
		default:
		}
	}

	// Upsert the member.
	member := &GroupMember{
		MemberID:           memberID,
		ClientID:           clientID,
		ClientHost:         clientHost,
		SessionTimeoutMs:   sessionTimeoutMs,
		RebalanceTimeoutMs: rebalanceTimeoutMs,
		ProtocolType:       protocolType,
		Protocols:          protocols,
		LastHeartbeat:      time.Now(),
	}
	g.Members[memberID] = member

	// Create a buffered channel for this member's join result.
	ch := make(chan JoinResult, 1)
	g.pendingJoins[memberID] = ch

	// Transition to PreparingRebalance if not already there.
	if g.State != GroupStatePreparingRebalance {
		g.State = GroupStatePreparingRebalance
		// Cancel previous rebalance timer if any.
		if g.rebalanceTimer != nil {
			g.rebalanceTimer.Stop()
		}
		// Use the maximum rebalance timeout among members.
		timeout := g.maxRebalanceTimeout()
		g.rebalanceTimer = time.AfterFunc(timeout, func() {
			g.completeJoinPhase()
		})
	}

	// If every known member already has a pending join we can complete
	// immediately instead of waiting for the timer.
	if g.allMembersJoined() {
		if g.rebalanceTimer != nil {
			g.rebalanceTimer.Stop()
		}
		g.completeJoinPhaseLocked()
		g.mu.Unlock()
		return <-ch
	}

	g.mu.Unlock()

	// Block until the join phase finishes (timer fires or all members join).
	return <-ch
}

// allMembersJoined returns true when every member has a pending join channel.
func (g *ConsumerGroup) allMembersJoined() bool {
	if len(g.Members) == 0 {
		return false
	}
	for id := range g.Members {
		if _, ok := g.pendingJoins[id]; !ok {
			return false
		}
	}
	return true
}

// maxRebalanceTimeout returns the longest rebalance timeout among members.
func (g *ConsumerGroup) maxRebalanceTimeout() time.Duration {
	var max int32
	for _, m := range g.Members {
		if m.RebalanceTimeoutMs > max {
			max = m.RebalanceTimeoutMs
		}
	}
	if max <= 0 {
		max = 5000
	}
	return time.Duration(max) * time.Millisecond
}

// completeJoinPhase is called by the rebalance timer goroutine.
func (g *ConsumerGroup) completeJoinPhase() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.completeJoinPhaseLocked()
}

// completeJoinPhaseLocked finishes the join phase and sends results to
// all waiting goroutines.  Must be called with g.mu held.
func (g *ConsumerGroup) completeJoinPhaseLocked() {
	if g.State != GroupStatePreparingRebalance {
		return
	}

	// Members that did not rejoin are removed.
	for id := range g.Members {
		if _, hasPending := g.pendingJoins[id]; !hasPending {
			g.removeMemberLocked(id)
		}
	}

	if len(g.Members) == 0 {
		g.State = GroupStateEmpty
		g.ProtocolType = ""
		g.Protocol = ""
		g.LeaderID = ""
		return
	}

	// Bump generation.
	g.Generation++

	// Select assignment protocol supported by all members.
	g.Protocol = g.selectProtocol()

	// Elect leader (pick deterministic – first alphabetically).
	g.LeaderID = ""
	for id := range g.Members {
		if g.LeaderID == "" || id < g.LeaderID {
			g.LeaderID = id
		}
	}

	// Build full member list for the leader response.
	allMembers := make([]GroupMember, 0, len(g.Members))
	for _, m := range g.Members {
		allMembers = append(allMembers, *m)
	}

	g.State = GroupStateCompletingRebalance

	// Notify every waiting handler goroutine.
	for memberID, ch := range g.pendingJoins {
		result := JoinResult{
			MemberID:     memberID,
			Generation:   g.Generation,
			ProtocolType: g.ProtocolType,
			Protocol:     g.Protocol,
			LeaderID:     g.LeaderID,
		}
		if memberID == g.LeaderID {
			result.Members = allMembers
		}
		ch <- result
	}
	g.pendingJoins = make(map[string]chan JoinResult)

	// Start (or reset) session timers for each member.
	for id, m := range g.Members {
		g.startSessionTimerLocked(id, m.SessionTimeoutMs)
	}
}

// selectProtocol picks the first protocol (in leader preference order) that
// every member supports.  Falls back to the first protocol of any member.
func (g *ConsumerGroup) selectProtocol() string {
	memberCount := len(g.Members)
	if memberCount == 0 {
		return ""
	}

	votes := make(map[string]int)
	for _, m := range g.Members {
		for _, p := range m.Protocols {
			votes[p.Name]++
		}
	}

	for _, m := range g.Members {
		for _, p := range m.Protocols {
			if votes[p.Name] == memberCount {
				return p.Name
			}
		}
		break // only need the first member's ordering
	}

	for _, m := range g.Members {
		if len(m.Protocols) > 0 {
			return m.Protocols[0].Name
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// SyncGroup (blocking for followers)
// ---------------------------------------------------------------------------

// SyncGroup distributes partition assignments.  The leader provides
// assignments; followers block until the leader has synced.
func (g *ConsumerGroup) SyncGroup(memberID string, generation int32, assignments map[string][]byte) SyncResult {
	g.mu.Lock()

	if g.State == GroupStateDead {
		g.mu.Unlock()
		return SyncResult{Error: ErrGroupNotFound}
	}

	member, exists := g.Members[memberID]
	if !exists {
		g.mu.Unlock()
		return SyncResult{Error: ErrUnknownMember}
	}
	if generation != g.Generation {
		g.mu.Unlock()
		return SyncResult{Error: ErrIllegalGeneration}
	}

	member.LastHeartbeat = time.Now()
	g.resetSessionTimerLocked(memberID, member.SessionTimeoutMs)

	if g.State == GroupStatePreparingRebalance {
		g.mu.Unlock()
		return SyncResult{Error: ErrRebalanceInProgress}
	}

	// If already stable (leader synced earlier), return immediately.
	if g.State == GroupStateStable {
		a := member.Assignment
		g.mu.Unlock()
		return SyncResult{Assignment: a}
	}

	// CompletingRebalance – leader provides assignments.
	if memberID == g.LeaderID && assignments != nil {
		for mid, a := range assignments {
			if m, ok := g.Members[mid]; ok {
				m.Assignment = a
			}
		}
		g.State = GroupStateStable

		// Notify all waiting followers.
		for mid, ch := range g.pendingSyncs {
			if m, ok := g.Members[mid]; ok {
				ch <- SyncResult{Assignment: m.Assignment}
			} else {
				ch <- SyncResult{Error: ErrUnknownMember}
			}
		}
		g.pendingSyncs = make(map[string]chan SyncResult)

		a := member.Assignment
		g.mu.Unlock()
		return SyncResult{Assignment: a}
	}

	// Follower (or leader without assignments yet): wait.
	ch := make(chan SyncResult, 1)
	g.pendingSyncs[memberID] = ch
	g.mu.Unlock()

	return <-ch
}

// ---------------------------------------------------------------------------
// Heartbeat
// ---------------------------------------------------------------------------

// Heartbeat processes a heartbeat from a member.  Returns
// ErrRebalanceInProgress when the group is rebalancing.
func (g *ConsumerGroup) Heartbeat(memberID string, generation int32) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.State == GroupStateDead {
		return ErrGroupNotFound
	}

	member, exists := g.Members[memberID]
	if !exists {
		return ErrUnknownMember
	}
	if generation != g.Generation {
		return ErrIllegalGeneration
	}

	member.LastHeartbeat = time.Now()
	g.resetSessionTimerLocked(memberID, member.SessionTimeoutMs)

	if g.State == GroupStatePreparingRebalance || g.State == GroupStateCompletingRebalance {
		return ErrRebalanceInProgress
	}

	return nil
}

// ---------------------------------------------------------------------------
// LeaveGroup
// ---------------------------------------------------------------------------

// LeaveGroup removes a member and triggers a rebalance if the group is
// not empty.
func (g *ConsumerGroup) LeaveGroup(memberID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.Members[memberID]; !exists {
		return ErrUnknownMember
	}

	g.removeMemberLocked(memberID)

	if len(g.Members) == 0 {
		g.State = GroupStateEmpty
		g.ProtocolType = ""
		g.Protocol = ""
		g.LeaderID = ""
	} else if g.State == GroupStateStable || g.State == GroupStateCompletingRebalance {
		g.State = GroupStatePreparingRebalance
	}

	return nil
}

// ---------------------------------------------------------------------------
// Offset management
// ---------------------------------------------------------------------------

// CommitOffset commits an offset for a topic-partition.
func (g *ConsumerGroup) CommitOffset(topic string, partition int32, offset int64, metadata string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.Offsets[topic] == nil {
		g.Offsets[topic] = make(map[int32]*OffsetAndMetadata)
	}
	g.Offsets[topic][partition] = &OffsetAndMetadata{
		Offset:         offset,
		Metadata:       metadata,
		CommitTimestamp: time.Now().UnixMilli(),
	}
}

// FetchOffset returns the last committed offset for a topic-partition.
func (g *ConsumerGroup) FetchOffset(topic string, partition int32) (int64, string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.Offsets[topic] == nil {
		return -1, "", false
	}
	om, exists := g.Offsets[topic][partition]
	if !exists {
		return -1, "", false
	}
	return om.Offset, om.Metadata, true
}

// ---------------------------------------------------------------------------
// Describe / Info
// ---------------------------------------------------------------------------

// MemberDescription holds information for DescribeGroups responses.
type MemberDescription struct {
	MemberID   string
	ClientID   string
	ClientHost string
	Metadata   []byte // protocol metadata (subscription)
	Assignment []byte // current partition assignment
}

// GroupDescription holds full information about a consumer group.
type GroupDescription struct {
	GroupID      string
	State        ConsumerGroupState
	ProtocolType string
	Protocol     string
	Members      []MemberDescription
}

// Describe returns a snapshot of the group for DescribeGroups responses.
func (g *ConsumerGroup) Describe() *GroupDescription {
	g.mu.Lock()
	defer g.mu.Unlock()

	desc := &GroupDescription{
		GroupID:      g.GroupID,
		State:        g.State,
		ProtocolType: g.ProtocolType,
		Protocol:     g.Protocol,
		Members:      make([]MemberDescription, 0, len(g.Members)),
	}
	for _, m := range g.Members {
		var meta []byte
		for _, p := range m.Protocols {
			if p.Name == g.Protocol {
				meta = p.Metadata
				break
			}
		}
		if meta == nil && len(m.Protocols) > 0 {
			meta = m.Protocols[0].Metadata
		}
		desc.Members = append(desc.Members, MemberDescription{
			MemberID:   m.MemberID,
			ClientID:   m.ClientID,
			ClientHost: m.ClientHost,
			Metadata:   meta,
			Assignment: m.Assignment,
		})
	}
	return desc
}

// GroupInfo is a lightweight summary used by ListGroups.
type GroupInfo struct {
	GroupID      string
	State        ConsumerGroupState
	ProtocolType string
	Protocol     string
}

// Info returns a lightweight summary.
func (g *ConsumerGroup) Info() *GroupInfo {
	g.mu.Lock()
	defer g.mu.Unlock()
	return &GroupInfo{
		GroupID:      g.GroupID,
		State:        g.State,
		ProtocolType: g.ProtocolType,
		Protocol:     g.Protocol,
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Close stops all timers and marks the group as dead.
func (g *ConsumerGroup) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.State = GroupStateDead

	if g.rebalanceTimer != nil {
		g.rebalanceTimer.Stop()
	}
	for _, t := range g.sessionTimers {
		t.Stop()
	}

	for _, ch := range g.pendingJoins {
		select {
		case ch <- JoinResult{Error: ErrGroupNotFound}:
		default:
		}
	}
	g.pendingJoins = make(map[string]chan JoinResult)

	for _, ch := range g.pendingSyncs {
		select {
		case ch <- SyncResult{Error: ErrGroupNotFound}:
		default:
		}
	}
	g.pendingSyncs = make(map[string]chan SyncResult)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// removeMemberLocked removes a member and cancels its pending operations.
// Must be called with g.mu held.
func (g *ConsumerGroup) removeMemberLocked(memberID string) {
	delete(g.Members, memberID)

	if timer, ok := g.sessionTimers[memberID]; ok {
		timer.Stop()
		delete(g.sessionTimers, memberID)
	}

	if ch, ok := g.pendingJoins[memberID]; ok {
		select {
		case ch <- JoinResult{MemberID: memberID, Error: ErrUnknownMember}:
		default:
		}
		delete(g.pendingJoins, memberID)
	}

	if ch, ok := g.pendingSyncs[memberID]; ok {
		select {
		case ch <- SyncResult{Error: ErrUnknownMember}:
		default:
		}
		delete(g.pendingSyncs, memberID)
	}

	if memberID == g.LeaderID {
		g.LeaderID = ""
		for id := range g.Members {
			if g.LeaderID == "" || id < g.LeaderID {
				g.LeaderID = id
			}
		}
		if g.State == GroupStateCompletingRebalance {
			for mid, ch := range g.pendingSyncs {
				select {
				case ch <- SyncResult{Error: ErrRebalanceInProgress}:
				default:
				}
				delete(g.pendingSyncs, mid)
			}
		}
	}
}

// startSessionTimerLocked starts (or replaces) the session timer for a
// member. Must be called with g.mu held.
func (g *ConsumerGroup) startSessionTimerLocked(memberID string, timeoutMs int32) {
	if timer, ok := g.sessionTimers[memberID]; ok {
		timer.Stop()
	}
	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	g.sessionTimers[memberID] = time.AfterFunc(timeout, func() {
		g.expireMember(memberID)
	})
}

// resetSessionTimerLocked resets an existing session timer.
// Must be called with g.mu held.
func (g *ConsumerGroup) resetSessionTimerLocked(memberID string, timeoutMs int32) {
	if timer, ok := g.sessionTimers[memberID]; ok {
		timeout := time.Duration(timeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		timer.Reset(timeout)
	} else {
		g.startSessionTimerLocked(memberID, timeoutMs)
	}
}

// expireMember is called by a session timer goroutine when a member's
// heartbeat has timed out.
func (g *ConsumerGroup) expireMember(memberID string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.Members[memberID]; !exists {
		return
	}

	g.removeMemberLocked(memberID)

	if len(g.Members) == 0 {
		g.State = GroupStateEmpty
		g.ProtocolType = ""
		g.Protocol = ""
		g.LeaderID = ""
	} else if g.State == GroupStateStable || g.State == GroupStateCompletingRebalance {
		g.State = GroupStatePreparingRebalance
	}
}

// ---------------------------------------------------------------------------
// Member ID generation
// ---------------------------------------------------------------------------

var memberIDCounter uint64

func generateMemberID(clientID string) string {
	n := atomic.AddUint64(&memberIDCounter, 1)
	return fmt.Sprintf("%s-%d-%d", clientID, time.Now().UnixNano(), n)
}

// ---------------------------------------------------------------------------
// GroupManager
// ---------------------------------------------------------------------------

// GroupManager manages all consumer groups.
type GroupManager struct {
	mu     sync.RWMutex
	groups map[string]*ConsumerGroup
}

// NewGroupManager creates a new group manager.
func NewGroupManager() *GroupManager {
	return &GroupManager{
		groups: make(map[string]*ConsumerGroup),
	}
}

// GetOrCreateGroup returns an existing group or creates a new one.
func (gm *GroupManager) GetOrCreateGroup(groupID string) *ConsumerGroup {
	gm.mu.Lock()
	defer gm.mu.Unlock()

	if g, ok := gm.groups[groupID]; ok {
		return g
	}
	g := newConsumerGroup(groupID)
	gm.groups[groupID] = g
	return g
}

// GetGroup returns a group or nil if it doesn't exist.
func (gm *GroupManager) GetGroup(groupID string) *ConsumerGroup {
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	return gm.groups[groupID]
}

// DeleteGroup deletes a group. Returns an error if the group has active
// members (must be Empty or Dead).
func (gm *GroupManager) DeleteGroup(groupID string) error {
	gm.mu.Lock()
	defer gm.mu.Unlock()

	g, ok := gm.groups[groupID]
	if !ok {
		return ErrGroupNotFound
	}

	g.mu.Lock()
	state := g.State
	hasMembers := len(g.Members) > 0
	g.mu.Unlock()

	if hasMembers && state != GroupStateEmpty && state != GroupStateDead {
		return ErrGroupNotEmpty
	}

	g.Close()
	delete(gm.groups, groupID)
	return nil
}

// ListGroups returns info about every group.
func (gm *GroupManager) ListGroups() []*GroupInfo {
	gm.mu.RLock()
	defer gm.mu.RUnlock()

	out := make([]*GroupInfo, 0, len(gm.groups))
	for _, g := range gm.groups {
		out = append(out, g.Info())
	}
	return out
}
