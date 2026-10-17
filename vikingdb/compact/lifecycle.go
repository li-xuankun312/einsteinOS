package compact

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type LifecycleConfig struct {
	Dir                string
	IndexID            string
	RotationCfg        RotationConfig
	CompactorCfg       CompactorConfig
	PlannerCfg         PlannerConfig
	PipelineCfg        PipelineConfig
	RetentionPolicy    RetentionPolicy
	CheckInterval      time.Duration
	AutoCompact        bool
	AutoGC             bool
	AutoSnapshot       bool
	SnapshotInterval   time.Duration
	GCInterval         time.Duration
	MaxDiskUsage       int64
	DiskWarningRatio   float64
}

func DefaultLifecycleConfig(dir string) LifecycleConfig {
	return LifecycleConfig{
		Dir:              dir,
		IndexID:          "hnsw",
		RotationCfg:      DefaultRotationConfig(dir, "hnsw"),
		CompactorCfg:     DefaultCompactorConfig(dir),
		PlannerCfg:       DefaultPlannerConfig(dir),
		PipelineCfg:      DefaultPipelineConfig(dir),
		RetentionPolicy:  DefaultRetentionPolicy(),
		CheckInterval:    30 * time.Second,
		AutoCompact:      true,
		AutoGC:           true,
		AutoSnapshot:     true,
		SnapshotInterval: 10 * time.Minute,
		GCInterval:       5 * time.Minute,
		MaxDiskUsage:     2 << 30,
		DiskWarningRatio: 0.8,
	}
}

type Lifecycle struct {
	cfg           LifecycleConfig
	rotator       *WALRotator
	pipeline      *Pipeline
	planner       *Planner
	checkpoint    *PersistentCheckpoint
	stats         *CompactionStats
	sizeTracker   *SizeTracker
	discovery     *FileDiscovery

	mu            sync.Mutex
	stopCh        chan struct{}
	stopped       bool
	lastCompact   time.Time
	lastGC        time.Time
	lastSnapshot  time.Time
	warnings      []string
	warningCount  atomic.Int64
}

func NewLifecycle(cfg LifecycleConfig) (*Lifecycle, error) {
	if err := os.MkdirAll(cfg.Dir, 0755); err != nil {
		return nil, err
	}

	rotator, err := NewWALRotator(cfg.RotationCfg)
	if err != nil {
		return nil, fmt.Errorf("create rotator: %w", err)
	}

	lc := &Lifecycle{
		cfg:         cfg,
		rotator:     rotator,
		pipeline:    NewPipeline(cfg.PipelineCfg),
		planner:     NewPlanner(cfg.PlannerCfg),
		checkpoint:  NewPersistentCheckpoint(cfg.Dir, cfg.IndexID),
		stats:       NewCompactionStats(),
		sizeTracker: NewSizeTracker(360),
		discovery:   NewFileDiscovery(cfg.Dir),
		stopCh:      make(chan struct{}),
	}

	if err := lc.checkpoint.Load(); err != nil {
		log.Printf("[lifecycle] checkpoint load: %v (starting fresh)", err)
	}

	return lc, nil
}

func (lc *Lifecycle) Start() {
	go lc.mainLoop()
	log.Printf("[lifecycle] started for %s in %s", lc.cfg.IndexID, lc.cfg.Dir)
}

func (lc *Lifecycle) Stop() error {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	if lc.stopped {
		return nil
	}
	lc.stopped = true
	close(lc.stopCh)

	lc.rotator.Flush()
	lc.rotator.Close()
	lc.checkpoint.Save()

	log.Printf("[lifecycle] stopped")
	return nil
}

func (lc *Lifecycle) mainLoop() {
	ticker := time.NewTicker(lc.cfg.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-lc.stopCh:
			return
		case <-ticker.C:
			lc.tick()
		}
	}
}

