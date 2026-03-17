package storage

import (
	"sync"
	"sync/atomic"
	"time"
)

// AccumulatorConfig holds tuning parameters for the adaptive write accumulator.
type AccumulatorConfig struct {
	// Enabled activates the accumulator. When false writes go directly to the
	// segment (original behaviour).
	Enabled bool

	// MaxLingerMicros is the upper bound of the adaptive linger window.
	// Under low load the linger is zero (no added latency); under high load
	// the accumulator waits up to this duration to coalesce more writes.
	MaxLingerMicros int64

	// MaxCoalesceBytes triggers an immediate flush when the accumulated
	// payload reaches this size, regardless of the linger timer.
	MaxCoalesceBytes int64
}

// DefaultAccumulatorConfig returns sensible defaults.
func DefaultAccumulatorConfig() AccumulatorConfig {
	return AccumulatorConfig{
		Enabled:          true,
		MaxLingerMicros:  500,
		MaxCoalesceBytes: 1024 * 1024, // 1 MB
	}
}

// writeRequest is a single produce that is submitted to the accumulator.
type writeRequest struct {
	data         []byte
	recordCount  int32
	maxTimestamp int64
	result       chan writeResult
}

// writeResult is returned to the caller after the coalesced write completes.
type writeResult struct {
	baseOffset int64
	err        error
}

// WriteAccumulator batches concurrent writes targeting the same partition
// into a single logFile.Write syscall. It adapts its linger window
// automatically: zero wait under low load, up to MaxLingerMicros under
// high load.
type WriteAccumulator struct {
	cfg AccumulatorConfig

	// incoming is the channel through which callers submit write requests.
	incoming chan writeRequest

	// partition is a reference back to the owning partition so the
	// accumulator can call the actual write path.
	partition *Partition

	// stopCh signals the flush goroutine to exit.
	stopCh chan struct{}
	wg     sync.WaitGroup

	// --- adaptive linger state (updated by the flush goroutine only) ---

	// recentRate is an exponential moving average of batches-per-second.
	recentRate float64

	// DirtyBytes tracks unflushed bytes (read by background sync).
	DirtyBytes atomic.Int64
}

// NewWriteAccumulator creates and starts a new accumulator for a partition.
func NewWriteAccumulator(p *Partition, cfg AccumulatorConfig) *WriteAccumulator {
	wa := &WriteAccumulator{
		cfg:       cfg,
		incoming:  make(chan writeRequest, 256),
		partition: p,
		stopCh:    make(chan struct{}),
	}
	wa.wg.Add(1)
	go wa.flushLoop()
	return wa
}

// Submit enqueues a write and blocks until the coalesced write is committed.
func (wa *WriteAccumulator) Submit(data []byte, recordCount int32, maxTimestamp int64) (int64, error) {
	req := writeRequest{
		data:         data,
		recordCount:  recordCount,
		maxTimestamp: maxTimestamp,
		result:       make(chan writeResult, 1),
	}

	select {
	case wa.incoming <- req:
	case <-wa.stopCh:
		return 0, ErrStorageClosed
	}

	res := <-req.result
	return res.baseOffset, res.err
}

// Stop drains remaining writes and stops the flush goroutine.
func (wa *WriteAccumulator) Stop() {
	close(wa.stopCh)
	wa.wg.Wait()
}

// flushLoop is the single goroutine that drains the incoming channel,
// optionally waits a short adaptive linger, then writes everything in
// one coalesced batch.
func (wa *WriteAccumulator) flushLoop() {
	defer wa.wg.Done()

	// Pre-allocate a drain buffer.
	pending := make([]writeRequest, 0, 64)

	for {
		// --- block until at least one request arrives (or stop) ---
		select {
		case req := <-wa.incoming:
			pending = append(pending, req)
		case <-wa.stopCh:
			// Drain anything still in the channel.
			wa.drainAndWrite(pending)
			return
		}

		// --- adaptive linger: wait for more if load is high ---
		linger := wa.adaptiveLinger()
		if linger > 0 {
			deadline := time.NewTimer(linger)
			wa.drainUntil(pending, deadline, &pending)
			deadline.Stop()
		}

		// --- non-blocking drain of everything currently queued ---
		wa.drainNonBlocking(&pending)

		// --- write coalesced batch ---
		wa.writeBatch(pending)

		// --- update adaptive rate ---
		wa.updateRate(len(pending))

		// Reset for next iteration (keep backing array).
		pending = pending[:0]
	}
}

