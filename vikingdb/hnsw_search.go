package vikingdb

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type FilterStrategy int

const (
	StrategySweeping FilterStrategy = iota
	StrategyPreFilter
	StrategyPostFilter
	StrategyAdaptive
)

type SearchParams struct {
	TopK           int
	Ef             int
	Filter         func(map[string]interface{}) bool
	MaxDist        float32
	Strategy       FilterStrategy
	Timeout        time.Duration
	AllowTombstone bool
}

func DefaultSearchParams(topK int) SearchParams {
	return SearchParams{
		TopK:     topK,
		Ef:       0,
		Strategy: StrategyAdaptive,
	}
}

func (h *HNSW) SearchWithParams(query Vector, params SearchParams) []SearchResult {
	start := time.Now()
	defer func() {
		if h.metrics != nil {
			h.metrics.TrackSearch(start)
		}
	}()

	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.nodeCount == 0 || h.maxLevel == -1 {
		return nil
	}

	ef := params.Ef
	if ef <= 0 {
		ef = h.autoEf(params.TopK)
	}
	if ef < params.TopK {
		ef = params.TopK
	}

	activeCount := int(h.nodeCount) - h.tombstones.Len()

	if params.Filter != nil && activeCount < h.flatCutoff {
		return h.flatSearchFiltered(query, params)
	}

	switch params.Strategy {
	case StrategyPreFilter:
		return h.preFilterSearch(query, params, ef)
	case StrategyPostFilter:
		return h.postFilterSearch(query, params, ef)
	case StrategySweeping:
		return h.sweepingSearch(query, params, ef)
	case StrategyAdaptive:
		return h.adaptiveSearch(query, params, ef, activeCount)
	default:
		return h.postFilterSearch(query, params, ef)
	}
}

func (h *HNSW) autoEf(k int) int {
	efMin := int(atomic.LoadInt64(&h.autoEfMin))
	efMax := int(atomic.LoadInt64(&h.autoEfMax))
	efFactor := int(atomic.LoadInt64(&h.autoEfFactor))

	if efMin == 0 {
		efMin = 100
	}
	if efMax == 0 {
		efMax = 500
	}
	if efFactor == 0 {
		efFactor = 8
	}

	ef := k * efFactor
	if ef < efMin {
		ef = efMin
	}
	if ef > efMax {
		ef = efMax
	}
	return ef
}

func (h *HNSW) SetAutoEfParams(efMin, efMax, efFactor int) {
	atomic.StoreInt64(&h.autoEfMin, int64(efMin))
	atomic.StoreInt64(&h.autoEfMax, int64(efMax))
	atomic.StoreInt64(&h.autoEfFactor, int64(efFactor))
}

func (h *HNSW) flatSearchFiltered(query Vector, params SearchParams) []SearchResult {
	if h.metrics != nil {
		h.metrics.TrackFlatSearch()
	}

	results := newMaxPQ(params.TopK)
	for id, node := range h.nodes {
		if h.tombstones.Has(id) && !params.AllowTombstone {
			continue
		}
		if params.Filter != nil && !params.Filter(h.meta[id]) {
			continue
		}
		dist := h.cfg.Dist(query, node.vector)
		if params.MaxDist > 0 && dist > params.MaxDist {
			continue
		}
		if results.Len() < params.TopK || dist < results.Top().dist {
			results.Push(id, dist)
			if results.Len() > params.TopK {
				results.Pop()
			}
		}
	}

	return h.pqToResults(results)
}

func (h *HNSW) postFilterSearch(query Vector, params SearchParams, ef int) []SearchResult {
	ep := h.findBestEntryPoint(query)
	overFetchFactor := 3
	if params.Filter != nil {
		overFetchFactor = 5
	}

	candidateIDs := h.searchLayer(query, ep, ef*overFetchFactor, 0)

	var results []SearchResult
	for _, id := range candidateIDs {
		if h.tombstones.Has(id) && !params.AllowTombstone {
			continue
		}
		if params.Filter != nil && !params.Filter(h.meta[id]) {
			continue
		}
		node := h.nodes[id]
		if node == nil {
			continue
		}
		dist := h.cfg.Dist(query, node.vector)
		if params.MaxDist > 0 && dist > params.MaxDist {
			continue
		}
		score := 1.0 / (1.0 + dist)
		results = append(results, SearchResult{
			ID:    h.reverseMap[id],
			Score: score,
			Data:  h.meta[id],
		})
		if len(results) >= params.TopK {
			break
		}
	}
	return results
}

