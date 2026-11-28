package compact

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type PipelineConfig struct {
	Dir              string
	IndexID          string
	Workers          int
	MaxBatchSize     int
	FlushInterval    time.Duration
	CompactorCfg     CompactorConfig
	MergeSelector    MergeSelector
	RetentionPolicy  RetentionPolicy
	Hooks            PipelineHooks
}

type PipelineHooks struct {
	OnStageStart  func(stage string)
	OnStageEnd    func(stage string, duration time.Duration, err error)
	OnFileCreated func(path string, size int64)
	OnFileRemoved func(path string)
}

func DefaultPipelineConfig(dir string) PipelineConfig {
	return PipelineConfig{
		Dir:             dir,
		IndexID:         "hnsw",
		Workers:         4,
		MaxBatchSize:    1000,
		FlushInterval:   5 * time.Second,
		CompactorCfg:    DefaultCompactorConfig(dir),
		MergeSelector:   DefaultMergeSelector(),
		RetentionPolicy: DefaultRetentionPolicy(),
	}
}

type Pipeline struct {
	cfg         PipelineConfig
	discovery   *FileDiscovery
	mu          sync.Mutex
	running     atomic.Bool
	metrics     PipelineMetrics
}

type PipelineMetrics struct {
	RunCount       atomic.Int64
	WALConverted   atomic.Int64
	FilesMerged    atomic.Int64
	SnapshotsCreated atomic.Int64
	BytesRead      atomic.Int64
	BytesWritten   atomic.Int64
	TotalDuration  atomic.Int64
	LastRunAt      atomic.Int64
	Errors         atomic.Int64
}

func NewPipeline(cfg PipelineConfig) *Pipeline {
	return &Pipeline{
		cfg:       cfg,
		discovery: NewFileDiscovery(cfg.Dir),
	}
}

func (p *Pipeline) Run() (*PipelineResult, error) {
	if !p.running.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("pipeline already running")
	}
	defer p.running.Store(false)

	start := time.Now()
	p.metrics.RunCount.Add(1)

	result := &PipelineResult{StartedAt: start}

	state, err := p.discovery.Scan()
	if err != nil {
		p.metrics.Errors.Add(1)
		return result, fmt.Errorf("scan: %w", err)
	}
	result.InitialState = stateSnapshot(state)

	if len(state.Overlaps) > 0 {
		p.runStage("cleanup", result, func() error {
			return p.cleanupOverlaps(state)
		})
		state, _ = p.discovery.Scan()
	}

	if len(state.WALFiles) > 0 {
		p.runStage("convert", result, func() error {
			count, err := p.convertWALs(state)
			result.WALsConverted = count
			return err
		})
		state, _ = p.discovery.Scan()
	}

	if candidate := p.cfg.MergeSelector.Select(state.SortedFiles, 0); candidate != nil {
		p.runStage("merge", result, func() error {
			count, err := p.mergeSorted(state, candidate)
			result.FilesMerged = count
			return err
		})
		state, _ = p.discovery.Scan()
	}

	totalSize := state.TotalSize()
	if totalSize > p.cfg.CompactorCfg.SnapshotThreshold {
		p.runStage("snapshot", result, func() error {
			return p.createSnapshot(state)
		})
		result.SnapshotCreated = true
		state, _ = p.discovery.Scan()
	}

	p.runStage("retain", result, func() error {
		deleted, err := p.cfg.RetentionPolicy.Apply(p.cfg.Dir)
		result.FilesRetired = len(deleted)
		return err
	})

	result.Duration = time.Since(start)
	p.metrics.TotalDuration.Add(result.Duration.Nanoseconds())
	p.metrics.LastRunAt.Store(time.Now().UnixNano())

	finalState, _ := p.discovery.Scan()
	result.FinalState = stateSnapshot(finalState)

	return result, nil
}

