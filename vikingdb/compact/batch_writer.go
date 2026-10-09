package compact

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type BatchWriterConfig struct {
	Dir           string
	Prefix        string
	MaxBatchSize  int
	MaxBatchWait  time.Duration
	MaxFileSize   int64
	SyncOnWrite   bool
	BufferSize    int
}

func DefaultBatchWriterConfig(dir, prefix string) BatchWriterConfig {
	return BatchWriterConfig{
		Dir:          dir,
		Prefix:       prefix,
		MaxBatchSize: 256,
		MaxBatchWait: 50 * time.Millisecond,
		MaxFileSize:  64 * 1024 * 1024,
		SyncOnWrite:  false,
		BufferSize:   128 * 1024,
	}
}

type BatchWriter struct {
	cfg        BatchWriterConfig
	mu         sync.Mutex
	pending    []Commit
	rotator    *WALRotator
	flushCh    chan struct{}
	stopCh     chan struct{}
	wg         sync.WaitGroup
	written    atomic.Int64
	flushed    atomic.Int64
	batches    atomic.Int64
	maxPending int
	lastFlush  time.Time
}

func NewBatchWriter(cfg BatchWriterConfig) (*BatchWriter, error) {
	rotCfg := DefaultRotationConfig(cfg.Dir, cfg.Prefix)
	rotCfg.MaxFileSize = cfg.MaxFileSize
	rotator, err := NewWALRotator(rotCfg)
	if err != nil {
		return nil, err
	}
	bw := &BatchWriter{
		cfg:     cfg,
		pending: make([]Commit, 0, cfg.MaxBatchSize),
		rotator: rotator,
		flushCh: make(chan struct{}, 1),
		stopCh:  make(chan struct{}),
	}
	bw.wg.Add(1)
	go bw.flushLoop()
	return bw, nil
}

