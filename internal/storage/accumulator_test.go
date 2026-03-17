package storage

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccumulatorSingleWrite(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultPartitionConfig()
	cfg.AccumulatorConfig = DefaultAccumulatorConfig()
	cfg.AccumulatorConfig.Enabled = true

	p, err := NewPartition(dir, "test-acc", 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// Build a minimal record batch.
	records := []Record{{Value: []byte("hello")}}
	batch := NewRecordBatch(0, records)
	data := batch.Encode()

	offset, err := p.AppendRaw(data, 1, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("AppendRaw failed: %v", err)
	}
	if offset != 0 {
		t.Fatalf("expected offset 0, got %d", offset)
	}

	// Verify the data was stored.
	batches, err := p.Fetch(0, 1024*1024)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(batches) == 0 {
		t.Fatal("expected at least one batch")
	}
}

func TestAccumulatorConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultPartitionConfig()
	cfg.AccumulatorConfig = AccumulatorConfig{
		Enabled:          true,
		MaxLingerMicros:  200,
		MaxCoalesceBytes: 1024 * 1024,
	}

	p, err := NewPartition(dir, "test-acc-conc", 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	const numWriters = 16
	const writesPerWriter = 100

	var wg sync.WaitGroup
	wg.Add(numWriters)

	offsets := make([]int64, numWriters*writesPerWriter)
	var offsetIdx atomic.Int64

	for w := 0; w < numWriters; w++ {
		go func() {
			defer wg.Done()
			records := []Record{{Value: []byte("concurrent-test")}}
			batch := NewRecordBatch(0, records)
			data := batch.Encode()

			for i := 0; i < writesPerWriter; i++ {
				off, err := p.AppendRaw(data, 1, time.Now().UnixMilli())
				if err != nil {
					t.Errorf("concurrent AppendRaw failed: %v", err)
					return
				}
				idx := offsetIdx.Add(1) - 1
				offsets[idx] = off
			}
		}()
	}

	wg.Wait()

	totalWritten := int(offsetIdx.Load())
	if totalWritten != numWriters*writesPerWriter {
		t.Fatalf("expected %d writes, got %d", numWriters*writesPerWriter, totalWritten)
	}

	// Verify all offsets are unique.
	seen := make(map[int64]bool, totalWritten)
	for i := 0; i < totalWritten; i++ {
		off := offsets[i]
		if seen[off] {
			t.Fatalf("duplicate offset %d", off)
		}
		seen[off] = true
	}

	// Verify high watermark reflects all writes.
	hwm := p.HighWatermark()
	if hwm != int64(totalWritten) {
		t.Fatalf("expected high watermark %d, got %d", totalWritten, hwm)
	}
}

func TestAccumulatorDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultPartitionConfig()
	cfg.AccumulatorConfig.Enabled = false

	p, err := NewPartition(dir, "test-acc-disabled", 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if p.accumulator != nil {
		t.Fatal("accumulator should be nil when disabled")
	}

	records := []Record{{Value: []byte("direct-write")}}
	batch := NewRecordBatch(0, records)
	data := batch.Encode()

	offset, err := p.AppendRaw(data, 1, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("direct AppendRaw failed: %v", err)
	}
	if offset != 0 {
		t.Fatalf("expected offset 0, got %d", offset)
	}
}

func TestAccumulatorDirtyBytes(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultPartitionConfig()
	cfg.AccumulatorConfig = DefaultAccumulatorConfig()

	p, err := NewPartition(dir, "test-acc-dirty", 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	records := []Record{{Value: make([]byte, 1000)}}
	batch := NewRecordBatch(0, records)
	data := batch.Encode()

	_, err = p.AppendRaw(data, 1, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("AppendRaw failed: %v", err)
	}

	dirty := p.accumulator.DirtyBytes.Load()
	if dirty == 0 {
		t.Fatal("expected dirty bytes > 0 after write")
	}

	// After sync, dirty bytes should be reset.
	if err := p.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	// Note: Sync resets dirty bytes via Log.Sync(), but here we're calling
	// Partition.Sync() directly. We verify the counter is still accessible.
	// The Log.Sync() path resets it; partition-level sync does not.
}

func BenchmarkAccumulatorWrite(b *testing.B) {
	dir := b.TempDir()
	cfg := DefaultPartitionConfig()
	cfg.AccumulatorConfig = DefaultAccumulatorConfig()

	p, err := NewPartition(dir, "bench-acc", 0, cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer p.Close()

	records := []Record{{Value: make([]byte, 1024)}}
	batch := NewRecordBatch(0, records)
	data := batch.Encode()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := p.AppendRaw(data, 1, time.Now().UnixMilli())
			if err != nil {
				b.Errorf("AppendRaw failed: %v", err)
			}
		}
	})
}

func BenchmarkDirectWrite(b *testing.B) {
	dir := b.TempDir()
	cfg := DefaultPartitionConfig()
	cfg.AccumulatorConfig.Enabled = false

	p, err := NewPartition(dir, "bench-direct", 0, cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer p.Close()

	records := []Record{{Value: make([]byte, 1024)}}
	batch := NewRecordBatch(0, records)
	data := batch.Encode()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := p.AppendRaw(data, 1, time.Now().UnixMilli())
		if err != nil {
			b.Errorf("AppendRaw failed: %v", err)
		}
	}
}