func (p *Pipeline) runStage(name string, result *PipelineResult, fn func() error) {
	start := time.Now()
	if p.cfg.Hooks.OnStageStart != nil {
		p.cfg.Hooks.OnStageStart(name)
	}

	err := fn()
	duration := time.Since(start)

	result.Stages = append(result.Stages, StageResult{
		Name:     name,
		Duration: duration,
		Error:    err,
	})

	if err != nil {
		p.metrics.Errors.Add(1)
		log.Printf("[pipeline] stage %s failed: %v", name, err)
	}

	if p.cfg.Hooks.OnStageEnd != nil {
		p.cfg.Hooks.OnStageEnd(name, duration, err)
	}
}

func (p *Pipeline) cleanupOverlaps(state *DirectoryState) error {
	for _, o := range state.Overlaps {
		smaller := o.FileA
		if o.FileB.Size < smaller.Size {
			smaller = o.FileB
		}
		os.Remove(smaller.Path)
		if p.cfg.Hooks.OnFileRemoved != nil {
			p.cfg.Hooks.OnFileRemoved(smaller.Path)
		}
	}
	return nil
}

func (p *Pipeline) convertWALs(state *DirectoryState) (int, error) {
	pool := NewWorkerPool(p.cfg.Workers)
	commits, err := pool.ProcessFiles(state.WALFiles)
	if err != nil {
		return 0, err
	}

	if len(commits) == 0 {
		return 0, nil
	}
	p.metrics.BytesRead.Add(state.TotalWALSize())

	globals, byNode := SplitByNode(commits)
	nodes := sortedNodeList(byNode)

	outputPath := filepath.Join(p.cfg.Dir,
		BuildSortedFilename(p.cfg.IndexID,
			state.WALFiles[0].StartTS, time.Now().UnixNano()))

	if err := WriteSortedFile(outputPath, globals, nodes); err != nil {
		return 0, err
	}

	info, _ := os.Stat(outputPath)
	if info != nil {
		p.metrics.BytesWritten.Add(info.Size())
		if p.cfg.Hooks.OnFileCreated != nil {
			p.cfg.Hooks.OnFileCreated(outputPath, info.Size())
		}
	}

	for _, f := range state.WALFiles {
		os.Remove(f.Path)
		if p.cfg.Hooks.OnFileRemoved != nil {
			p.cfg.Hooks.OnFileRemoved(f.Path)
		}
	}

	p.metrics.WALConverted.Add(int64(len(state.WALFiles)))
	return len(state.WALFiles), nil
}

func (p *Pipeline) mergeSorted(state *DirectoryState, candidate *MergeCandidate) (int, error) {
	var allCommits []Commit
	for _, f := range candidate.Files {
		reader, closer, err := OpenWALFile(f.Path)
		if err != nil {
			continue
		}
		commits, _ := reader.ReadAll()
		closer.Close()
		allCommits = append(allCommits, commits...)
		p.metrics.BytesRead.Add(f.Size)
	}

	globals, byNode := SplitByNode(allCommits)
	nodes := sortedNodeList(byNode)

	for _, nc := range nodes {
		nc.Commits = deduplicateCommits(nc.Commits)
	}

	var startTS, endTS int64
	for _, f := range candidate.Files {
		if startTS == 0 || f.StartTS < startTS {
			startTS = f.StartTS
		}
		if f.EndTS > endTS {
			endTS = f.EndTS
		}
	}

	outputPath := filepath.Join(p.cfg.Dir,
		BuildSortedFilename(p.cfg.IndexID, startTS, endTS))

	if err := WriteSortedFile(outputPath, globals, nodes); err != nil {
		return 0, err
	}

	info, _ := os.Stat(outputPath)
	if info != nil {
		p.metrics.BytesWritten.Add(info.Size())
	}

	for _, f := range candidate.Files {
		os.Remove(f.Path)
	}

	p.metrics.FilesMerged.Add(int64(len(candidate.Files)))
	return len(candidate.Files), nil
}

