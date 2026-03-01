package cluster

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"horizon/internal/storage"
)

// ---------------------------------------------------------------------------
// RPC message types
// ---------------------------------------------------------------------------

const (
	rpcForwardProduce byte = 1 // forward a produce request to the partition leader
	rpcForwardFetch   byte = 2 // forward a fetch request to the partition leader
	rpcReplicaFetch   byte = 3 // follower replication fetch
	rpcAssignBroadcast byte = 4 // controller broadcasts partition assignments
	rpcAckOffset      byte = 5 // follower reports replicated offset to leader

	rpcErrNone            byte = 0
	rpcErrNotLeader       byte = 1
	rpcErrUnknown         byte = 2
	rpcErrTopicNotFound   byte = 3
)

// ---------------------------------------------------------------------------
// RPC Server
// ---------------------------------------------------------------------------

// RPCServer listens on TCP for inter-broker requests.
type RPCServer struct {
	listener net.Listener
	cluster  *Cluster
	shutdown chan struct{}
	wg       sync.WaitGroup
}

// NewRPCServer creates a new inter-broker RPC server.
func NewRPCServer(c *Cluster) *RPCServer {
	return &RPCServer{
		cluster:  c,
		shutdown: make(chan struct{}),
	}
}

// Start begins listening for RPC connections.
func (s *RPCServer) Start(bindAddr string) error {
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return fmt.Errorf("rpc listen: %w", err)
	}
	s.listener = ln
	s.wg.Add(1)
	go s.acceptLoop()
	return nil
}

// Stop gracefully shuts down the RPC server.
func (s *RPCServer) Stop() {
	close(s.shutdown)
	s.listener.Close()
	s.wg.Wait()
}

func (s *RPCServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.shutdown:
				return
			default:
				continue
			}
		}
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *RPCServer) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	reader := bufio.NewReaderSize(conn, 256*1024)
	for {
		select {
		case <-s.shutdown:
			return
		default:
		}
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))

		// Read frame: [4 bytes length][1 byte type][4 bytes corrID][payload]
		hdr := make([]byte, 9)
		if _, err := io.ReadFull(reader, hdr); err != nil {
			return // client disconnected or timeout
		}
		frameLen := int(binary.BigEndian.Uint32(hdr[0:4]))
		msgType := hdr[4]
		corrID := binary.BigEndian.Uint32(hdr[5:9])

		payloadLen := frameLen - 5 // type(1) + corrID(4) already read
		var payload []byte
		if payloadLen > 0 {
			payload = make([]byte, payloadLen)
			if _, err := io.ReadFull(reader, payload); err != nil {
				return
			}
		}

		resp := s.dispatch(msgType, payload)
		s.writeResponse(conn, corrID, resp)
	}
}

func (s *RPCServer) dispatch(msgType byte, payload []byte) []byte {
	switch msgType {
	case rpcForwardProduce:
		return s.handleForwardProduce(payload)
	case rpcForwardFetch:
		return s.handleForwardFetch(payload)
	case rpcReplicaFetch:
		return s.handleReplicaFetch(payload)
	case rpcAssignBroadcast:
		return s.handleAssignBroadcast(payload)
	case rpcAckOffset:
		return s.handleAckOffset(payload)
	default:
		return []byte{rpcErrUnknown}
	}
}

func (s *RPCServer) writeResponse(conn net.Conn, corrID uint32, data []byte) {
	// Frame: [4 bytes length][4 bytes corrID][data]
	frame := make([]byte, 4+4+len(data))
	binary.BigEndian.PutUint32(frame[0:], uint32(4+len(data)))
	binary.BigEndian.PutUint32(frame[4:], corrID)
	copy(frame[8:], data)
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	conn.Write(frame)
}

// ---------------------------------------------------------------------------
// RPC Server handlers
// ---------------------------------------------------------------------------