func (lc *Lifecycle) tick() {
	lc.sizeTracker.Record(lc.cfg.Dir)
	lc.checkDiskUsage()

	if lc.cfg.AutoCompact && time.Since(lc.lastCompact) > lc.cfg.CheckInterval*2 {
		lc.maybeCompact()
	}

	if lc.cfg.AutoSnapshot && time.Since(lc.lastSnapshot) > lc.cfg.SnapshotInterval {
		lc.maybeSnapshot()
	}

	if lc.cfg.AutoGC && time.Since(lc.lastGC) > lc.cfg.GCInterval {
		lc.runGC()
	}
}

func (lc *Lifecycle) maybeCompact() {
	plan, err := lc.planner.Plan()
	if err != nil {
		lc.addWarning("plan error: " + err.Error())
		return
	}
	if plan.IsEmpty() {
		return
	}

	start := time.Now()
	result, err := lc.pipeline.Run()
	duration := time.Since(start)

	if err != nil {
		lc.stats.RecordRun(ActionNone, duration, 0, 0, err)
		lc.addWarning("compact error: " + err.Error())
	} else if result != nil {
		action := ActionConvertToSort
		if result.SnapshotCreated {
			action = ActionCreateSnapshot
		}
		lc.stats.RecordRun(action, duration, 0, 0, nil)
		lc.checkpoint.IncrCompactCycles()
		lc.checkpoint.Save()
	}

	lc.lastCompact = time.Now()
}

func (lc *Lifecycle) maybeSnapshot() {
	state, err := lc.discovery.Scan()
	if err != nil {
		return
	}

	if state.TotalSize() < lc.cfg.CompactorCfg.SnapshotThreshold/2 {
		return
	}

	lc.lastSnapshot = time.Now()
	lc.checkpoint.SetSnapshotTS(time.Now().UnixNano())
	lc.checkpoint.Save()
}

func (lc *Lifecycle) runGC() {
	gc := NewGarbageCollector(lc.cfg.Dir, false)
	deleted, err := gc.Run()
	if err != nil {
		lc.addWarning("gc error: " + err.Error())
	}
	if len(deleted) > 0 {
		lc.stats.RecordFilesDeleted(len(deleted))
		log.Printf("[lifecycle] GC: removed %d files", len(deleted))
	}

	deleted2, _ := lc.cfg.RetentionPolicy.Apply(lc.cfg.Dir)
	if len(deleted2) > 0 {
		lc.stats.RecordFilesDeleted(len(deleted2))
	}

	lc.lastGC = time.Now()
}

func (lc *Lifecycle) checkDiskUsage() {
	latest := lc.sizeTracker.Latest()
	if latest == nil {
		return
	}

	if lc.cfg.MaxDiskUsage > 0 && latest.totalSize > lc.cfg.MaxDiskUsage {
		lc.addWarning(fmt.Sprintf("disk usage %s exceeds limit %s",
			formatBytes(latest.totalSize), formatBytes(lc.cfg.MaxDiskUsage)))
	}

	warningLevel := int64(float64(lc.cfg.MaxDiskUsage) * lc.cfg.DiskWarningRatio)
	if lc.cfg.MaxDiskUsage > 0 && latest.totalSize > warningLevel {
		lc.addWarning(fmt.Sprintf("disk usage %s above %.0f%% of limit",
			formatBytes(latest.totalSize), lc.cfg.DiskWarningRatio*100))
	}
}

func (lc *Lifecycle) addWarning(msg string) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.warnings = append(lc.warnings, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg))
	if len(lc.warnings) > 100 {
		lc.warnings = lc.warnings[len(lc.warnings)-50:]
	}
	lc.warningCount.Add(1)
}

func (lc *Lifecycle) WriteCommit(c Commit) error {
	return lc.rotator.Write(c)
}

func (lc *Lifecycle) WriteBatch(commits []Commit) error {
	return lc.rotator.WriteBatch(commits)
}

func (lc *Lifecycle) Flush() error {
	return lc.rotator.Flush()
}

func (lc *Lifecycle) ForceCompact() error {
	_, err := lc.pipeline.Run()
	return err
}

func (lc *Lifecycle) ForceGC() {
	lc.runGC()
}

func (lc *Lifecycle) Warnings() []string {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	out := make([]string, len(lc.warnings))
	copy(out, lc.warnings)
	return out
}

