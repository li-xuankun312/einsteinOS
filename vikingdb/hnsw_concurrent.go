package vikingdb

import (
	"sort"
	"sync"
	"time"
)

func (h *HNSW) ParallelSearch(queries []Vector, topK int, workers int) [][]SearchResult {
	if workers <= 1 || len(queries) <= 1 {
		results := make([][]SearchResult, len(queries))
		for i, q := range queries {
			results[i] = h.SearchByVector(q, topK, nil)
		}
		return results
	}

	results := make([][]SearchResult, len(queries))
	var wg sync.WaitGroup

	chunkSize := (len(queries) + workers - 1) / workers
	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= len(queries) {
			break
		}
		if end > len(queries) {
			end = len(queries)
		}

		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				results[i] = h.SearchByVector(queries[i], topK, nil)
			}
		}(start, end)
	}

	wg.Wait()
	return results
}

func (h *HNSW) ParallelSearchWithFilter(queries []Vector, topK int, workers int,
	filter func(map[string]interface{}) bool) [][]SearchResult {

	results := make([][]SearchResult, len(queries))
	var wg sync.WaitGroup

	if workers <= 1 {
		workers = 1
	}

	chunkSize := (len(queries) + workers - 1) / workers
	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= len(queries) {
			break
		}
		if end > len(queries) {
			end = len(queries)
		}

		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				results[i] = h.SearchByVector(queries[i], topK, filter)
			}
		}(start, end)
	}

	wg.Wait()
	return results
}

type SearchRequest struct {
	ID     int
	Query  Vector
	TopK   int
	Filter func(map[string]interface{}) bool
}

type SearchResponse struct {
	ID      int
	Results []SearchResult
	Latency time.Duration
	Error   error
}

func (h *HNSW) ProcessSearchBatch(requests []SearchRequest, workers int) []SearchResponse {
	responses := make([]SearchResponse, len(requests))

	if workers <= 1 {
		for i, req := range requests {
			start := time.Now()
			responses[i] = SearchResponse{
				ID:      req.ID,
				Results: h.SearchByVector(req.Query, req.TopK, req.Filter),
				Latency: time.Since(start),
			}
		}
		return responses
	}

	var wg sync.WaitGroup
	chunkSize := (len(requests) + workers - 1) / workers

	for w := 0; w < workers; w++ {
		s := w * chunkSize
		e := s + chunkSize
		if s >= len(requests) {
			break
		}
		if e > len(requests) {
			e = len(requests)
		}

		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				req := requests[i]
				t := time.Now()
				responses[i] = SearchResponse{
					ID:      req.ID,
					Results: h.SearchByVector(req.Query, req.TopK, req.Filter),
					Latency: time.Since(t),
				}
			}
		}(s, e)
	}

	wg.Wait()
	return responses
}

type InsertPipeline struct {
	h          *HNSW
	inputCh    chan BatchItem
	resultCh   chan insertResult
	workers    int
	batchSize  int
	flushInterval time.Duration
	wg         sync.WaitGroup
	stopCh     chan struct{}
}

type insertResult struct {
	ExtID string
	ID    uint64
	Error error
}

func NewInsertPipeline(h *HNSW, workers, batchSize int, flushInterval time.Duration) *InsertPipeline {
	if workers <= 0 {
		workers = 2
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	if flushInterval <= 0 {
		flushInterval = 100 * time.Millisecond
	}

	p := &InsertPipeline{
		h:             h,
		inputCh:       make(chan BatchItem, batchSize*workers),
		resultCh:      make(chan insertResult, batchSize*workers),
		workers:       workers,
		batchSize:     batchSize,
		flushInterval: flushInterval,
		stopCh:        make(chan struct{}),
	}

	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}

	return p
}

func (p *InsertPipeline) Submit(item BatchItem) {
	p.inputCh <- item
}

func (p *InsertPipeline) Results() <-chan insertResult {
	return p.resultCh
}

func (p *InsertPipeline) worker() {
	defer p.wg.Done()

	batch := make([]BatchItem, 0, p.batchSize)
	ticker := time.NewTicker(p.flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		for _, item := range batch {
			id := p.h.Insert(item.ID, item.Vector, item.Metadata)
			p.resultCh <- insertResult{ExtID: item.ID, ID: id}
		}
		batch = batch[:0]
	}

	for {
		select {
		case item, ok := <-p.inputCh:
			if !ok {
				flush()
				return
			}
			batch = append(batch, item)
			if len(batch) >= p.batchSize {
				flush()
			}

		case <-ticker.C:
			flush()

		case <-p.stopCh:
			flush()
			return
		}
	}
}

func (p *InsertPipeline) Stop() {
	close(p.stopCh)
	close(p.inputCh)
	p.wg.Wait()
	close(p.resultCh)
}

func MergeResults(allResults [][]SearchResult, topK int) []SearchResult {
	seen := make(map[string]SearchResult)
	for _, results := range allResults {
		for _, r := range results {
			if existing, ok := seen[r.ID]; ok {
				if r.Score > existing.Score {
					seen[r.ID] = r
				}
			} else {
				seen[r.ID] = r
			}
		}
	}

	merged := make([]SearchResult, 0, len(seen))
	for _, r := range seen {
		merged = append(merged, r)
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})

	if len(merged) > topK {
		merged = merged[:topK]
	}
	return merged
}

type LatencyHistogram struct {
	mu      sync.Mutex
	buckets []int64
	bounds  []time.Duration
	total   int64
	sumNs   int64
}

func NewLatencyHistogram() *LatencyHistogram {
	return &LatencyHistogram{
		bounds: []time.Duration{
			100 * time.Microsecond,
			500 * time.Microsecond,
			1 * time.Millisecond,
			5 * time.Millisecond,
			10 * time.Millisecond,
			50 * time.Millisecond,
			100 * time.Millisecond,
			500 * time.Millisecond,
			1 * time.Second,
		},
		buckets: make([]int64, 10),
	}
}

func (lh *LatencyHistogram) Observe(d time.Duration) {
	lh.mu.Lock()
	defer lh.mu.Unlock()

	lh.total++
	lh.sumNs += d.Nanoseconds()

	bucket := len(lh.bounds)
	for i, b := range lh.bounds {
		if d <= b {
			bucket = i
			break
		}
	}
	lh.buckets[bucket]++
}

func (lh *LatencyHistogram) P50() time.Duration {
	return lh.percentile(0.50)
}

func (lh *LatencyHistogram) P95() time.Duration {
	return lh.percentile(0.95)
}

func (lh *LatencyHistogram) P99() time.Duration {
	return lh.percentile(0.99)
}

func (lh *LatencyHistogram) Avg() time.Duration {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	if lh.total == 0 {
		return 0
	}
	return time.Duration(lh.sumNs / lh.total)
}

func (lh *LatencyHistogram) percentile(p float64) time.Duration {
	lh.mu.Lock()
	defer lh.mu.Unlock()

	if lh.total == 0 {
		return 0
	}

	target := int64(float64(lh.total) * p)
	var cumulative int64
	for i, count := range lh.buckets {
		cumulative += count
		if cumulative >= target {
			if i < len(lh.bounds) {
				return lh.bounds[i]
			}
			return lh.bounds[len(lh.bounds)-1] * 2
		}
	}
	return lh.bounds[len(lh.bounds)-1] * 2
}

func (lh *LatencyHistogram) Count() int64 {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	return lh.total
}

func (lh *LatencyHistogram) Reset() {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	lh.total = 0
	lh.sumNs = 0
	for i := range lh.buckets {
		lh.buckets[i] = 0
	}
}