// ForwardProduce payload:
//   [2] topicLen [N] topic [4] partition [4] dataLen [N] data [4] recordCount [8] maxTs
// Response:
//   [1] errCode [8] baseOffset
func (s *RPCServer) handleForwardProduce(payload []byte) []byte {
	topic, partition, data, recordCount, maxTs, err := decodeProducePayload(payload)
	if err != nil {
		return []byte{rpcErrUnknown, 0, 0, 0, 0, 0, 0, 0, 0}
	}
	offset, err := s.cluster.localBroker.ProduceRaw(topic, partition, data, recordCount, maxTs)
	if err != nil {
		resp := make([]byte, 9)
		resp[0] = rpcErrUnknown
		return resp
	}
	resp := make([]byte, 9)
	resp[0] = rpcErrNone
	binary.BigEndian.PutUint64(resp[1:], uint64(offset))
	return resp
}

// ForwardFetch payload:
//   [2] topicLen [N] topic [4] partition [8] offset [8] maxBytes
// Response:
//   [1] errCode [4] batchCount { [4] batchLen [N] batchData }*
func (s *RPCServer) handleForwardFetch(payload []byte) []byte {
	topic, partition, offset, maxBytes, err := decodeFetchPayload(payload)
	if err != nil {
		return []byte{rpcErrUnknown}
	}
	batches, err := s.cluster.localBroker.Fetch(topic, partition, offset, maxBytes)
	if err != nil {
		return []byte{rpcErrUnknown}
	}
	return encodeStorageFetchResponse(batches)
}

// ReplicaFetch — same as ForwardFetch but used by the replicator.
func (s *RPCServer) handleReplicaFetch(payload []byte) []byte {
	return s.handleForwardFetch(payload)
}

// AssignBroadcast — controller sends new partition assignment table.
func (s *RPCServer) handleAssignBroadcast(payload []byte) []byte {
	table, ctrlID, ver, err := DecodeAssignments(payload)
	if err != nil {
		log.Printf("[rpc] bad assignment broadcast: %v", err)
		return []byte{rpcErrUnknown}
	}
	s.cluster.state.SetAssignments(table, ctrlID, ver)
	log.Printf("[rpc] received assignment v%d from controller %d (%d topic-partitions)",
		ver, ctrlID, countAssignments(table))

	// Notify replicator of new assignments
	if s.cluster.replicator != nil {
		s.cluster.replicator.Refresh()
	}
	return []byte{rpcErrNone}
}

// AckOffset — follower reports its replicated offset for a partition.
// payload: [2] topicLen [N] topic [4] partition [8] offset [4] followerID
func (s *RPCServer) handleAckOffset(payload []byte) []byte {
	if len(payload) < 2 {
		return []byte{rpcErrUnknown}
	}
	off := 0
	topicLen := int(binary.BigEndian.Uint16(payload[off:]))
	off += 2
	if off+topicLen+4+8+4 > len(payload) {
		return []byte{rpcErrUnknown}
	}
	topic := string(payload[off : off+topicLen])
	off += topicLen
	partition := int32(binary.BigEndian.Uint32(payload[off:]))
	off += 4
	replicatedOffset := int64(binary.BigEndian.Uint64(payload[off:]))
	off += 8
	followerID := int32(binary.BigEndian.Uint32(payload[off:]))

	// Update ISR tracking
	if s.cluster.replicator != nil {
		s.cluster.replicator.AckFollowerOffset(topic, partition, followerID, replicatedOffset)
	}
	return []byte{rpcErrNone}
}

// ---------------------------------------------------------------------------
// RPC Client
// ---------------------------------------------------------------------------

// RPCClient manages a connection to a single remote broker.
type RPCClient struct {
	mu     sync.Mutex
	addr   string
	conn   net.Conn
	reader *bufio.Reader
	nextID uint32
}

// NewRPCClient creates a client for a remote broker.
func NewRPCClient(addr string) *RPCClient {
	return &RPCClient{addr: addr}
}

func (c *RPCClient) connect() error {
	if c.conn != nil {
		return nil
	}
	conn, err := net.DialTimeout("tcp", c.addr, 5*time.Second)
	if err != nil {
		return err
	}
	c.conn = conn
	c.reader = bufio.NewReaderSize(conn, 256*1024)
	return nil
}

func (c *RPCClient) close() {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
		c.reader = nil
	}
}

