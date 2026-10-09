package vikingdb

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type OperationMetrics struct {
	count    atomic.Int64
	totalNs  atomic.Int64
	maxNs    atomic.Int64
	minNs    atomic.Int64
	errors   atomic.Int64
}

func (om *OperationMetrics) Record(d time.Duration, err error) {
	om.count.Add(1)
	ns := d.Nanoseconds()
	om.totalNs.Add(ns)
	if err != nil {
		om.errors.Add(1)
	}
	for {
		cur := om.maxNs.Load()
		if ns <= cur || om.maxNs.CompareAndSwap(cur, ns) {
			break
		}
	}
	for {
		cur := om.minNs.Load()
		if cur != 0 && ns >= cur {
			break
		}
		if om.minNs.CompareAndSwap(cur, ns) {
			break
		}
	}
}

func (om *OperationMetrics) Count() int64   { return om.count.Load() }
func (om *OperationMetrics) Errors() int64  { return om.errors.Load() }

func (om *OperationMetrics) AvgDuration() time.Duration {
	c := om.count.Load()
	if c == 0 {
		return 0
	}
	return time.Duration(om.totalNs.Load() / c)
}

func (om *OperationMetrics) MaxDuration() time.Duration {
	return time.Duration(om.maxNs.Load())
}

func (om *OperationMetrics) MinDuration() time.Duration {
	return time.Duration(om.minNs.Load())
}

func (om *OperationMetrics) TotalDuration() time.Duration {
	return time.Duration(om.totalNs.Load())
}

func (om *OperationMetrics) Reset() {
	om.count.Store(0)
	om.totalNs.Store(0)
	om.maxNs.Store(0)
	om.minNs.Store(0)
	om.errors.Store(0)
}

func (om *OperationMetrics) Snapshot() map[string]interface{} {
	return map[string]interface{}{
		"count":    om.Count(),
		"errors":   om.Errors(),
		"avg_us":   om.AvgDuration().Microseconds(),
		"max_us":   om.MaxDuration().Microseconds(),
		"min_us":   om.MinDuration().Microseconds(),
		"total_ms": om.TotalDuration().Milliseconds(),
	}
}

type DetailedMetrics struct {
	Insert      OperationMetrics
	Search      OperationMetrics
	Delete      OperationMetrics
	BatchInsert OperationMetrics
	BatchSearch OperationMetrics
	Tombstone   OperationMetrics
	Cleanup     OperationMetrics
	Snapshot    OperationMetrics
	Restore     OperationMetrics
	Compact     OperationMetrics
	distComps   atomic.Int64
	nodeAccess  atomic.Int64
	cacheHits   atomic.Int64
	cacheMisses atomic.Int64
	startedAt   time.Time
}

func NewDetailedMetrics() *DetailedMetrics {
	return &DetailedMetrics{
		startedAt: time.Now(),
	}
}

func (dm *DetailedMetrics) TrackDistComputations(n int64) { dm.distComps.Add(n) }
func (dm *DetailedMetrics) TrackNodeAccess()               { dm.nodeAccess.Add(1) }
func (dm *DetailedMetrics) TrackCacheHit()                 { dm.cacheHits.Add(1) }
func (dm *DetailedMetrics) TrackCacheMiss()                { dm.cacheMisses.Add(1) }

func (dm *DetailedMetrics) CacheHitRate() float64 {
	hits := dm.cacheHits.Load()
	misses := dm.cacheMisses.Load()
	total := hits + misses
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

func (dm *DetailedMetrics) Uptime() time.Duration {
	return time.Since(dm.startedAt)
}

func (dm *DetailedMetrics) QPS() float64 {
	elapsed := dm.Uptime().Seconds()
	if elapsed == 0 {
		return 0
	}
	total := dm.Insert.Count() + dm.Search.Count() + dm.Delete.Count()
	return float64(total) / elapsed
}

func (dm *DetailedMetrics) FullSnapshot() map[string]interface{} {
	return map[string]interface{}{
		"insert":        dm.Insert.Snapshot(),
		"search":        dm.Search.Snapshot(),
		"delete":        dm.Delete.Snapshot(),
		"batch_insert":  dm.BatchInsert.Snapshot(),
		"batch_search":  dm.BatchSearch.Snapshot(),
		"tombstone":     dm.Tombstone.Snapshot(),
		"cleanup":       dm.Cleanup.Snapshot(),
		"dist_comps":    dm.distComps.Load(),
		"node_access":   dm.nodeAccess.Load(),
		"cache_hit_rate": fmt.Sprintf("%.2f", dm.CacheHitRate()),
		"qps":           fmt.Sprintf("%.1f", dm.QPS()),
		"uptime":        dm.Uptime().String(),
	}
}

func (dm *DetailedMetrics) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("HNSW Metrics (uptime %v, QPS %.1f):\n", dm.Uptime().Round(time.Second), dm.QPS()))
	sb.WriteString(fmt.Sprintf("  Insert:  %d ops, avg %v, max %v\n",
		dm.Insert.Count(), dm.Insert.AvgDuration(), dm.Insert.MaxDuration()))
	sb.WriteString(fmt.Sprintf("  Search:  %d ops, avg %v, max %v\n",
		dm.Search.Count(), dm.Search.AvgDuration(), dm.Search.MaxDuration()))
	sb.WriteString(fmt.Sprintf("  Delete:  %d ops, avg %v\n", dm.Delete.Count(), dm.Delete.AvgDuration()))
	sb.WriteString(fmt.Sprintf("  Dist:    %d computations\n", dm.distComps.Load()))
	sb.WriteString(fmt.Sprintf("  Cache:   %.0f%% hit rate\n", dm.CacheHitRate()*100))
	return sb.String()
}