func (h *HNSW) preFilterSearch(query Vector, params SearchParams, ef int) []SearchResult {
	var allowedIDs []uint64
	for id := range h.nodes {
		if h.tombstones.Has(id) && !params.AllowTombstone {
			continue
		}
		if params.Filter != nil && !params.Filter(h.meta[id]) {
			continue
		}
		allowedIDs = append(allowedIDs, id)
	}

	if len(allowedIDs) == 0 {
		return nil
	}

	if len(allowedIDs) < h.flatCutoff {
		return h.flatSearchAmong(query, allowedIDs, params)
	}

	allowed := make(map[uint64]bool, len(allowedIDs))
	for _, id := range allowedIDs {
		allowed[id] = true
	}

	ep := h.findBestEntryPoint(query)
	candidateIDs := h.searchLayerFiltered(query, ep, ef, 0, allowed)

	var results []SearchResult
	for _, id := range candidateIDs {
		node := h.nodes[id]
		if node == nil {
			continue
		}
		dist := h.cfg.Dist(query, node.vector)
		if params.MaxDist > 0 && dist > params.MaxDist {
			continue
		}
		score := 1.0 / (1.0 + dist)
		results = append(results, SearchResult{
			ID:    h.reverseMap[id],
			Score: score,
			Data:  h.meta[id],
		})
		if len(results) >= params.TopK {
			break
		}
	}
	return results
}

func (h *HNSW) sweepingSearch(query Vector, params SearchParams, ef int) []SearchResult {
	multipleEf := []int{ef, ef * 2, ef * 4}
	var allResults []SearchResult

	ep := h.findBestEntryPoint(query)

	for _, currentEf := range multipleEf {
		candidateIDs := h.searchLayer(query, ep, currentEf, 0)

		for _, id := range candidateIDs {
			if h.tombstones.Has(id) && !params.AllowTombstone {
				continue
			}
			if params.Filter != nil && !params.Filter(h.meta[id]) {
				continue
			}
			node := h.nodes[id]
			if node == nil {
				continue
			}
			dist := h.cfg.Dist(query, node.vector)
			if params.MaxDist > 0 && dist > params.MaxDist {
				continue
			}
			score := 1.0 / (1.0 + dist)
			allResults = append(allResults, SearchResult{
				ID:    h.reverseMap[id],
				Score: score,
				Data:  h.meta[id],
			})
		}

		allResults = deduplicateResults(allResults)
		if len(allResults) >= params.TopK {
			break
		}
	}

	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].Score > allResults[j].Score
	})
	if len(allResults) > params.TopK {
		allResults = allResults[:params.TopK]
	}
	return allResults
}

func (h *HNSW) adaptiveSearch(query Vector, params SearchParams, ef, activeCount int) []SearchResult {
	filterSelectivity := float64(1.0)
	if params.Filter != nil {
		sampleSize := 100
		if activeCount < sampleSize {
			sampleSize = activeCount
		}
		if sampleSize > 0 {
			passing := 0
			checked := 0
			for id := range h.nodes {
				if checked >= sampleSize {
					break
				}
				if h.tombstones.Has(id) {
					continue
				}
				checked++
				if params.Filter(h.meta[id]) {
					passing++
				}
			}
			if checked > 0 {
				filterSelectivity = float64(passing) / float64(checked)
			}
		}
	}

	if filterSelectivity < 0.01 {
		return h.flatSearchFiltered(query, params)
	}
	if filterSelectivity < 0.1 {
		return h.preFilterSearch(query, params, ef)
	}
	if filterSelectivity < 0.5 {
		return h.sweepingSearch(query, params, ef)
	}
	return h.postFilterSearch(query, params, ef)
}

