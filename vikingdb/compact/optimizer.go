package compact

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type OptimizerConfig struct {
	Dir             string
	SampleWindow    int
	MinSamples      int
	TargetWriteAmp  float64
	TargetSpaceAmp  float64
	TargetLatencyMs int64
	AdaptInterval   time.Duration
}

func DefaultOptimizerConfig(dir string) OptimizerConfig {
	return OptimizerConfig{
		Dir:             dir,
		SampleWindow:    100,
		MinSamples:      10,
		TargetWriteAmp:  3.0,
		TargetSpaceAmp:  2.0,
		TargetLatencyMs: 500,
		AdaptInterval:   5 * time.Minute,
	}
}

type Optimizer struct {
	cfg           OptimizerConfig
	mu            sync.Mutex
	history       []CompactionSample
	currentParams OptimizedParams
	adaptCount    int
	lastAdapt     time.Time
}

type CompactionSample struct {
	Timestamp    time.Time
	Action       Action
	Duration     time.Duration
	BytesRead    int64
	BytesWritten int64
	InputFiles   int
	OutputFiles  int
	InputSize    int64
	OutputSize   int64
	WriteAmp     float64
	SpaceAmp     float64
}

type OptimizedParams struct {
	MaxWALSize       int64
	MaxSortedFiles   int
	MergeWidth       int
	SnapshotThreshold int64
	CompactInterval  time.Duration
}

func DefaultOptimizedParams() OptimizedParams {
	return OptimizedParams{
		MaxWALSize:       32 * 1024 * 1024,
		MaxSortedFiles:   8,
		MergeWidth:       8,
		SnapshotThreshold: 128 * 1024 * 1024,
		CompactInterval:  30 * time.Second,
	}
}

func NewOptimizer(cfg OptimizerConfig) *Optimizer {
	return &Optimizer{
		cfg:           cfg,
		currentParams: DefaultOptimizedParams(),
	}
}

func (o *Optimizer) RecordSample(s CompactionSample) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s.InputSize > 0 {
		s.WriteAmp = float64(s.BytesWritten) / float64(s.InputSize)
	}
	if s.OutputSize > 0 && s.InputSize > 0 {
		s.SpaceAmp = float64(s.InputSize+s.OutputSize) / float64(s.OutputSize)
	}
	o.history = append(o.history, s)
	if len(o.history) > o.cfg.SampleWindow {
		o.history = o.history[len(o.history)-o.cfg.SampleWindow:]
	}
}

func (o *Optimizer) ShouldAdapt() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.history) < o.cfg.MinSamples {
		return false
	}
	return time.Since(o.lastAdapt) > o.cfg.AdaptInterval
}

func (o *Optimizer) Adapt() OptimizedParams {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.history) < o.cfg.MinSamples {
		return o.currentParams
	}
	params := o.currentParams
	avgWriteAmp := o.avgWriteAmp()
	avgSpaceAmp := o.avgSpaceAmp()
	avgLatency := o.avgLatency()
	if avgWriteAmp > o.cfg.TargetWriteAmp {
		params.MaxSortedFiles = maxInt(params.MaxSortedFiles-1, 3)
		params.MergeWidth = maxInt(params.MergeWidth+2, 4)
	} else if avgWriteAmp < o.cfg.TargetWriteAmp*0.5 {
		params.MaxSortedFiles = minInt(params.MaxSortedFiles+1, 20)
		params.MergeWidth = maxInt(params.MergeWidth-1, 2)
	}
	if avgSpaceAmp > o.cfg.TargetSpaceAmp {
		params.SnapshotThreshold = int64(float64(params.SnapshotThreshold) * 0.8)
		if params.SnapshotThreshold < 16*1024*1024 {
			params.SnapshotThreshold = 16 * 1024 * 1024
		}
	} else if avgSpaceAmp < o.cfg.TargetSpaceAmp*0.5 {
		params.SnapshotThreshold = int64(float64(params.SnapshotThreshold) * 1.2)
	}
	if avgLatency > float64(o.cfg.TargetLatencyMs) {
		params.MaxWALSize = int64(float64(params.MaxWALSize) * 0.8)
		if params.MaxWALSize < 4*1024*1024 {
			params.MaxWALSize = 4 * 1024 * 1024
		}
		params.CompactInterval = time.Duration(float64(params.CompactInterval) * 0.8)
		if params.CompactInterval < 5*time.Second {
			params.CompactInterval = 5 * time.Second
		}
	} else if avgLatency < float64(o.cfg.TargetLatencyMs)*0.3 {
		params.MaxWALSize = int64(float64(params.MaxWALSize) * 1.2)
		params.CompactInterval = time.Duration(float64(params.CompactInterval) * 1.2)
	}
	o.currentParams = params
	o.adaptCount++
	o.lastAdapt = time.Now()
	return params
}

