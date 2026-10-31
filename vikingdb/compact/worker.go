package compact

import (
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type WorkerPool struct {
	workers    int
	jobCh      chan workerJob
	resultCh   chan workerResult
	wg         sync.WaitGroup
	processed  atomic.Int64
	errors     atomic.Int64
	bytesRead  atomic.Int64
	bytesWrite atomic.Int64
	startTime  time.Time
}

type workerJob struct {
	id   int
	file FileInfo
}

type workerResult struct {
	id       int
	file     FileInfo
	commits  []Commit
	err      error
	duration time.Duration
}

func NewWorkerPool(workers int) *WorkerPool {
	if workers <= 0 {
		workers = 4
	}
	return &WorkerPool{
		workers:  workers,
		jobCh:    make(chan workerJob, workers*2),
		resultCh: make(chan workerResult, workers*2),
	}
}

func (wp *WorkerPool) ProcessFiles(files []FileInfo) ([]Commit, error) {
	wp.startTime = time.Now()

	for i := 0; i < wp.workers; i++ {
		wp.wg.Add(1)
		go wp.worker()
	}

	go func() {
		for i, f := range files {
			wp.jobCh <- workerJob{id: i, file: f}
		}
		close(wp.jobCh)
	}()

	go func() {
		wp.wg.Wait()
		close(wp.resultCh)
	}()

	resultMap := make(map[int][]Commit)
	var firstErr error

	for result := range wp.resultCh {
		if result.err != nil {
			wp.errors.Add(1)
			if firstErr == nil {
				firstErr = result.err
			}
			log.Printf("[worker] file %s error: %v", result.file.Path, result.err)
			continue
		}
		resultMap[result.id] = result.commits
		wp.processed.Add(1)
		wp.bytesRead.Add(result.file.Size)
	}

	ids := make([]int, 0, len(resultMap))
	for id := range resultMap {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	var allCommits []Commit
	for _, id := range ids {
		allCommits = append(allCommits, resultMap[id]...)
	}

	return allCommits, firstErr
}

func (wp *WorkerPool) worker() {
	defer wp.wg.Done()
	for job := range wp.jobCh {
		start := time.Now()
		commits, err := wp.processFile(job.file)
		wp.resultCh <- workerResult{
			id:       job.id,
			file:     job.file,
			commits:  commits,
			err:      err,
			duration: time.Since(start),
		}
	}
}

func (wp *WorkerPool) processFile(f FileInfo) ([]Commit, error) {
	reader, closer, err := OpenWALFile(f.Path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", f.Path, err)
	}
	defer closer.Close()
	return reader.ReadAll()
}

func (wp *WorkerPool) Stats() map[string]interface{} {
	elapsed := time.Since(wp.startTime)
	processed := wp.processed.Load()
	bytesRead := wp.bytesRead.Load()
	throughput := float64(0)
	if elapsed.Seconds() > 0 {
		throughput = float64(bytesRead) / elapsed.Seconds() / 1024 / 1024
	}
	return map[string]interface{}{
		"workers":     wp.workers,
		"processed":   processed,
		"errors":      wp.errors.Load(),
		"bytes_read":  bytesRead,
		"elapsed":     elapsed.String(),
		"throughput":  fmt.Sprintf("%.1f MB/s", throughput),
	}
}

type Checkpoint struct {
	mu           sync.RWMutex
	dir          string
	lastTS       int64
	lastSeq      int
	processedIDs map[string]bool
}

func NewCheckpoint(dir string) *Checkpoint {
	return &Checkpoint{
		dir:          dir,
		processedIDs: make(map[string]bool),
	}
}

func (cp *Checkpoint) Mark(f FileInfo) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.processedIDs[f.Path] = true
	if f.EndTS > cp.lastTS {
		cp.lastTS = f.EndTS
	}
	if f.SeqNo > cp.lastSeq {
		cp.lastSeq = f.SeqNo
	}
}

func (cp *Checkpoint) IsProcessed(f FileInfo) bool {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.processedIDs[f.Path]
}

func (cp *Checkpoint) FilterUnprocessed(files []FileInfo) []FileInfo {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	var result []FileInfo
	for _, f := range files {
		if !cp.processedIDs[f.Path] {
			result = append(result, f)
		}
	}
	return result
}

func (cp *Checkpoint) LastTS() int64 {
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.lastTS
}

func (cp *Checkpoint) Reset() {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.lastTS = 0
	cp.lastSeq = 0
	cp.processedIDs = make(map[string]bool)
}

type ProgressTracker struct {
	mu          sync.Mutex
	total       int
	completed   int
	failed      int
	startTime   time.Time
	callbacks   []func(float64)
}

func NewProgressTracker(total int) *ProgressTracker {
	return &ProgressTracker{
		total:     total,
		startTime: time.Now(),
	}
}

func (pt *ProgressTracker) OnProgress(fn func(float64)) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.callbacks = append(pt.callbacks, fn)
}