func (h *HNSW) findBestEntryPoint(query Vector) uint64 {
	ep := h.entryPoint
	if h.tombstones.Has(ep) || h.nodes[ep] == nil {
		for id, node := range h.nodes {
			if !h.tombstones.Has(id) && node.level >= h.maxLevel {
				ep = id
				break
			}
		}
	}

	for lc := h.maxLevel; lc > 0; lc-- {
		changed := true
		for changed {
			changed = false
			node := h.nodes[ep]
			if node == nil {
				break
			}
			for _, n := range node.neighborsAtLevel(lc) {
				if h.tombstones.Has(n) {
					continue
				}
				nNode := h.nodes[n]
				if nNode == nil {
					continue
				}
				if h.cfg.Dist(query, nNode.vector) < h.cfg.Dist(query, h.nodes[ep].vector) {
					ep = n
					changed = true
				}
			}
		}
	}
	return ep
}

func (h *HNSW) searchLayerFiltered(query Vector, ep uint64, ef int, level int, allowed map[uint64]bool) []uint64 {
	visited := make(map[uint64]bool)
	visited[ep] = true

	epDist := h.cfg.Dist(query, h.nodes[ep].vector)
	candidates := newMinPQ(ef)
	candidates.Push(ep, epDist)

	results := newMaxPQ(ef)
	if allowed[ep] {
		results.Push(ep, epDist)
	}

	for candidates.Len() > 0 {
		c := candidates.Pop()
		if results.Len() >= ef && c.dist > results.Top().dist {
			break
		}

		cNode := h.nodes[c.id]
		if cNode == nil {
			continue
		}
		for _, n := range cNode.neighborsAtLevel(level) {
			if visited[n] {
				continue
			}
			visited[n] = true

			nNode := h.nodes[n]
			if nNode == nil {
				continue
			}
			d := h.cfg.Dist(query, nNode.vector)

			candidates.Push(n, d)
			if allowed[n] {
				if results.Len() < ef || d < results.Top().dist {
					results.Push(n, d)
					if results.Len() > ef {
						results.Pop()
					}
				}
			}
		}
	}

	out := make([]uint64, 0, results.Len())
	for results.Len() > 0 {
		out = append(out, results.Pop().id)
	}
	reverseUint64(out)
	return out
}

func (h *HNSW) flatSearchAmong(query Vector, ids []uint64, params SearchParams) []SearchResult {
	type scoredResult struct {
		id    uint64
		dist  float32
		score float32
	}

	results := make([]scoredResult, 0, len(ids))
	for _, id := range ids {
		node := h.nodes[id]
		if node == nil {
			continue
		}
		dist := h.cfg.Dist(query, node.vector)
		if params.MaxDist > 0 && dist > params.MaxDist {
			continue
		}
		results = append(results, scoredResult{id, dist, 1.0 / (1.0 + dist)})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].dist < results[j].dist
	})

	out := make([]SearchResult, 0, params.TopK)
	for i := 0; i < len(results) && i < params.TopK; i++ {
		out = append(out, SearchResult{
			ID:    h.reverseMap[results[i].id],
			Score: results[i].score,
			Data:  h.meta[results[i].id],
		})
	}
	return out
}

func (h *HNSW) pqToResults(pq *maxPQ) []SearchResult {
	out := make([]SearchResult, 0, pq.Len())
	for pq.Len() > 0 {
		item := pq.Pop()
		score := 1.0 / (1.0 + item.dist)
		out = append(out, SearchResult{
			ID:    h.reverseMap[item.id],
			Score: float32(score),
			Data:  h.meta[item.id],
		})
	}
	reverseResults(out)
	return out
}

func deduplicateResults(results []SearchResult) []SearchResult {
	seen := make(map[string]bool)
	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		if !seen[r.ID] {
			seen[r.ID] = true
			out = append(out, r)
		}
	}
	return out
}

type MultiProbeConfig struct {
	NumProbes    int
	PerturbDist  float32
}