// adaptiveLinger returns the linger duration based on the recent batch rate.
//
//	rate < 1000/s  → 0          (zero-wait fast path, same as today)
//	rate 1000-10k  → 50-200µs   (light coalescing)
//	rate > 10k     → 200-500µs  (full coalescing)
func (wa *WriteAccumulator) adaptiveLinger() time.Duration {
	maxMicros := wa.cfg.MaxLingerMicros
	if maxMicros <= 0 {
		return 0
	}
	rate := wa.recentRate
	switch {
	case rate < 1000:
		return 0
	case rate < 10000:
		// Linear interpolation 50..200µs
		frac := (rate - 1000) / 9000
		micros := 50 + frac*150
		if micros > float64(maxMicros) {
			micros = float64(maxMicros)
		}
		return time.Duration(int64(micros)) * time.Microsecond
	default:
		// Linear interpolation 200..maxMicros
		frac := (rate - 10000) / 90000
		if frac > 1 {
			frac = 1
		}
		micros := 200 + frac*float64(maxMicros-200)
		return time.Duration(int64(micros)) * time.Microsecond
	}
}

// drainUntil drains the incoming channel until the timer fires, accumulating
// into *dst. It also breaks early if maxCoalesceBytes is reached.
func (wa *WriteAccumulator) drainUntil(cur []writeRequest, t *time.Timer, dst *[]writeRequest) {
	accBytes := int64(0)
	for _, r := range cur {
		accBytes += int64(len(r.data))
	}
	for {
		if accBytes >= wa.cfg.MaxCoalesceBytes {
			return
		}
		select {
		case req := <-wa.incoming:
			*dst = append(*dst, req)
			accBytes += int64(len(req.data))
		case <-t.C:
			return
		case <-wa.stopCh:
			return
		}
	}
}

// drainNonBlocking drains everything currently in the channel without waiting.
func (wa *WriteAccumulator) drainNonBlocking(dst *[]writeRequest) {
	for {
		select {
		case req := <-wa.incoming:
			*dst = append(*dst, req)
		default:
			return
		}
	}
}

// writeBatch writes all pending requests to the partition in a single
// coalesced operation.
func (wa *WriteAccumulator) writeBatch(batch []writeRequest) {
	if len(batch) == 0 {
		return
	}

	p := wa.partition

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		for i := range batch {
			batch[i].result <- writeResult{err: ErrStorageClosed}
		}
		return
	}

	for i := range batch {
		req := &batch[i]

		// Check if we need to roll to a new segment.
		if p.activeSegment.size >= p.activeSegment.maxBytes {
			if err := p.rollSegment(); err != nil {
				// Fail this and all subsequent requests in the batch.
				for j := i; j < len(batch); j++ {
					batch[j].result <- writeResult{err: err}
				}
				return
			}
		}

		baseOffset, err := p.activeSegment.appendRawLocked(req.data, req.recordCount, req.maxTimestamp)
		if err != nil {
			if err == ErrSegmentFull {
				if rollErr := p.rollSegment(); rollErr != nil {
					for j := i; j < len(batch); j++ {
						batch[j].result <- writeResult{err: rollErr}
					}
					return
				}
				baseOffset, err = p.activeSegment.appendRawLocked(req.data, req.recordCount, req.maxTimestamp)
			}
			if err != nil {
				req.result <- writeResult{err: err}
				continue
			}
		}

		wa.DirtyBytes.Add(int64(len(req.data)))
		req.result <- writeResult{baseOffset: baseOffset}
	}

	// Update high watermark once after the coalesced write.
	p.highWatermark = p.activeSegment.nextOffset
}

// drainAndWrite is used during shutdown to flush remaining requests.
func (wa *WriteAccumulator) drainAndWrite(pending []writeRequest) {
	for {
		select {
		case req := <-wa.incoming:
			pending = append(pending, req)
		default:
			wa.writeBatch(pending)
			return
		}
	}
}

// updateRate updates the exponential moving average of the batch arrival rate.
func (wa *WriteAccumulator) updateRate(batchCount int) {
	if batchCount == 0 {
		return
	}
	// Simple EMA with α = 0.3 (reacts within ~3 iterations).
	// We approximate instantaneous "rate" as batchCount / lingerWindow.
	// For simplicity we use batchCount * 10000 as a proxy (assumes ~100µs
	// per iteration at high load).
	instantRate := float64(batchCount) * 10000
	const alpha = 0.3
	wa.recentRate = alpha*instantRate + (1-alpha)*wa.recentRate
}
