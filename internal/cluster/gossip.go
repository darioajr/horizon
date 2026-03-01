package cluster

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Gossip message types
// ---------------------------------------------------------------------------

const (
	gossipPing  byte = 1 // periodic heartbeat containing the sender's node list
	gossipAck   byte = 2 // response to PING
	gossipJoin  byte = 3 // new node announcing itself
	gossipLeave byte = 4 // graceful departure

	maxGossipPacket = 65000 // safe UDP payload limit
)

// gossipMessage is the over-the-wire format for gossip packets.
//
// Layout:
//
//	[1 byte]  type
//	[4 bytes] sender ID
//	[8 bytes] cluster state version
//	[...]     encoded node list (see EncodeNodeList)
type gossipMessage struct {
	Type     byte
	SenderID int32
	Version  int64
	Nodes    []*NodeInfo
}

func encodeGossipMessage(m *gossipMessage) []byte {
	nodeBlob := EncodeNodeList(m.Nodes)
	buf := make([]byte, 1+4+8+len(nodeBlob))
	buf[0] = m.Type
	putInt32(buf[1:], m.SenderID)
	putInt64(buf[5:], m.Version)
	copy(buf[13:], nodeBlob)
	return buf
}

func decodeGossipMessage(data []byte) (*gossipMessage, error) {
	if len(data) < 13 {
		return nil, fmt.Errorf("gossip packet too short (%d)", len(data))
	}
	m := &gossipMessage{
		Type:     data[0],
		SenderID: getInt32(data[1:]),
		Version:  getInt64(data[5:]),
	}
	nodes, err := DecodeNodeList(data[13:])
	if err != nil {
		return nil, fmt.Errorf("decode nodes: %w", err)
	}
	m.Nodes = nodes
	return m, nil
}

// ---------------------------------------------------------------------------
// Gossip      – UDP-based SWIM-lite membership protocol
// ---------------------------------------------------------------------------

// Gossip manages cluster membership via UDP heartbeats.
type Gossip struct {
	mu   sync.Mutex
	state *ClusterState
	self  *NodeInfo

	// Configuration
	seeds    []string      // "host:port" seed addresses
	interval time.Duration // heartbeat interval
	failThr  time.Duration // time before marking node dead

	conn     *net.UDPConn
	shutdown chan struct{}
	wg       sync.WaitGroup

	// Callback invoked when the set of alive nodes changes.
	onMemberChange func()
}

// GossipConfig configures the gossip subsystem.
type GossipConfig struct {
	BindAddr         string // "0.0.0.0:9093" (UDP)
	Seeds            []string
	Interval         time.Duration
	FailureThreshold time.Duration
	OnMemberChange   func()
}

// NewGossip creates a new gossip instance.
func NewGossip(state *ClusterState, self *NodeInfo, cfg GossipConfig) *Gossip {
	return &Gossip{
		state:          state,
		self:           self,
		seeds:          cfg.Seeds,
		interval:       cfg.Interval,
		failThr:        cfg.FailureThreshold,
		shutdown:       make(chan struct{}),
		onMemberChange: cfg.OnMemberChange,
	}
}