func (dm *DetailedMetrics) Reset() {
	dm.Insert.Reset()
	dm.Search.Reset()
	dm.Delete.Reset()
	dm.BatchInsert.Reset()
	dm.BatchSearch.Reset()
	dm.Tombstone.Reset()
	dm.Cleanup.Reset()
	dm.Snapshot.Reset()
	dm.Restore.Reset()
	dm.Compact.Reset()
	dm.distComps.Store(0)
	dm.nodeAccess.Store(0)
	dm.cacheHits.Store(0)
	dm.cacheMisses.Store(0)
	dm.startedAt = time.Now()
}

type LevelMetrics struct {
	mu     sync.RWMutex
	levels map[int]*LevelStats
}

type LevelStats struct {
	Nodes       int
	Edges       int
	AvgDegree   float64
	MaxDegree   int
	MinDegree   int
	SearchHits  atomic.Int64
	InsertOps   atomic.Int64
}

func NewLevelMetrics() *LevelMetrics {
	return &LevelMetrics{levels: make(map[int]*LevelStats)}
}

func (lm *LevelMetrics) Update(h *HNSW) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.levels = make(map[int]*LevelStats)
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		for level := 0; level <= node.level; level++ {
			ls, ok := lm.levels[level]
			if !ok {
				ls = &LevelStats{MinDegree: int(^uint(0) >> 1)}
				lm.levels[level] = ls
			}
			ls.Nodes++
			degree := len(node.neighborsAtLevel(level))
			ls.Edges += degree
			if degree > ls.MaxDegree {
				ls.MaxDegree = degree
			}
			if degree < ls.MinDegree {
				ls.MinDegree = degree
			}
		}
	}
	for _, ls := range lm.levels {
		if ls.Nodes > 0 {
			ls.AvgDegree = float64(ls.Edges) / float64(ls.Nodes)
		}
		if ls.Nodes == 0 {
			ls.MinDegree = 0
		}
	}
}

func (lm *LevelMetrics) Summary() string {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	var sb strings.Builder
	sb.WriteString("Level Metrics:\n")
	for level := 0; level < len(lm.levels); level++ {
		ls, ok := lm.levels[level]
		if !ok {
			continue
		}
		sb.WriteString(fmt.Sprintf("  L%d: %d nodes, %d edges, avg=%.1f max=%d min=%d\n",
			level, ls.Nodes, ls.Edges, ls.AvgDegree, ls.MaxDegree, ls.MinDegree))
	}
	return sb.String()
}

type MetricCollector struct {
	mu        sync.Mutex
	snapshots []metricsSnapshot
	maxSnaps  int
	interval  time.Duration
}

type metricsSnapshot struct {
	ts      time.Time
	metrics map[string]interface{}
}

func NewMetricCollector(maxSnapshots int, interval time.Duration) *MetricCollector {
	return &MetricCollector{
		maxSnaps: maxSnapshots,
		interval: interval,
	}
}

func (mc *MetricCollector) Collect(metrics map[string]interface{}) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.snapshots = append(mc.snapshots, metricsSnapshot{
		ts:      time.Now(),
		metrics: metrics,
	})
	if len(mc.snapshots) > mc.maxSnaps {
		mc.snapshots = mc.snapshots[len(mc.snapshots)-mc.maxSnaps:]
	}
}

func (mc *MetricCollector) Latest() map[string]interface{} {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if len(mc.snapshots) == 0 {
		return nil
	}
	return mc.snapshots[len(mc.snapshots)-1].metrics
}

func (mc *MetricCollector) History() []metricsSnapshot {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	out := make([]metricsSnapshot, len(mc.snapshots))
	copy(out, mc.snapshots)
	return out
}

func (mc *MetricCollector) Count() int {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return len(mc.snapshots)
}