func (pt *ProgressTracker) Complete() {
	pt.mu.Lock()
	pt.completed++
	progress := pt.Progress()
	callbacks := pt.callbacks
	pt.mu.Unlock()

	for _, fn := range callbacks {
		fn(progress)
	}
}

func (pt *ProgressTracker) Fail() {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.failed++
}

func (pt *ProgressTracker) Progress() float64 {
	if pt.total == 0 {
		return 1.0
	}
	return float64(pt.completed) / float64(pt.total)
}

func (pt *ProgressTracker) ETA() time.Duration {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	if pt.completed == 0 {
		return 0
	}
	elapsed := time.Since(pt.startTime)
	perItem := elapsed / time.Duration(pt.completed)
	remaining := pt.total - pt.completed
	return perItem * time.Duration(remaining)
}

func (pt *ProgressTracker) Summary() string {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return fmt.Sprintf("Progress: %d/%d (%.0f%%) failed=%d eta=%v",
		pt.completed, pt.total, pt.Progress()*100, pt.failed, pt.ETA())
}

type BatchProcessor struct {
	pool       *WorkerPool
	checkpoint *Checkpoint
	tracker    *ProgressTracker
	cfg        CompactorConfig
}

func NewBatchProcessor(cfg CompactorConfig, workers int) *BatchProcessor {
	return &BatchProcessor{
		pool:       NewWorkerPool(workers),
		checkpoint: NewCheckpoint(cfg.Dir),
		cfg:        cfg,
	}
}

func (bp *BatchProcessor) ProcessDirectory() (*ProcessResult, error) {
	discovery := NewFileDiscovery(bp.cfg.Dir)
	state, err := discovery.Scan()
	if err != nil {
		return nil, err
	}

	files := make([]FileInfo, 0)
	files = append(files, state.WALFiles...)
	files = append(files, state.SortedFiles...)

	unprocessed := bp.checkpoint.FilterUnprocessed(files)
	if len(unprocessed) == 0 {
		return &ProcessResult{Skipped: len(files)}, nil
	}

	bp.tracker = NewProgressTracker(len(unprocessed))
	commits, err := bp.pool.ProcessFiles(unprocessed)

	for _, f := range unprocessed {
		bp.checkpoint.Mark(f)
		bp.tracker.Complete()
	}

	result := &ProcessResult{
		TotalFiles: len(files),
		Processed:  len(unprocessed),
		Skipped:    len(files) - len(unprocessed),
		Commits:    len(commits),
		Duration:   time.Since(bp.tracker.startTime),
	}

	if len(commits) > 0 {
		globals, byNode := SplitByNode(commits)
		nodes := make([]*NodeCommits, 0, len(byNode))
		for _, nc := range byNode {
			nc.Commits = deduplicateCommits(nc.Commits)
			nodes = append(nodes, nc)
		}
		sort.Slice(nodes, func(i, j int) bool {
			return nodes[i].NodeID < nodes[j].NodeID
		})
		result.Globals = len(globals)
		result.Nodes = len(nodes)
	}

	return result, err
}

type ProcessResult struct {
	TotalFiles int
	Processed  int
	Skipped    int
	Commits    int
	Globals    int
	Nodes      int
	Duration   time.Duration
}

func (r *ProcessResult) Summary() string {
	return fmt.Sprintf("Processed %d/%d files, %d commits (%d global, %d nodes) in %v",
		r.Processed, r.TotalFiles, r.Commits, r.Globals, r.Nodes, r.Duration)
}