func (h *HNSW) MultiProbeSearch(query Vector, params SearchParams, mpCfg MultiProbeConfig) []SearchResult {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.nodeCount == 0 || h.maxLevel == -1 {
		return nil
	}

	if mpCfg.NumProbes <= 1 {
		return h.SearchWithParams(query, params)
	}

	var allResults []SearchResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	probeQueries := make([]Vector, mpCfg.NumProbes)
	probeQueries[0] = query
	for i := 1; i < mpCfg.NumProbes; i++ {
		perturbed := make(Vector, len(query))
		copy(perturbed, query)
		for j := range perturbed {
			offset := (h.rng.Float32()*2 - 1) * mpCfg.PerturbDist
			perturbed[j] += float32(offset)
		}
		probeQueries[i] = perturbed
	}

	for _, pq := range probeQueries {
		wg.Add(1)
		go func(q Vector) {
			defer wg.Done()
			results := h.postFilterSearch(q, params, h.cfg.EfSearch)
			mu.Lock()
			allResults = append(allResults, results...)
			mu.Unlock()
		}(pq)
	}

	wg.Wait()

	allResults = deduplicateResults(allResults)
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].Score > allResults[j].Score
	})
	if len(allResults) > params.TopK {
		allResults = allResults[:params.TopK]
	}
	return allResults
}

type SearchByDistParams struct {
	maxLimit    int64
	currentEf   int
	step        int
	iterCount   int
}

func NewSearchByDistParams(maxLimit int64) *SearchByDistParams {
	return &SearchByDistParams{
		maxLimit:  maxLimit,
		currentEf: 100,
		step:      0,
	}
}

func (p *SearchByDistParams) Iterate() {
	p.step++
	p.currentEf *= 2
	p.iterCount++
}

func (p *SearchByDistParams) MaxLimitReached(found int) bool {
	return int64(found) >= p.maxLimit || p.iterCount > 10
}

func (h *HNSW) SearchByVectorDistance(query Vector, maxDist float32, limit int) []SearchResult {
	params := DefaultSearchParams(limit)
	params.MaxDist = maxDist

	sdp := NewSearchByDistParams(int64(limit))
	var lastResults []SearchResult

	for !sdp.MaxLimitReached(len(lastResults)) {
		params.Ef = sdp.currentEf
		lastResults = h.SearchWithParams(query, params)

		allWithinDist := true
		for _, r := range lastResults {
			dist := 1.0/float64(r.Score) - 1.0
			if float32(dist) > maxDist {
				allWithinDist = false
				break
			}
		}
		if allWithinDist && len(lastResults) < limit {
			break
		}

		sdp.Iterate()
	}

	var filtered []SearchResult
	for _, r := range lastResults {
		dist := float32(1.0/float64(r.Score) - 1.0)
		if dist <= maxDist {
			filtered = append(filtered, r)
		}
	}

	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered
}

func (h *HNSW) KNNRecall(query Vector, k int, exactResults []string) float64 {
	approxResults := h.SearchByVector(query, k, nil)
	if len(exactResults) == 0 {
		return 1.0
	}

	exactSet := make(map[string]bool)
	for _, id := range exactResults {
		exactSet[id] = true
	}

	found := 0
	for _, r := range approxResults {
		if exactSet[r.ID] {
			found++
		}
	}

	return float64(found) / float64(len(exactResults))
}

func (h *HNSW) ExactKNN(query Vector, k int) []SearchResult {
	h.mu.RLock()
	defer h.mu.RUnlock()

	type scored struct {
		id   uint64
		dist float32
	}
	all := make([]scored, 0, len(h.nodes))
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		d := h.cfg.Dist(query, node.vector)
		all = append(all, scored{id, d})
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].dist < all[j].dist
	})

	out := make([]SearchResult, 0, k)
	for i := 0; i < len(all) && i < k; i++ {
		out = append(out, SearchResult{
			ID:    h.reverseMap[all[i].id],
			Score: 1.0 / (1.0 + all[i].dist),
			Data:  h.meta[all[i].id],
		})
	}
	return out
}

var _ = math.MaxFloat32