// Start begins listening for gossip messages and sending periodic heartbeats.
func (g *Gossip) Start(bindAddr string) error {
	addr, err := net.ResolveUDPAddr("udp", bindAddr)
	if err != nil {
		return fmt.Errorf("gossip resolve: %w", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("gossip listen: %w", err)
	}
	g.conn = conn

	// Register ourselves in the state
	g.self.State = NodeAlive
	g.self.LastSeen = time.Now()
	g.state.SetNode(g.self)

	g.wg.Add(2)
	go g.receiveLoop()
	go g.heartbeatLoop()

	// Send initial JOIN to all seeds
	g.sendJoin()

	return nil
}

// Stop gracefully shuts down gossip.
func (g *Gossip) Stop() {
	close(g.shutdown)
	// Send LEAVE to peers
	g.sendLeave()
	g.conn.Close()
	g.wg.Wait()
}

// ---------------------------------------------------------------------------
// receive loop
// ---------------------------------------------------------------------------

func (g *Gossip) receiveLoop() {
	defer g.wg.Done()
	buf := make([]byte, maxGossipPacket)
	for {
		select {
		case <-g.shutdown:
			return
		default:
		}
		g.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, remoteAddr, err := g.conn.ReadFromUDP(buf)
		if err != nil {
			continue // timeout or shutdown
		}
		msg, err := decodeGossipMessage(buf[:n])
		if err != nil {
			log.Printf("[gossip] bad packet from %s: %v", remoteAddr, err)
			continue
		}
		g.handleMessage(msg, remoteAddr)
	}
}

func (g *Gossip) handleMessage(msg *gossipMessage, from *net.UDPAddr) {
	changed := false
	switch msg.Type {
	case gossipPing:
		changed = g.mergeNodes(msg.Nodes)
		// Reply with ACK
		g.sendTo(from, gossipAck)
	case gossipAck:
		changed = g.mergeNodes(msg.Nodes)
	case gossipJoin:
		changed = g.mergeNodes(msg.Nodes)
		// Reply with our full state so the joiner learns about everyone
		g.sendTo(from, gossipAck)
	case gossipLeave:
		g.state.MarkNode(msg.SenderID, NodeLeft)
		changed = true
	}
	if changed && g.onMemberChange != nil {
		g.onMemberChange()
	}
}

// mergeNodes merges incoming node info with local state. Returns true if
// anything changed.
func (g *Gossip) mergeNodes(incoming []*NodeInfo) bool {
	changed := false
	for _, remote := range incoming {
		if remote.ID == g.self.ID {
			continue // don't overwrite our own state
		}
		existing := g.state.GetNode(remote.ID)
		if existing == nil {
			// New node
			remote.State = NodeAlive
			remote.LastSeen = time.Now()
			g.state.SetNode(remote)
			log.Printf("[gossip] discovered node %d (%s:%d)", remote.ID, remote.Host, remote.RPCPort)
			changed = true
			continue
		}
		// Update if remote has higher generation or fresher data
		if remote.Generation > existing.Generation {
			remote.LastSeen = time.Now()
			remote.State = NodeAlive
			g.state.SetNode(remote)
			changed = true
		} else if remote.Generation == existing.Generation && remote.State == NodeAlive {
			// Refresh last seen
			existing.LastSeen = time.Now()
			g.state.SetNode(existing)
		}
	}
	return changed
}

// ---------------------------------------------------------------------------
// heartbeat loop
// ---------------------------------------------------------------------------

func (g *Gossip) heartbeatLoop() {
	defer g.wg.Done()
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()

	for {
		select {
		case <-g.shutdown:
			return
		case <-ticker.C:
			g.checkFailures()
			g.sendHeartbeats()
		}
	}
}

// checkFailures marks nodes as suspect or dead based on last-seen times.
func (g *Gossip) checkFailures() {
	now := time.Now()
	changed := false
	for _, n := range g.state.AllNodes() {
		if n.ID == g.self.ID {
			continue
		}
		if n.State == NodeLeft || n.State == NodeDead {
			continue
		}
		age := now.Sub(n.LastSeen)
		if age > g.failThr && n.State == NodeAlive {
			log.Printf("[gossip] node %d suspect (last seen %v ago)", n.ID, age.Round(time.Millisecond))
			g.state.MarkNode(n.ID, NodeSuspect)
			changed = true
		} else if age > g.failThr*2 && n.State == NodeSuspect {
			log.Printf("[gossip] node %d declared dead", n.ID)
			g.state.MarkNode(n.ID, NodeDead)
			changed = true
		}
	}
	if changed && g.onMemberChange != nil {
		g.onMemberChange()
	}
}

// sendHeartbeats sends a PING to a random subset of alive peers.
func (g *Gossip) sendHeartbeats() {
	peers := g.state.AliveNodes()
	// Remove self
	filtered := make([]*NodeInfo, 0, len(peers))
	for _, p := range peers {
		if p.ID != g.self.ID {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == 0 {
		// No peers known — re-try seeds
		g.sendJoin()
		return
	}

	// Ping up to 3 random peers
	count := 3
	if count > len(filtered) {
		count = len(filtered)
	}
	rand.Shuffle(len(filtered), func(i, j int) { filtered[i], filtered[j] = filtered[j], filtered[i] })
	for _, peer := range filtered[:count] {
		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", peer.Host, peer.RPCPort))
		if err != nil {
			continue
		}
		g.sendTo(addr, gossipPing)
	}
}

// ---------------------------------------------------------------------------
// send helpers
// ---------------------------------------------------------------------------

func (g *Gossip) sendTo(addr *net.UDPAddr, msgType byte) {
	// Refresh our own last seen
	g.self.LastSeen = time.Now()
	g.state.SetNode(g.self)

	msg := &gossipMessage{
		Type:     msgType,
		SenderID: g.self.ID,
		Version:  g.state.Version(),
		Nodes:    g.state.AllNodes(),
	}
	data := encodeGossipMessage(msg)
	if len(data) > maxGossipPacket {
		log.Printf("[gossip] packet too large (%d bytes), dropping", len(data))
		return
	}
	g.conn.WriteToUDP(data, addr)
}

func (g *Gossip) sendJoin() {
	for _, seed := range g.seeds {
		addr, err := net.ResolveUDPAddr("udp", seed)
		if err != nil {
			log.Printf("[gossip] bad seed %q: %v", seed, err)
			continue
		}
		g.sendTo(addr, gossipJoin)
	}
}

func (g *Gossip) sendLeave() {
	g.self.State = NodeLeft
	g.state.SetNode(g.self)
	peers := g.state.AliveNodes()
	for _, p := range peers {
		if p.ID == g.self.ID {
			continue
		}
		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", p.Host, p.RPCPort))
		if err != nil {
			continue
		}
		g.sendTo(addr, gossipLeave)
	}
}

// ---------------------------------------------------------------------------
// binary helpers
// ---------------------------------------------------------------------------

func putInt32(buf []byte, v int32) { buf[0] = byte(v >> 24); buf[1] = byte(v >> 16); buf[2] = byte(v >> 8); buf[3] = byte(v) }
func putInt64(buf []byte, v int64) {
	buf[0] = byte(v >> 56); buf[1] = byte(v >> 48); buf[2] = byte(v >> 40); buf[3] = byte(v >> 32)
	buf[4] = byte(v >> 24); buf[5] = byte(v >> 16); buf[6] = byte(v >> 8); buf[7] = byte(v)
}
func getInt32(b []byte) int32 { return int32(b[0])<<24 | int32(b[1])<<16 | int32(b[2])<<8 | int32(b[3]) }
func getInt64(b []byte) int64 {
	return int64(b[0])<<56 | int64(b[1])<<48 | int64(b[2])<<40 | int64(b[3])<<32 |
		int64(b[4])<<24 | int64(b[5])<<16 | int64(b[6])<<8 | int64(b[7])
}