func (bw *BatchWriter) Write(c Commit) error {
	bw.mu.Lock()
	bw.pending = append(bw.pending, c)
	needsFlush := len(bw.pending) >= bw.cfg.MaxBatchSize
	if len(bw.pending) > bw.maxPending {
		bw.maxPending = len(bw.pending)
	}
	bw.mu.Unlock()
	bw.written.Add(1)
	if needsFlush {
		select {
		case bw.flushCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (bw *BatchWriter) WriteBatch(commits []Commit) error {
	bw.mu.Lock()
	bw.pending = append(bw.pending, commits...)
	needsFlush := len(bw.pending) >= bw.cfg.MaxBatchSize
	if len(bw.pending) > bw.maxPending {
		bw.maxPending = len(bw.pending)
	}
	bw.mu.Unlock()
	bw.written.Add(int64(len(commits)))
	if needsFlush {
		select {
		case bw.flushCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (bw *BatchWriter) flushLoop() {
	defer bw.wg.Done()
	ticker := time.NewTicker(bw.cfg.MaxBatchWait)
	defer ticker.Stop()
	for {
		select {
		case <-bw.stopCh:
			bw.doFlush()
			return
		case <-bw.flushCh:
			bw.doFlush()
		case <-ticker.C:
			bw.doFlush()
		}
	}
}

func (bw *BatchWriter) doFlush() {
	bw.mu.Lock()
	if len(bw.pending) == 0 {
		bw.mu.Unlock()
		return
	}
	batch := make([]Commit, len(bw.pending))
	copy(batch, bw.pending)
	bw.pending = bw.pending[:0]
	bw.mu.Unlock()
	if err := bw.rotator.WriteBatch(batch); err != nil {
		bw.mu.Lock()
		bw.pending = append(batch, bw.pending...)
		bw.mu.Unlock()
		return
	}
	if bw.cfg.SyncOnWrite {
		bw.rotator.Flush()
	}
	bw.flushed.Add(int64(len(batch)))
	bw.batches.Add(1)
	bw.lastFlush = time.Now()
}

func (bw *BatchWriter) Flush() error {
	bw.doFlush()
	return bw.rotator.Flush()
}

func (bw *BatchWriter) Close() error {
	close(bw.stopCh)
	bw.wg.Wait()
	return bw.rotator.Close()
}

func (bw *BatchWriter) PendingCount() int {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	return len(bw.pending)
}

func (bw *BatchWriter) Stats() map[string]interface{} {
	bw.mu.Lock()
	pending := len(bw.pending)
	maxPending := bw.maxPending
	bw.mu.Unlock()
	return map[string]interface{}{
		"written":     bw.written.Load(),
		"flushed":     bw.flushed.Load(),
		"batches":     bw.batches.Load(),
		"pending":     pending,
		"max_pending": maxPending,
		"last_flush":  bw.lastFlush.Format(time.RFC3339Nano),
		"rotator":     bw.rotator.Stats(),
	}
}

type WriteThrottler struct {
	mu         sync.Mutex
	maxRate    int64
	window     time.Duration
	tokens     int64
	lastRefill time.Time
}

func NewWriteThrottler(maxPerSecond int64) *WriteThrottler {
	return &WriteThrottler{
		maxRate:    maxPerSecond,
		window:     time.Second,
		tokens:     maxPerSecond,
		lastRefill: time.Now(),
	}
}

func (wt *WriteThrottler) Allow(n int64) bool {
	wt.mu.Lock()
	defer wt.mu.Unlock()
	wt.refill()
	if wt.tokens >= n {
		wt.tokens -= n
		return true
	}
	return false
}

func (wt *WriteThrottler) Wait(n int64) {
	for {
		if wt.Allow(n) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func (wt *WriteThrottler) refill() {
	now := time.Now()
	elapsed := now.Sub(wt.lastRefill)
	if elapsed < time.Millisecond {
		return
	}
	add := int64(float64(wt.maxRate) * elapsed.Seconds())
	wt.tokens += add
	if wt.tokens > wt.maxRate {
		wt.tokens = wt.maxRate
	}
	wt.lastRefill = now
}

func (wt *WriteThrottler) SetRate(maxPerSecond int64) {
	wt.mu.Lock()
	defer wt.mu.Unlock()
	wt.maxRate = maxPerSecond
}

type ThrottledWriter struct {
	writer    *BatchWriter
	throttler *WriteThrottler
}

func NewThrottledWriter(cfg BatchWriterConfig, maxPerSecond int64) (*ThrottledWriter, error) {
	bw, err := NewBatchWriter(cfg)
	if err != nil {
		return nil, err
	}
	return &ThrottledWriter{
		writer:    bw,
		throttler: NewWriteThrottler(maxPerSecond),
	}, nil
}

func (tw *ThrottledWriter) Write(c Commit) error {
	tw.throttler.Wait(1)
	return tw.writer.Write(c)
}

func (tw *ThrottledWriter) WriteBatch(commits []Commit) error {
	tw.throttler.Wait(int64(len(commits)))
	return tw.writer.WriteBatch(commits)
}

func (tw *ThrottledWriter) Flush() error {
	return tw.writer.Flush()
}

func (tw *ThrottledWriter) Close() error {
	return tw.writer.Close()
}

func (tw *ThrottledWriter) Stats() map[string]interface{} {
	return tw.writer.Stats()
}

type WALSizeMonitor struct {
	dir       string
	threshold int64
	callback  func(currentSize int64)
	interval  time.Duration
	stopCh    chan struct{}
}

func NewWALSizeMonitor(dir string, threshold int64, callback func(int64)) *WALSizeMonitor {
	return &WALSizeMonitor{
		dir:       dir,
		threshold: threshold,
		callback:  callback,
		interval:  10 * time.Second,
		stopCh:    make(chan struct{}),
	}
}

func (m *WALSizeMonitor) Start() {
	go func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.check()
			}
		}
	}()
}

func (m *WALSizeMonitor) Stop() {
	close(m.stopCh)
}

func (m *WALSizeMonitor) check() {
	var totalSize int64
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		totalSize += info.Size()
	}
	if totalSize > m.threshold && m.callback != nil {
		m.callback(totalSize)
	}
}

func FormatCommitLog(commits []Commit) string {
	if len(commits) == 0 {
		return "(empty)"
	}
	s := fmt.Sprintf("%d commits:\n", len(commits))
	limit := 50
	for i, c := range commits {
		if i >= limit {
			s += fmt.Sprintf("  ... and %d more\n", len(commits)-limit)
			break
		}
		switch c.Type {
		case CommitAddNodeLevel:
			s += fmt.Sprintf("  [%d] AddNode id=%d level=%d\n", i, c.NodeID, c.Level)
		case CommitSetEntryPoint:
			s += fmt.Sprintf("  [%d] SetEntryPoint id=%d\n", i, c.NodeID)
		case CommitReplaceLinks:
			s += fmt.Sprintf("  [%d] ReplaceLinks id=%d level=%d count=%d\n", i, c.NodeID, c.Level, len(c.Links))
		case CommitAddLink:
			s += fmt.Sprintf("  [%d] AddLink id=%d → %d level=%d\n", i, c.NodeID, c.Target, c.Level)
		case CommitDeleteNode:
			s += fmt.Sprintf("  [%d] DeleteNode id=%d\n", i, c.NodeID)
		case CommitAddTombstone:
			s += fmt.Sprintf("  [%d] AddTombstone id=%d\n", i, c.NodeID)
		case CommitRemoveTombstone:
			s += fmt.Sprintf("  [%d] RemoveTombstone id=%d\n", i, c.NodeID)
		case CommitSetMaxLevel:
			s += fmt.Sprintf("  [%d] SetMaxLevel level=%d\n", i, c.Level)
		case CommitResetIndex:
			s += fmt.Sprintf("  [%d] ResetIndex\n", i)
		default:
			s += fmt.Sprintf("  [%d] %s\n", i, c.Type)
		}
	}
	return s
}