func (p *Pipeline) createSnapshot(state *DirectoryState) error {
	var allCommits []Commit
	for _, f := range state.SortedFiles {
		reader, closer, err := OpenWALFile(f.Path)
		if err != nil {
			continue
		}
		commits, _ := reader.ReadAll()
		closer.Close()
		allCommits = append(allCommits, commits...)
	}
	for _, f := range state.WALFiles {
		reader, closer, err := OpenWALFile(f.Path)
		if err != nil {
			continue
		}
		commits, _ := reader.ReadAll()
		closer.Close()
		allCommits = append(allCommits, commits...)
	}

	if len(allCommits) == 0 {
		return nil
	}

	globals, byNode := SplitByNode(allCommits)
	nodes := sortedNodeList(byNode)
	for _, nc := range nodes {
		nc.Commits = deduplicateCommits(nc.Commits)
	}

	snapPath := filepath.Join(p.cfg.Dir,
		BuildSnapshotFilename(p.cfg.IndexID, time.Now().UnixNano(), 0))

	if err := WriteSortedFile(snapPath, globals, nodes); err != nil {
		return err
	}

	for _, f := range state.SortedFiles {
		os.Remove(f.Path)
	}
	for _, f := range state.WALFiles {
		os.Remove(f.Path)
	}

	p.metrics.SnapshotsCreated.Add(1)
	return nil
}

func (p *Pipeline) Metrics() map[string]interface{} {
	return map[string]interface{}{
		"runs":               p.metrics.RunCount.Load(),
		"wal_converted":      p.metrics.WALConverted.Load(),
		"files_merged":       p.metrics.FilesMerged.Load(),
		"snapshots_created":  p.metrics.SnapshotsCreated.Load(),
		"bytes_read":         p.metrics.BytesRead.Load(),
		"bytes_written":      p.metrics.BytesWritten.Load(),
		"errors":             p.metrics.Errors.Load(),
		"total_duration_ms":  p.metrics.TotalDuration.Load() / 1e6,
	}
}

func (p *Pipeline) IsRunning() bool {
	return p.running.Load()
}

type PipelineResult struct {
	StartedAt       time.Time
	Duration        time.Duration
	Stages          []StageResult
	WALsConverted   int
	FilesMerged     int
	SnapshotCreated bool
	FilesRetired    int
	InitialState    StateSnapshot
	FinalState      StateSnapshot
}

type StageResult struct {
	Name     string
	Duration time.Duration
	Error    error
}

type StateSnapshot struct {
	WALFiles    int
	SortedFiles int
	Snapshots   int
	TotalSize   int64
}

func stateSnapshot(s *DirectoryState) StateSnapshot {
	return StateSnapshot{
		WALFiles:    len(s.WALFiles),
		SortedFiles: len(s.SortedFiles),
		Snapshots:   len(s.SnapshotFiles),
		TotalSize:   s.TotalSize(),
	}
}

func (pr *PipelineResult) Summary() string {
	stages := ""
	for _, s := range pr.Stages {
		status := "ok"
		if s.Error != nil {
			status = fmt.Sprintf("err: %v", s.Error)
		}
		stages += fmt.Sprintf("  %s: %v (%s)\n", s.Name, s.Duration, status)
	}
	return fmt.Sprintf("Pipeline completed in %v:\n%s"+
		"  WALs converted: %d, merged: %d, snapshot: %v, retired: %d\n"+
		"  Before: wal=%d sorted=%d snap=%d (%s)\n"+
		"  After:  wal=%d sorted=%d snap=%d (%s)\n",
		pr.Duration, stages,
		pr.WALsConverted, pr.FilesMerged, pr.SnapshotCreated, pr.FilesRetired,
		pr.InitialState.WALFiles, pr.InitialState.SortedFiles, pr.InitialState.Snapshots,
		formatBytes(pr.InitialState.TotalSize),
		pr.FinalState.WALFiles, pr.FinalState.SortedFiles, pr.FinalState.Snapshots,
		formatBytes(pr.FinalState.TotalSize))
}

func sortedNodeList(byNode map[uint64]*NodeCommits) []*NodeCommits {
	nodeIDs := make([]uint64, 0, len(byNode))
	for id := range byNode {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
	nodes := make([]*NodeCommits, len(nodeIDs))
	for i, id := range nodeIDs {
		nodes[i] = byNode[id]
	}
	return nodes
}
