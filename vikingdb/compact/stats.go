package compact

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type CompactionStats struct {
	mu              sync.RWMutex
	totalRuns       atomic.Int64
	totalWALConvert atomic.Int64
	totalMerges     atomic.Int64
	totalSnapshots  atomic.Int64
	totalCleanups   atomic.Int64
	totalErrors     atomic.Int64
	bytesRead       atomic.Int64
	bytesWritten    atomic.Int64
	filesCreated    atomic.Int64
	filesDeleted    atomic.Int64
	runDurations    []time.Duration
	mergeInputSizes []int64
	startedAt       time.Time
}

func NewCompactionStats() *CompactionStats {
	return &CompactionStats{
		startedAt: time.Now(),
	}
}

func (cs *CompactionStats) RecordRun(action Action, duration time.Duration, bytesIn, bytesOut int64, err error) {
	cs.totalRuns.Add(1)
	cs.bytesRead.Add(bytesIn)
	cs.bytesWritten.Add(bytesOut)

	cs.mu.Lock()
	cs.runDurations = append(cs.runDurations, duration)
	if len(cs.runDurations) > 1000 {
		cs.runDurations = cs.runDurations[len(cs.runDurations)-500:]
	}
	cs.mu.Unlock()

	if err != nil {
		cs.totalErrors.Add(1)
	}

	switch action {
	case ActionConvertToSort:
		cs.totalWALConvert.Add(1)
	case ActionMergeSorted:
		cs.totalMerges.Add(1)
		cs.mu.Lock()
		cs.mergeInputSizes = append(cs.mergeInputSizes, bytesIn)
		cs.mu.Unlock()
	case ActionCreateSnapshot:
		cs.totalSnapshots.Add(1)
	case ActionCleanup:
		cs.totalCleanups.Add(1)
	}
}

func (cs *CompactionStats) RecordFileCreated()  { cs.filesCreated.Add(1) }
func (cs *CompactionStats) RecordFileDeleted()  { cs.filesDeleted.Add(1) }
func (cs *CompactionStats) RecordFilesCreated(n int) { cs.filesCreated.Add(int64(n)) }
func (cs *CompactionStats) RecordFilesDeleted(n int) { cs.filesDeleted.Add(int64(n)) }

func (cs *CompactionStats) AvgRunDuration() time.Duration {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if len(cs.runDurations) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range cs.runDurations {
		total += d
	}
	return total / time.Duration(len(cs.runDurations))
}

func (cs *CompactionStats) MaxRunDuration() time.Duration {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	var maxD time.Duration
	for _, d := range cs.runDurations {
		if d > maxD {
			maxD = d
		}
	}
	return maxD
}

func (cs *CompactionStats) WriteAmplification() float64 {
	read := cs.bytesRead.Load()
	if read == 0 {
		return 0
	}
	return float64(cs.bytesWritten.Load()) / float64(read)
}

func (cs *CompactionStats) Throughput() float64 {
	elapsed := time.Since(cs.startedAt).Seconds()
	if elapsed == 0 {
		return 0
	}
	return float64(cs.bytesWritten.Load()) / elapsed / 1024 / 1024
}

func (cs *CompactionStats) Snapshot() map[string]interface{} {
	return map[string]interface{}{
		"total_runs":        cs.totalRuns.Load(),
		"wal_converts":      cs.totalWALConvert.Load(),
		"merges":            cs.totalMerges.Load(),
		"snapshots":         cs.totalSnapshots.Load(),
		"cleanups":          cs.totalCleanups.Load(),
		"errors":            cs.totalErrors.Load(),
		"bytes_read":        cs.bytesRead.Load(),
		"bytes_written":     cs.bytesWritten.Load(),
		"files_created":     cs.filesCreated.Load(),
		"files_deleted":     cs.filesDeleted.Load(),
		"avg_run_duration":  cs.AvgRunDuration().String(),
		"max_run_duration":  cs.MaxRunDuration().String(),
		"write_amp":         fmt.Sprintf("%.2f", cs.WriteAmplification()),
		"throughput_mbps":   fmt.Sprintf("%.1f", cs.Throughput()),
		"uptime":            time.Since(cs.startedAt).String(),
	}
}

