package vikingdb

import (
	"fmt"
	"log"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type BulkLoaderConfig struct {
	Workers        int
	BatchSize      int
	ProgressFn     func(done, total int)
	EfConstruction int
	M              int
}

func DefaultBulkLoaderConfig() BulkLoaderConfig {
	return BulkLoaderConfig{
		Workers:        4,
		BatchSize:      500,
		EfConstruction: 200,
		M:              16,
	}
}

type BulkLoader struct {
	cfg     BulkLoaderConfig
	h       *HNSW
	loaded  atomic.Int64
	total   int
	start   time.Time
	errors  atomic.Int64
}

func NewBulkLoader(cfg BulkLoaderConfig) *BulkLoader {
	hnswCfg := NewHNSWConfigBuilder().
		WithM(cfg.M).
		WithEfConstruction(cfg.EfConstruction).
		Build()
	return &BulkLoader{
		cfg: cfg,
		h:   NewHNSW(hnswCfg),
	}
}

func (bl *BulkLoader) Load(items []BatchItem) (*HNSW, error) {
	bl.start = time.Now()
	bl.total = len(items)
	if len(items) == 0 {
		return bl.h, nil
	}
	first := items[0]
	bl.h.Insert(first.ID, first.Vector, first.Metadata)
	bl.loaded.Add(1)
	remaining := items[1:]
	if bl.cfg.Workers <= 1 || len(remaining) < bl.cfg.BatchSize {
		for _, item := range remaining {
			bl.h.Insert(item.ID, item.Vector, item.Metadata)
			bl.loaded.Add(1)
			bl.reportProgress()
		}
		return bl.h, nil
	}
	batches := splitItems(remaining, bl.cfg.BatchSize)
	for _, batch := range batches {
		var wg sync.WaitGroup
		for _, item := range batch {
			wg.Add(1)
			go func(it BatchItem) {
				defer wg.Done()
				bl.h.Insert(it.ID, it.Vector, it.Metadata)
				bl.loaded.Add(1)
			}(item)
		}
		wg.Wait()
		bl.reportProgress()
	}
	log.Printf("[bulk-loader] loaded %d items in %v", bl.loaded.Load(), time.Since(bl.start))
	return bl.h, nil
}

func (bl *BulkLoader) reportProgress() {
	if bl.cfg.ProgressFn != nil {
		bl.cfg.ProgressFn(int(bl.loaded.Load()), bl.total)
	}
}

func (bl *BulkLoader) Progress() float64 {
	if bl.total == 0 {
		return 1.0
	}
	return float64(bl.loaded.Load()) / float64(bl.total)
}

func (bl *BulkLoader) ETA() time.Duration {
	done := bl.loaded.Load()
	if done == 0 {
		return 0
	}
	elapsed := time.Since(bl.start)
	remaining := int64(bl.total) - done
	perItem := elapsed / time.Duration(done)
	return perItem * time.Duration(remaining)
}

func splitItems(items []BatchItem, size int) [][]BatchItem {
	var batches [][]BatchItem
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		batches = append(batches, items[i:end])
	}
	return batches
}

type ProgressiveIndexer struct {
	h             *HNSW
	buffer        []BatchItem
	flushSize     int
	totalInserted int
	phases        []indexPhase
}

type indexPhase struct {
	StartCount int
	EndCount   int
	Ef         int
	M          int
	Duration   time.Duration
}

func NewProgressiveIndexer(cfg HNSWConfig, flushSize int) *ProgressiveIndexer {
	return &ProgressiveIndexer{
		h:         NewHNSW(cfg),
		flushSize: flushSize,
	}
}

func (pi *ProgressiveIndexer) Add(id string, vec Vector, meta map[string]interface{}) {
	pi.buffer = append(pi.buffer, BatchItem{ID: id, Vector: vec, Metadata: meta})
	if len(pi.buffer) >= pi.flushSize {
		pi.flush()
	}
}

func (pi *ProgressiveIndexer) flush() {
	if len(pi.buffer) == 0 {
		return
	}
	start := time.Now()
	startCount := pi.totalInserted
	phase := indexPhase{
		StartCount: startCount,
		Ef:         pi.h.cfg.EfConstruction,
		M:          pi.h.cfg.M,
	}
	for _, item := range pi.buffer {
		pi.h.Insert(item.ID, item.Vector, item.Metadata)
		pi.totalInserted++
	}
	phase.EndCount = pi.totalInserted
	phase.Duration = time.Since(start)
	pi.phases = append(pi.phases, phase)
	pi.buffer = pi.buffer[:0]
	pi.adaptParameters()
}

func (pi *ProgressiveIndexer) adaptParameters() {
	n := pi.totalInserted
	if n > 10000 {
		pi.h.cfg.EfConstruction = max(pi.h.cfg.EfConstruction, 400)
	} else if n > 1000 {
		pi.h.cfg.EfConstruction = max(pi.h.cfg.EfConstruction, 200)
	}
}

func (pi *ProgressiveIndexer) Finish() *HNSW {
	pi.flush()
	return pi.h
}