// Call sends an RPC request and waits for the response.
func (c *RPCClient) Call(msgType byte, payload []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.connect(); err != nil {
		return nil, fmt.Errorf("rpc connect %s: %w", c.addr, err)
	}

	c.nextID++
	corrID := c.nextID

	// Write frame: [4 len][1 type][4 corrID][payload]
	frameLen := 1 + 4 + len(payload)
	frame := make([]byte, 4+frameLen)
	binary.BigEndian.PutUint32(frame[0:], uint32(frameLen))
	frame[4] = msgType
	binary.BigEndian.PutUint32(frame[5:], corrID)
	copy(frame[9:], payload)

	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.conn.Write(frame); err != nil {
		c.close()
		return nil, fmt.Errorf("rpc write: %w", err)
	}

	// Read response: [4 len][4 corrID][data]
	c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	respHdr := make([]byte, 8)
	if _, err := io.ReadFull(c.reader, respHdr); err != nil {
		c.close()
		return nil, fmt.Errorf("rpc read header: %w", err)
	}
	respLen := int(binary.BigEndian.Uint32(respHdr[0:4]))
	// respCorrID := binary.BigEndian.Uint32(respHdr[4:8])

	dataLen := respLen - 4 // corrID already read
	if dataLen <= 0 {
		return nil, nil
	}
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(c.reader, data); err != nil {
		c.close()
		return nil, fmt.Errorf("rpc read data: %w", err)
	}
	return data, nil
}

// Close closes the underlying connection.
func (c *RPCClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.close()
}

// ---------------------------------------------------------------------------
// RPCPool — connection pool indexed by node ID
// ---------------------------------------------------------------------------

// RPCPool maintains one RPCClient per known remote node.
type RPCPool struct {
	mu      sync.Mutex
	clients map[int32]*RPCClient
	state   *ClusterState
}

// NewRPCPool creates a pool backed by the cluster state.
func NewRPCPool(state *ClusterState) *RPCPool {
	return &RPCPool{
		clients: make(map[int32]*RPCClient),
		state:   state,
	}
}

// Get returns (or creates) the client for a given node ID.
func (p *RPCPool) Get(nodeID int32) (*RPCClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[nodeID]; ok {
		return c, nil
	}
	node := p.state.GetNode(nodeID)
	if node == nil {
		return nil, fmt.Errorf("unknown node %d", nodeID)
	}
	addr := fmt.Sprintf("%s:%d", node.Host, node.RPCPort)
	c := NewRPCClient(addr)
	p.clients[nodeID] = c
	return c, nil
}

// CloseAll closes every client in the pool.
func (p *RPCPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.clients {
		c.Close()
	}
	p.clients = make(map[int32]*RPCClient)
}

// Remove removes and closes the client for a specific node.
func (p *RPCPool) Remove(nodeID int32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[nodeID]; ok {
		c.Close()
		delete(p.clients, nodeID)
	}
}

// ---------------------------------------------------------------------------
// Payload encoding / decoding helpers
// ---------------------------------------------------------------------------

func encodeProducePayload(topic string, partition int32, data []byte, recordCount int32, maxTs int64) []byte {
	size := 2 + len(topic) + 4 + 4 + len(data) + 4 + 8
	buf := make([]byte, size)
	off := 0
	binary.BigEndian.PutUint16(buf[off:], uint16(len(topic)))
	off += 2
	copy(buf[off:], topic)
	off += len(topic)
	binary.BigEndian.PutUint32(buf[off:], uint32(partition))
	off += 4
	binary.BigEndian.PutUint32(buf[off:], uint32(len(data)))
	off += 4
	copy(buf[off:], data)
	off += len(data)
	binary.BigEndian.PutUint32(buf[off:], uint32(recordCount))
	off += 4
	binary.BigEndian.PutUint64(buf[off:], uint64(maxTs))
	return buf
}

