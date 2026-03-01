package cluster

import (
	"encoding/binary"
	"fmt"
	"log"
	"sync"
	"time"

	"horizon/internal/storage"
)

// ---------------------------------------------------------------------------
// Replicator — runs on every node.
//   - For partitions where this node is a FOLLOWER: fetches from the leader.
//   - For partitions where this node is the LEADER: tracks follower offsets
//     and advances the ISR / high watermark.
// ---------------------------------------------------------------------------

// Replicator manages replica synchronisation.
type Replicator struct {
	mu       sync.Mutex
	cluster  *Cluster
	fetchers map[string]chan struct{} // "topic/partition" → stop channel
	shutdown chan struct{}
	wg       sync.WaitGroup

	// Leader-side: tracks the last offset each follower has acknowledged.
	followerOffsets map[string]map[int32]int64 // "topic/partition" → followerID → offset
	foMu            sync.Mutex
}

// NewReplicator creates a replicator.
func NewReplicator(c *Cluster) *Replicator {
	return &Replicator{
		cluster:         c,
		fetchers:        make(map[string]chan struct{}),
		shutdown:        make(chan struct{}),
		followerOffsets: make(map[string]map[int32]int64),
	}
}

// Start begins the replicator.
func (r *Replicator) Start() {
	r.Refresh()
}

// Stop shuts down all fetcher goroutines.
func (r *Replicator) Stop() {
	close(r.shutdown)
	r.mu.Lock()
	for _, ch := range r.fetchers {
		close(ch)
	}
	r.fetchers = make(map[string]chan struct{})
	r.mu.Unlock()
	r.wg.Wait()
}

// Refresh recalculates which partitions need a fetcher goroutine.
func (r *Replicator) Refresh() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Determine desired set of follower partitions
	desired := make(map[string]*PartitionAssignment)
	for _, a := range r.cluster.state.LocalFollowerPartitions() {
		key := partKey(a.Topic, a.Partition)
		desired[key] = a
	}

	// Stop fetchers that are no longer needed
	for key, ch := range r.fetchers {
		if _, ok := desired[key]; !ok {
			close(ch)
			delete(r.fetchers, key)
		}
	}

	// Start fetchers for new follower partitions
	for key, a := range desired {
		if _, running := r.fetchers[key]; running {
			continue
		}
		stopCh := make(chan struct{})
		r.fetchers[key] = stopCh
		r.wg.Add(1)
		go r.fetchLoop(a.Topic, a.Partition, a.Leader, stopCh)
	}
}

// ---------------------------------------------------------------------------
// Follower fetch loop
// ---------------------------------------------------------------------------

func (r *Replicator) fetchLoop(topic string, partition int32, leaderID int32, stop chan struct{}) {
	defer r.wg.Done()
	log.Printf("[replicator] starting fetch for %s/%d from leader %d", topic, partition, leaderID)

	// Determine starting offset from local storage
	localOffset := r.getLocalOffset(topic, partition)

	backoff := 100 * time.Millisecond
	maxBackoff := 5 * time.Second

	for {
		select {
		case <-r.shutdown:
			return
		case <-stop:
			return
		default:
		}

		client, err := r.cluster.rpcPool.Get(leaderID)
		if err != nil {
			log.Printf("[replicator] cannot reach leader %d for %s/%d: %v", leaderID, topic, partition, err)
			r.sleep(backoff, stop)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		payload := encodeFetchPayload(topic, partition, localOffset, 1024*1024)
		resp, err := client.Call(rpcReplicaFetch, payload)
		if err != nil {
			log.Printf("[replicator] fetch error %s/%d: %v", topic, partition, err)
			r.cluster.rpcPool.Remove(leaderID)
			r.sleep(backoff, stop)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		if len(resp) < 5 || resp[0] != rpcErrNone {
			r.sleep(backoff, stop)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		batchCount := int(binary.BigEndian.Uint32(resp[1:5]))
		off := 5
		written := 0
		for i := 0; i < batchCount; i++ {
			if off+4 > len(resp) {
				break
			}
			batchLen := int(binary.BigEndian.Uint32(resp[off:]))
			off += 4
			if off+batchLen > len(resp) {
				break
			}
			batchData := resp[off : off+batchLen]
			off += batchLen

			// Validate and write to local storage
			recordCount, maxTs, _, vErr := storage.ValidateRecordBatchHeader(batchData)
			if vErr != nil {
				log.Printf("[replicator] bad batch from leader %d: %v", leaderID, vErr)
				continue
			}
			newOffset, wErr := r.cluster.localBroker.ProduceRaw(topic, partition, batchData, recordCount, maxTs)
			if wErr != nil {
				log.Printf("[replicator] write error %s/%d: %v", topic, partition, wErr)
				continue
			}
			localOffset = newOffset + int64(recordCount)
			written += int(recordCount)
		}

		if written > 0 {
			backoff = 100 * time.Millisecond
			// Report replicated offset to the leader
			r.reportOffset(leaderID, topic, partition, localOffset)
		} else {
			// Nothing new — back off slightly
			r.sleep(200*time.Millisecond, stop)
		}
	}
}

func (r *Replicator) getLocalOffset(topic string, partition int32) int64 {
	p, err := r.cluster.localBroker.GetPartition(topic, partition)
	if err != nil {
		return 0
	}
	return p.HighWatermark()
}

func (r *Replicator) reportOffset(leaderID int32, topic string, partition int32, offset int64) {
	client, err := r.cluster.rpcPool.Get(leaderID)
	if err != nil {
		return
	}
	payload := make([]byte, 2+len(topic)+4+8+4)
	off := 0
	binary.BigEndian.PutUint16(payload[off:], uint16(len(topic)))
	off += 2
	copy(payload[off:], topic)
	off += len(topic)
	binary.BigEndian.PutUint32(payload[off:], uint32(partition))
	off += 4
	binary.BigEndian.PutUint64(payload[off:], uint64(offset))
	off += 8
	binary.BigEndian.PutUint32(payload[off:], uint32(r.cluster.cfg.NodeID))
	client.Call(rpcAckOffset, payload)
}

func (r *Replicator) sleep(d time.Duration, stop chan struct{}) {
	select {
	case <-time.After(d):
	case <-stop:
	case <-r.shutdown:
	}
}

// ---------------------------------------------------------------------------
// Leader-side: track follower offsets
// ---------------------------------------------------------------------------

// AckFollowerOffset records a follower's replicated offset.
func (r *Replicator) AckFollowerOffset(topic string, partition int32, followerID int32, offset int64) {
	r.foMu.Lock()
	defer r.foMu.Unlock()
	key := partKey(topic, partition)
	if r.followerOffsets[key] == nil {
		r.followerOffsets[key] = make(map[int32]int64)
	}
	r.followerOffsets[key][followerID] = offset
}

// GetFollowerOffset returns the last acked offset for a follower.
func (r *Replicator) GetFollowerOffset(topic string, partition int32, followerID int32) int64 {
	r.foMu.Lock()
	defer r.foMu.Unlock()
	key := partKey(topic, partition)
	if m := r.followerOffsets[key]; m != nil {
		return m[followerID]
	}
	return 0
}

func partKey(topic string, partition int32) string {
	return fmt.Sprintf("%s/%d", topic, partition)
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