func (o *Optimizer) avgWriteAmp() float64 {
	if len(o.history) == 0 {
		return 1.0
	}
	var sum float64
	count := 0
	for _, s := range o.history {
		if s.WriteAmp > 0 {
			sum += s.WriteAmp
			count++
		}
	}
	if count == 0 {
		return 1.0
	}
	return sum / float64(count)
}

func (o *Optimizer) avgSpaceAmp() float64 {
	if len(o.history) == 0 {
		return 1.0
	}
	var sum float64
	count := 0
	for _, s := range o.history {
		if s.SpaceAmp > 0 {
			sum += s.SpaceAmp
			count++
		}
	}
	if count == 0 {
		return 1.0
	}
	return sum / float64(count)
}

func (o *Optimizer) avgLatency() float64 {
	if len(o.history) == 0 {
		return 0
	}
	var sum float64
	for _, s := range o.history {
		sum += float64(s.Duration.Milliseconds())
	}
	return sum / float64(len(o.history))
}

func (o *Optimizer) CurrentParams() OptimizedParams {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.currentParams
}

func (o *Optimizer) HistoryLen() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.history)
}

func (o *Optimizer) P50Latency() time.Duration {
	return o.percentileLatency(0.50)
}

func (o *Optimizer) P99Latency() time.Duration {
	return o.percentileLatency(0.99)
}

func (o *Optimizer) percentileLatency(p float64) time.Duration {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.history) == 0 {
		return 0
	}
	durations := make([]time.Duration, len(o.history))
	for i, s := range o.history {
		durations[i] = s.Duration
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	idx := int(float64(len(durations)-1) * p)
	return durations[idx]
}

func (o *Optimizer) ActionDistribution() map[Action]int {
	o.mu.Lock()
	defer o.mu.Unlock()
	dist := make(map[Action]int)
	for _, s := range o.history {
		dist[s.Action]++
	}
	return dist
}

func (o *Optimizer) TotalBytesProcessed() (read, written int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, s := range o.history {
		read += s.BytesRead
		written += s.BytesWritten
	}
	return
}

func (o *Optimizer) Trend() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.history) < 4 {
		return "insufficient data"
	}
	half := len(o.history) / 2
	firstHalf := o.history[:half]
	secondHalf := o.history[half:]
	avgFirst := avgDuration(firstHalf)
	avgSecond := avgDuration(secondHalf)
	if avgSecond > avgFirst*1.2 {
		return "degrading"
	} else if avgSecond < avgFirst*0.8 {
		return "improving"
	}
	return "stable"
}

func avgDuration(samples []CompactionSample) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += float64(s.Duration.Milliseconds())
	}
	return sum / float64(len(samples))
}

func (o *Optimizer) Summary() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Optimizer (adapted %d times, %d samples):\n", o.adaptCount, len(o.history)))
	sb.WriteString(fmt.Sprintf("  Avg write amp: %.2f (target %.2f)\n", o.avgWriteAmp(), o.cfg.TargetWriteAmp))
	sb.WriteString(fmt.Sprintf("  Avg space amp: %.2f (target %.2f)\n", o.avgSpaceAmp(), o.cfg.TargetSpaceAmp))
	sb.WriteString(fmt.Sprintf("  Avg latency:   %.1fms (target %dms)\n", o.avgLatency(), o.cfg.TargetLatencyMs))
	sb.WriteString(fmt.Sprintf("  Trend: %s\n", o.Trend()))
	p := o.currentParams
	sb.WriteString(fmt.Sprintf("  Params: maxWAL=%s maxSorted=%d mergeWidth=%d snapThresh=%s interval=%v\n",
		formatBytes(p.MaxWALSize), p.MaxSortedFiles, p.MergeWidth,
		formatBytes(p.SnapshotThreshold), p.CompactInterval))
	return sb.String()
}

func (o *Optimizer) ApplyTo(cfg *CompactorConfig) {
	p := o.CurrentParams()
	cfg.MaxWALSize = p.MaxWALSize
	cfg.MaxSortedFiles = p.MaxSortedFiles
	cfg.SnapshotThreshold = p.SnapshotThreshold
}

func (o *Optimizer) ApplyToScheduler(cfg *SchedulerConfig) {
	p := o.CurrentParams()
	cfg.CheckInterval = p.CompactInterval
	cfg.CompactorCfg.MaxWALSize = p.MaxWALSize
	cfg.CompactorCfg.MaxSortedFiles = p.MaxSortedFiles
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = math.Inf