func (lc *Lifecycle) Stats() map[string]interface{} {
	return map[string]interface{}{
		"compaction":  lc.stats.Snapshot(),
		"rotator":    lc.rotator.Stats(),
		"checkpoint": lc.checkpoint.Summary(),
		"size":       lc.sizeTracker.Summary(),
		"pipeline":   lc.pipeline.Metrics(),
		"warnings":   lc.warningCount.Load(),
	}
}

func (lc *Lifecycle) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Lifecycle [%s]:\n", lc.cfg.IndexID))
	sb.WriteString(fmt.Sprintf("  Dir: %s\n", lc.cfg.Dir))
	sb.WriteString(fmt.Sprintf("  Auto: compact=%v snapshot=%v gc=%v\n",
		lc.cfg.AutoCompact, lc.cfg.AutoSnapshot, lc.cfg.AutoGC))
	sb.WriteString(fmt.Sprintf("  Last: compact=%s snapshot=%s gc=%s\n",
		timeSince(lc.lastCompact), timeSince(lc.lastSnapshot), timeSince(lc.lastGC)))
	sb.WriteString(fmt.Sprintf("  %s\n", lc.stats.Summary()))
	sb.WriteString(fmt.Sprintf("  %s\n", lc.sizeTracker.Summary()))
	if wc := lc.warningCount.Load(); wc > 0 {
		sb.WriteString(fmt.Sprintf("  Warnings: %d\n", wc))
	}
	return sb.String()
}

func timeSince(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return time.Since(t).Round(time.Second).String() + " ago"
}

func DirectoryReport(dir string) (string, error) {
	discovery := NewFileDiscovery(dir)
	state, err := discovery.Scan()
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== VikingDB Directory Report ===\n"))
	sb.WriteString(fmt.Sprintf("Path: %s\n\n", dir))

	stats, err := GetDirectoryStats(dir)
	if err == nil {
		sb.WriteString(stats.Summary())
		sb.WriteString("\n")
	}

	health, err := CheckCompactionHealth(dir, DefaultCompactorConfig(dir))
	if err == nil {
		sb.WriteString(health.Summary())
		sb.WriteString("\n\n")
	}

	sa, err := CalculateSpaceAmplification(dir)
	if err == nil {
		sb.WriteString(sa.Summary())
		sb.WriteString("\n\n")
	}

	if len(state.WALFiles) > 0 {
		sb.WriteString(fmt.Sprintf("WAL Files (%d):\n", len(state.WALFiles)))
		for _, f := range state.WALFiles {
			sb.WriteString(fmt.Sprintf("  %s (%s)\n", filepath.Base(f.Path), formatBytes(f.Size)))
		}
		sb.WriteString("\n")
	}

	if len(state.SortedFiles) > 0 {
		sb.WriteString(fmt.Sprintf("Sorted Files (%d):\n", len(state.SortedFiles)))
		for _, f := range state.SortedFiles {
			sb.WriteString(fmt.Sprintf("  %s (%s)\n", filepath.Base(f.Path), formatBytes(f.Size)))
		}
		sb.WriteString("\n")
	}

	if len(state.SnapshotFiles) > 0 {
		sb.WriteString(fmt.Sprintf("Snapshot Files (%d):\n", len(state.SnapshotFiles)))
		sorted := make([]FileInfo, len(state.SnapshotFiles))
		copy(sorted, state.SnapshotFiles)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].EndTS > sorted[j].EndTS
		})
		for _, f := range sorted {
			sb.WriteString(fmt.Sprintf("  %s (%s)\n", filepath.Base(f.Path), formatBytes(f.Size)))
		}
		sb.WriteString("\n")
	}

	if len(state.Overlaps) > 0 {
		sb.WriteString(fmt.Sprintf("Overlaps (%d):\n", len(state.Overlaps)))
		for _, o := range state.Overlaps {
			sb.WriteString(fmt.Sprintf("  %s ↔ %s\n",
				filepath.Base(o.FileA.Path), filepath.Base(o.FileB.Path)))
		}
	}

	return sb.String(), nil
}