func (cs *CompactionStats) Summary() string {
	s := cs.Snapshot()
	var sb strings.Builder
	sb.WriteString("Compaction Stats:\n")
	sb.WriteString(fmt.Sprintf("  Runs: %v (wal=%v merge=%v snap=%v clean=%v err=%v)\n",
		s["total_runs"], s["wal_converts"], s["merges"], s["snapshots"], s["cleanups"], s["errors"]))
	sb.WriteString(fmt.Sprintf("  IO: read %s, write %s, amp %s\n",
		formatBytes(cs.bytesRead.Load()), formatBytes(cs.bytesWritten.Load()), s["write_amp"]))
	sb.WriteString(fmt.Sprintf("  Files: created %v, deleted %v\n", s["files_created"], s["files_deleted"]))
	sb.WriteString(fmt.Sprintf("  Latency: avg %s, max %s\n", s["avg_run_duration"], s["max_run_duration"]))
	sb.WriteString(fmt.Sprintf("  Throughput: %s MB/s over %s\n", s["throughput_mbps"], s["uptime"]))
	return sb.String()
}

func (cs *CompactionStats) Reset() {
	cs.totalRuns.Store(0)
	cs.totalWALConvert.Store(0)
	cs.totalMerges.Store(0)
	cs.totalSnapshots.Store(0)
	cs.totalCleanups.Store(0)
	cs.totalErrors.Store(0)
	cs.bytesRead.Store(0)
	cs.bytesWritten.Store(0)
	cs.filesCreated.Store(0)
	cs.filesDeleted.Store(0)
	cs.mu.Lock()
	cs.runDurations = nil
	cs.mergeInputSizes = nil
	cs.mu.Unlock()
	cs.startedAt = time.Now()
}

type SizeTracker struct {
	mu       sync.Mutex
	samples  []sizeSample
	maxLen   int
}

type sizeSample struct {
	ts        time.Time
	walSize   int64
	sortSize  int64
	snapSize  int64
	totalSize int64
	fileCount int
}

func NewSizeTracker(maxSamples int) *SizeTracker {
	if maxSamples <= 0 {
		maxSamples = 360
	}
	return &SizeTracker{maxLen: maxSamples}
}

func (st *SizeTracker) Record(dir string) error {
	stats, err := GetDirectoryStats(dir)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	st.samples = append(st.samples, sizeSample{
		ts:        time.Now(),
		walSize:   stats.WALSize,
		sortSize:  stats.SortedSize,
		snapSize:  stats.SnapshotSize,
		totalSize: stats.TotalSize,
		fileCount: stats.TotalFiles,
	})

	if len(st.samples) > st.maxLen {
		st.samples = st.samples[len(st.samples)-st.maxLen:]
	}

	return nil
}

func (st *SizeTracker) GrowthRate() float64 {
	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.samples) < 2 {
		return 0
	}

	first := st.samples[0]
	last := st.samples[len(st.samples)-1]
	elapsed := last.ts.Sub(first.ts).Seconds()
	if elapsed == 0 {
		return 0
	}

	return float64(last.totalSize-first.totalSize) / elapsed
}

func (st *SizeTracker) Latest() *sizeSample {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.samples) == 0 {
		return nil
	}
	s := st.samples[len(st.samples)-1]
	return &s
}

func (st *SizeTracker) EstimatedTimeToLimit(limitBytes int64) time.Duration {
	rate := st.GrowthRate()
	if rate <= 0 {
		return 0
	}
	latest := st.Latest()
	if latest == nil {
		return 0
	}
	remaining := limitBytes - latest.totalSize
	if remaining <= 0 {
		return 0
	}
	return time.Duration(float64(remaining)/rate) * time.Second
}

func (st *SizeTracker) Summary() string {
	latest := st.Latest()
	if latest == nil {
		return "SizeTracker: no samples"
	}
	rate := st.GrowthRate()
	rateStr := "stable"
	if rate > 0 {
		rateStr = fmt.Sprintf("+%s/s", formatBytes(int64(rate)))
	} else if rate < 0 {
		rateStr = fmt.Sprintf("-%s/s", formatBytes(int64(-rate)))
	}
	return fmt.Sprintf("SizeTracker: %s total (%s wal, %s sorted, %s snap) %d files [%s]",
		formatBytes(latest.totalSize), formatBytes(latest.walSize),
		formatBytes(latest.sortSize), formatBytes(latest.snapSize),
		latest.fileCount, rateStr)
}