func decodeProducePayload(payload []byte) (topic string, partition int32, data []byte, recordCount int32, maxTs int64, err error) {
	if len(payload) < 2 {
		return "", 0, nil, 0, 0, fmt.Errorf("produce payload too short")
	}
	off := 0
	topicLen := int(binary.BigEndian.Uint16(payload[off:]))
	off += 2
	if off+topicLen+4+4 > len(payload) {
		return "", 0, nil, 0, 0, fmt.Errorf("produce payload truncated")
	}
	topic = string(payload[off : off+topicLen])
	off += topicLen
	partition = int32(binary.BigEndian.Uint32(payload[off:]))
	off += 4
	dataLen := int(binary.BigEndian.Uint32(payload[off:]))
	off += 4
	if off+dataLen+4+8 > len(payload) {
		return "", 0, nil, 0, 0, fmt.Errorf("produce payload data truncated")
	}
	data = payload[off : off+dataLen]
	off += dataLen
	recordCount = int32(binary.BigEndian.Uint32(payload[off:]))
	off += 4
	maxTs = int64(binary.BigEndian.Uint64(payload[off:]))
	return
}

func encodeFetchPayload(topic string, partition int32, offset int64, maxBytes int64) []byte {
	size := 2 + len(topic) + 4 + 8 + 8
	buf := make([]byte, size)
	off := 0
	binary.BigEndian.PutUint16(buf[off:], uint16(len(topic)))
	off += 2
	copy(buf[off:], topic)
	off += len(topic)
	binary.BigEndian.PutUint32(buf[off:], uint32(partition))
	off += 4
	binary.BigEndian.PutUint64(buf[off:], uint64(offset))
	off += 8
	binary.BigEndian.PutUint64(buf[off:], uint64(maxBytes))
	return buf
}

func decodeFetchPayload(payload []byte) (topic string, partition int32, offset int64, maxBytes int64, err error) {
	if len(payload) < 2 {
		return "", 0, 0, 0, fmt.Errorf("fetch payload too short")
	}
	off := 0
	topicLen := int(binary.BigEndian.Uint16(payload[off:]))
	off += 2
	if off+topicLen+4+8+8 > len(payload) {
		return "", 0, 0, 0, fmt.Errorf("fetch payload truncated")
	}
	topic = string(payload[off : off+topicLen])
	off += topicLen
	partition = int32(binary.BigEndian.Uint32(payload[off:]))
	off += 4
	offset = int64(binary.BigEndian.Uint64(payload[off:]))
	off += 8
	maxBytes = int64(binary.BigEndian.Uint64(payload[off:]))
	return
}

func encodeFetchResponse(batches []*rpcBatch) []byte {
	// [1 errCode][4 count]{ [4 len][data] }
	size := 1 + 4
	for _, b := range batches {
		size += 4 + len(b.Data)
	}
	buf := make([]byte, size)
	buf[0] = rpcErrNone
	binary.BigEndian.PutUint32(buf[1:], uint32(len(batches)))
	off := 5
	for _, b := range batches {
		binary.BigEndian.PutUint32(buf[off:], uint32(len(b.Data)))
		off += 4
		copy(buf[off:], b.Data)
		off += len(b.Data)
	}
	return buf
}

// encodeStorageFetchResponse encodes storage.RecordBatch results for RPC.
func encodeStorageFetchResponse(batches []*storage.RecordBatch) []byte {
	// Encode each batch to wire format first
	encoded := make([][]byte, len(batches))
	totalSize := 1 + 4 // errCode + count
	for i, b := range batches {
		encoded[i] = b.Encode()
		totalSize += 4 + len(encoded[i])
	}
	buf := make([]byte, totalSize)
	buf[0] = rpcErrNone
	binary.BigEndian.PutUint32(buf[1:], uint32(len(batches)))
	off := 5
	for _, data := range encoded {
		binary.BigEndian.PutUint32(buf[off:], uint32(len(data)))
		off += 4
		copy(buf[off:], data)
		off += len(data)
	}
	return buf
}

// rpcBatch is a thin wrapper used during RPC encode/decode of fetch results.
type rpcBatch struct {
	Data []byte
}

func countAssignments(table map[string]map[int32]*PartitionAssignment) int {
	n := 0
	for _, parts := range table {
		n += len(parts)
	}
	return n
}