func (pi *ProgressiveIndexer) Stats() map[string]interface{} {
	totalDur := time.Duration(0)
	for _, p := range pi.phases {
		totalDur += p.Duration
	}
	return map[string]interface{}{
		"total_inserted": pi.totalInserted,
		"phases":         len(pi.phases),
		"total_duration": totalDur.String(),
		"buffered":       len(pi.buffer),
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type WarmupConfig struct {
	Queries     int
	TopK        int
	Concurrency int
}

func DefaultWarmupConfig() WarmupConfig {
	return WarmupConfig{
		Queries:     100,
		TopK:        10,
		Concurrency: 4,
	}
}

func (h *HNSW) Warmup(cfg WarmupConfig) time.Duration {
	start := time.Now()
	h.mu.RLock()
	samples := make([]Vector, 0, cfg.Queries)
	count := 0
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		samples = append(samples, node.vector)
		count++
		if count >= cfg.Queries {
			break
		}
	}
	h.mu.RUnlock()
	if len(samples) == 0 {
		return 0
	}
	var wg sync.WaitGroup
	chunkSize := (len(samples) + cfg.Concurrency - 1) / cfg.Concurrency
	for w := 0; w < cfg.Concurrency; w++ {
		s := w * chunkSize
		e := s + chunkSize
		if s >= len(samples) {
			break
		}
		if e > len(samples) {
			e = len(samples)
		}
		wg.Add(1)
		go func(queries []Vector) {
			defer wg.Done()
			for _, q := range queries {
				h.SearchByVector(q, cfg.TopK, nil)
			}
		}(samples[s:e])
	}
	wg.Wait()
	elapsed := time.Since(start)
	log.Printf("[hnsw] warmup: %d queries in %v", len(samples), elapsed)
	return elapsed
}

type IndexQuality struct {
	RecallAt1    float64
	RecallAt10   float64
	RecallAt100  float64
	AvgQueryTime time.Duration
	QPS          float64
}

func (h *HNSW) MeasureQuality(queries []Vector, groundTruth [][]string, ks []int) map[int]float64 {
	recalls := make(map[int]float64)
	for _, k := range ks {
		totalRecall := 0.0
		measured := 0
		for i, q := range queries {
			if i >= len(groundTruth) {
				break
			}
			results := h.SearchByVector(q, k, nil)
			gt := groundTruth[i]
			if len(gt) > k {
				gt = gt[:k]
			}
			gtSet := make(map[string]bool)
			for _, id := range gt {
				gtSet[id] = true
			}
			found := 0
			for _, r := range results {
				if gtSet[r.ID] {
					found++
				}
			}
			if len(gt) > 0 {
				totalRecall += float64(found) / float64(len(gt))
			}
			measured++
		}
		if measured > 0 {
			recalls[k] = totalRecall / float64(measured)
		}
	}
	return recalls
}

func (h *HNSW) BenchmarkSearch(queries []Vector, topK int) IndexQuality {
	quality := IndexQuality{}
	if len(queries) == 0 {
		return quality
	}
	start := time.Now()
	for _, q := range queries {
		h.SearchByVector(q, topK, nil)
	}
	elapsed := time.Since(start)
	quality.AvgQueryTime = elapsed / time.Duration(len(queries))
	quality.QPS = float64(len(queries)) / elapsed.Seconds()
	return quality
}

type IndexOptimizer struct {
	h *HNSW
}

func NewIndexOptimizer(h *HNSW) *IndexOptimizer {
	return &IndexOptimizer{h: h}
}

func (io *IndexOptimizer) OptimizeForRecall() {
	io.h.SetEfSearch(io.h.cfg.M * 8)
	io.h.SetAutoEfParams(200, 1000, 12)
}

func (io *IndexOptimizer) OptimizeForSpeed() {
	io.h.SetEfSearch(io.h.cfg.M * 2)
	io.h.SetAutoEfParams(50, 200, 4)
}

func (io *IndexOptimizer) OptimizeForMemory() {
	count := int(io.h.Count())
	if count > 0 {
		optimalM := RecommendM(io.h.VectorDimension())
		if optimalM < io.h.cfg.M {
			io.h.Resize(optimalM, 0)
		}
	}
}

func (io *IndexOptimizer) AutoTune(queries []Vector, groundTruth [][]string, targetRecall float64) int {
	efValues := []int{50, 100, 200, 400, 800}
	bestEf := efValues[0]
	for _, ef := range efValues {
		io.h.SetEfSearch(ef)
		recalls := io.h.MeasureQuality(queries, groundTruth, []int{10})
		if recalls[10] >= targetRecall {
			bestEf = ef
			break
		}
		bestEf = ef
	}
	io.h.SetEfSearch(bestEf)
	return bestEf
}

func GenerateRandomVectors(count, dim int, seed int64) []Vector {
	vectors := make([]Vector, count)
	state := uint64(seed)
	for i := range vectors {
		vec := make(Vector, dim)
		for j := range vec {
			state = state*6364136223846793005 + 1442695040888963407
			vec[j] = float32(state>>33) / float32(1<<31) * 2 - 1
		}
		vectors[i] = vec
	}
	return vectors
}

func ComputeGroundTruth(queries, database []Vector, ids []string, k int, distFn DistFunc) [][]string {
	groundTruth := make([][]string, len(queries))
	for i, q := range queries {
		type scored struct {
			id   string
			dist float32
		}
		var results []scored
		for j, d := range database {
			id := ""
			if j < len(ids) {
				id = ids[j]
			} else {
				id = fmt.Sprintf("%d", j)
			}
			results = append(results, scored{id, distFn(q, d)})
		}
		sort.Slice(results, func(a, b int) bool {
			return results[a].dist < results[b].dist
		})
		gt := make([]string, 0, k)
		for j := 0; j < len(results) && j < k; j++ {
			gt = append(gt, results[j].id)
		}
		groundTruth[i] = gt
	}
	return groundTruth
}

var _ = math.MaxFloat64
