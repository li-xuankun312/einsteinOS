package vikingdb

import (
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
)

type DistFunc func(a, b Vector) float32

type HNSWConfig struct {
	M              int
	MMax           int
	MMax0          int
	EfConstruction int
	EfSearch       int
	ML             float64
	Dist           DistFunc
}

func DefaultHNSWConfig() HNSWConfig {
	return HNSWConfig{
		M:              16,
		MMax:           16,
		MMax0:          32,
		EfConstruction: 200,
		EfSearch:       50,
		Dist:           L2Distance,
		ML:             1.0 / math.Log(float64(16)),
	}
}

type hnswNode struct {
	sync.Mutex
	id          uint64
	level       int
	connections [][]uint64
	vector      Vector
}

func (n *hnswNode) neighborsAtLevel(level int) []uint64 {
	if level >= len(n.connections) {
		return nil
	}
	return n.connections[level]
}

func (n *hnswNode) setNeighborsAtLevel(level int, neighbors []uint64) {
	for level >= len(n.connections) {
		n.connections = append(n.connections, nil)
	}
	n.connections[level] = neighbors
}

type HNSW struct {
	mu          sync.RWMutex
	cfg         HNSWConfig
	nodes       map[uint64]*hnswNode
	entryPoint  uint64
	maxLevel    int
	nodeCount   uint64
	nextID      atomic.Uint64
	idMap       map[string]uint64
	reverseMap  map[uint64]string
	meta        map[uint64]map[string]interface{}
	rng         *rand.Rand
	tombstones  *tombstoneSet
	metrics     *HNSWMetrics
	flatCutoff  int
}

func NewHNSW(cfg HNSWConfig) *HNSW {
	if cfg.M == 0 {
		cfg = DefaultHNSWConfig()
	}
	if cfg.MMax == 0 {
		cfg.MMax = cfg.M
	}
	if cfg.MMax0 == 0 {
		cfg.MMax0 = cfg.M * 2
	}
	if cfg.ML == 0 {
		cfg.ML = 1.0 / math.Log(float64(cfg.M))
	}
	if cfg.Dist == nil {
		cfg.Dist = L2Distance
	}
	return &HNSW{
		cfg:        cfg,
		nodes:      make(map[uint64]*hnswNode),
		idMap:      make(map[string]uint64),
		reverseMap: make(map[uint64]string),
		meta:       make(map[uint64]map[string]interface{}),
		maxLevel:   -1,
		rng:        rand.New(rand.NewSource(42)),
		tombstones: newTombstoneSet(),
		metrics:    NewHNSWMetrics(),
		flatCutoff: 100,
	}
}

func (h *HNSW) randomLevel() int {
	r := h.rng.Float64()
	level := int(-math.Log(r) * h.cfg.ML)
	return level
}

func (h *HNSW) maxConnections(level int) int {
	if level == 0 {
		return h.cfg.MMax0
	}
	return h.cfg.MMax
}

func (h *HNSW) Insert(extID string, vec Vector, metadata map[string]interface{}) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	if existing, ok := h.idMap[extID]; ok {
		node := h.nodes[existing]
		node.vector = vec
		h.meta[existing] = metadata
		return existing
	}

	id := h.nextID.Add(1) - 1
	level := h.randomLevel()

	node := &hnswNode{
		id:          id,
		level:       level,
		connections: make([][]uint64, level+1),
		vector:      vec,
	}
	for i := range node.connections {
		node.connections[i] = make([]uint64, 0)
	}

	h.nodes[id] = node
	h.idMap[extID] = id
	h.reverseMap[id] = extID
	if metadata != nil {
		h.meta[id] = metadata
	}
	h.nodeCount++

	if h.maxLevel == -1 {
		h.entryPoint = id
		h.maxLevel = level
		return id
	}

	ep := h.entryPoint
	epDist := h.cfg.Dist(vec, h.nodes[ep].vector)

	for lc := h.maxLevel; lc > level; lc-- {
		changed := true
		for changed {
			changed = false
			neighbors := h.nodes[ep].neighborsAtLevel(lc)
			for _, n := range neighbors {
				nNode, ok := h.nodes[n]
				if !ok {
					continue
				}
				d := h.cfg.Dist(vec, nNode.vector)
				if d < epDist {
					ep = n
					epDist = d
					changed = true
				}
			}
		}
	}

	for lc := min(level, h.maxLevel); lc >= 0; lc-- {
		candidates := h.searchLayer(vec, ep, h.cfg.EfConstruction, lc)
		neighbors := h.selectNeighborsHeuristic(vec, candidates, h.cfg.M)

		node.setNeighborsAtLevel(lc, neighbors)

		maxConn := h.maxConnections(lc)
		for _, n := range neighbors {
			nNode, ok := h.nodes[n]
			if !ok {
				continue
			}
			nNode.Lock()
			conns := nNode.neighborsAtLevel(lc)
			conns = append(conns, id)
			if len(conns) > maxConn {
				conns = h.shrinkConnections(nNode.vector, conns, maxConn)
			}
			nNode.setNeighborsAtLevel(lc, conns)
			nNode.Unlock()
		}

		if len(candidates) > 0 {
			ep = candidates[0]
		}
	}

	if level > h.maxLevel {
		h.entryPoint = id
		h.maxLevel = level
	}

	return id
}

func (h *HNSW) searchLayer(query Vector, ep uint64, ef int, level int) []uint64 {
	visited := make(map[uint64]bool)
	visited[ep] = true

	epDist := h.cfg.Dist(query, h.nodes[ep].vector)

	candidates := newMinPQ(ef)
	candidates.Push(ep, epDist)

	results := newMaxPQ(ef)
	results.Push(ep, epDist)

	for candidates.Len() > 0 {
		c := candidates.Pop()

		if results.Len() >= ef && c.dist > results.Top().dist {
			break
		}

		cNode, ok := h.nodes[c.id]
		if !ok {
			continue
		}
		neighbors := cNode.neighborsAtLevel(level)
		for _, n := range neighbors {
			if visited[n] {
				continue
			}
			visited[n] = true

			nNode, ok := h.nodes[n]
			if !ok {
				continue
			}
			d := h.cfg.Dist(query, nNode.vector)

			if results.Len() < ef || d < results.Top().dist {
				candidates.Push(n, d)
				results.Push(n, d)
				if results.Len() > ef {
					results.Pop()
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

func (h *HNSW) selectNeighborsHeuristic(query Vector, candidateIDs []uint64, m int) []uint64 {
	if len(candidateIDs) <= m {
		return candidateIDs
	}

	candidates := make([]scored, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		node, ok := h.nodes[id]
		if !ok {
			continue
		}
		d := h.cfg.Dist(query, node.vector)
		candidates = append(candidates, scored{id, d})
	}
	sortScored(candidates)

	var selected []scored
	for _, c := range candidates {
		if len(selected) >= m {
			break
		}
		good := true
		for _, s := range selected {
			sNode, ok := h.nodes[s.id]
			cNode, ok2 := h.nodes[c.id]
			if !ok || !ok2 {
				continue
			}
			peerDist := h.cfg.Dist(cNode.vector, sNode.vector)
			if peerDist < c.dist {
				good = false
				break
			}
		}
		if good {
			selected = append(selected, c)
		}
	}

	result := make([]uint64, len(selected))
	for i, s := range selected {
		result[i] = s.id
	}
	return result
}

func (h *HNSW) shrinkConnections(nodeVec Vector, connections []uint64, maxConn int) []uint64 {
	return h.selectNeighborsHeuristic(nodeVec, connections, maxConn)
}

func (h *HNSW) Search(extID string, topK int) []SearchResult {
	h.mu.RLock()
	defer h.mu.RUnlock()

	id, ok := h.idMap[extID]
	if !ok {
		return nil
	}
	return h.SearchByVector(h.nodes[id].vector, topK, nil)
}

func (h *HNSW) SearchByVector(query Vector, topK int, filter func(map[string]interface{}) bool) []SearchResult {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.nodeCount == 0 || h.maxLevel == -1 {
		return nil
	}

	activeCount := int(h.nodeCount) - h.tombstones.Len()
	if filter != nil && activeCount < h.flatCutoff {
		return h.flatSearch(query, topK, filter)
	}

	if h.metrics != nil {
		h.metrics.TrackHNSWSearch()
	}

	ef := h.cfg.EfSearch
	if ef < topK {
		ef = topK
	}

	ep := h.entryPoint
	if h.tombstones.Has(ep) {
		h.findNewEntryPoint()
		ep = h.entryPoint
	}

	for lc := h.maxLevel; lc > 0; lc-- {
		changed := true
		for changed {
			changed = false
			if node, ok := h.nodes[ep]; ok {
				for _, n := range node.neighborsAtLevel(lc) {
					if h.tombstones.Has(n) {
						continue
					}
					if nNode, ok := h.nodes[n]; ok {
						if h.cfg.Dist(query, nNode.vector) < h.cfg.Dist(query, h.nodes[ep].vector) {
							ep = n
							changed = true
						}
					}
				}
			}
		}
	}

	candidateIDs := h.searchLayer(query, ep, ef, 0)

	var results []SearchResult
	for _, id := range candidateIDs {
		if h.tombstones.Has(id) {
			continue
		}
		if filter != nil {
			m := h.meta[id]
			if !filter(m) {
				continue
			}
		}
		node, ok := h.nodes[id]
		if !ok {
			continue
		}
		dist := h.cfg.Dist(query, node.vector)
		score := 1.0 / (1.0 + dist)
		results = append(results, SearchResult{
			ID:    h.reverseMap[id],
			Score: score,
			Data:  h.meta[id],
		})
		if len(results) >= topK {
			break
		}
	}
	return results
}

func (h *HNSW) Delete(extID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	id, ok := h.idMap[extID]
	if !ok {
		return
	}

	node := h.nodes[id]
	if node == nil {
		return
	}

	for level := 0; level <= node.level; level++ {
		for _, neighborID := range node.neighborsAtLevel(level) {
			nNode, ok := h.nodes[neighborID]
			if !ok {
				continue
			}
			nNode.Lock()
			conns := nNode.neighborsAtLevel(level)
			filtered := make([]uint64, 0, len(conns))
			for _, c := range conns {
				if c != id {
					filtered = append(filtered, c)
				}
			}
			nNode.setNeighborsAtLevel(level, filtered)
			nNode.Unlock()
		}
	}

	delete(h.nodes, id)
	delete(h.idMap, extID)
	delete(h.reverseMap, id)
	delete(h.meta, id)
	h.nodeCount--

	if h.entryPoint == id {
		h.findNewEntryPoint()
	}
}

func (h *HNSW) findNewEntryPoint() {
	h.maxLevel = -1
	for nid, node := range h.nodes {
		if node.level > h.maxLevel {
			h.maxLevel = node.level
			h.entryPoint = nid
		}
	}
}

func (h *HNSW) Count() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.nodeCount
}

func (h *HNSW) Stats() map[string]interface{} {
	h.mu.RLock()
	defer h.mu.RUnlock()

	levelDist := make(map[int]int)
	totalEdges := 0
	for _, node := range h.nodes {
		levelDist[node.level]++
		for _, conns := range node.connections {
			totalEdges += len(conns)
		}
	}

	return map[string]interface{}{
		"nodes":       h.nodeCount,
		"max_level":   h.maxLevel,
		"entry_point": h.entryPoint,
		"total_edges": totalEdges,
		"levels":      levelDist,
		"M":           h.cfg.M,
		"efConstruct": h.cfg.EfConstruction,
		"efSearch":    h.cfg.EfSearch,
	}
}

type scored struct {
	id   uint64
	dist float32
}

func sortScored(items []scored) {
	for i := 1; i < len(items); i++ {
		key := items[i]
		j := i - 1
		for j >= 0 && items[j].dist > key.dist {
			items[j+1] = items[j]
			j--
		}
		items[j+1] = key
	}
}

func reverseUint64(s []uint64) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func (h *HNSW) flatSearch(query Vector, topK int, filter func(map[string]interface{}) bool) []SearchResult {
	if h.metrics != nil {
		h.metrics.TrackFlatSearch()
	}
	results := newMaxPQ(topK)
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		if filter != nil {
			m := h.meta[id]
			if !filter(m) {
				continue
			}
		}
		dist := h.cfg.Dist(query, node.vector)
		if results.Len() < topK || dist < results.Top().dist {
			results.Push(id, dist)
			if results.Len() > topK {
				results.Pop()
			}
		}
	}

	out := make([]SearchResult, 0, results.Len())
	for results.Len() > 0 {
		item := results.Pop()
		score := 1.0 / (1.0 + item.dist)
		out = append(out, SearchResult{
			ID:    h.reverseMap[item.id],
			Score: score,
			Data:  h.meta[item.id],
		})
	}
	reverseResults(out)
	return out
}

func (h *HNSW) InsertBatch(items []struct {
	ID       string
	Vector   Vector
	Metadata map[string]interface{}
}) []uint64 {
	ids := make([]uint64, len(items))
	for i, item := range items {
		ids[i] = h.Insert(item.ID, item.Vector, item.Metadata)
	}
	return ids
}

func (h *HNSW) SearchByDistance(query Vector, maxDist float32, limit int, filter func(map[string]interface{}) bool) []SearchResult {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.nodeCount == 0 || h.maxLevel == -1 {
		return nil
	}

	ef := h.cfg.EfSearch * 2
	ep := h.entryPoint
	for lc := h.maxLevel; lc > 0; lc-- {
		changed := true
		for changed {
			changed = false
			if node, ok := h.nodes[ep]; ok {
				for _, n := range node.neighborsAtLevel(lc) {
					if nNode, ok := h.nodes[n]; ok {
						if h.cfg.Dist(query, nNode.vector) < h.cfg.Dist(query, h.nodes[ep].vector) {
							ep = n
							changed = true
						}
					}
				}
			}
		}
	}

	candidateIDs := h.searchLayer(query, ep, ef, 0)

	var results []SearchResult
	for _, id := range candidateIDs {
		if h.tombstones.Has(id) {
			continue
		}
		if filter != nil && !filter(h.meta[id]) {
			continue
		}
		node, ok := h.nodes[id]
		if !ok {
			continue
		}
		dist := h.cfg.Dist(query, node.vector)
		if dist > maxDist {
			continue
		}
		score := 1.0 / (1.0 + dist)
		results = append(results, SearchResult{
			ID:    h.reverseMap[id],
			Score: score,
			Data:  h.meta[id],
		})
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results
}

func (h *HNSW) ValidateBeforeInsert(vec Vector) error {
	if len(vec) == 0 {
		return fmt.Errorf("empty vector")
	}
	if h.nodeCount > 0 {
		for _, node := range h.nodes {
			if len(node.vector) != len(vec) {
				return fmt.Errorf("dimension mismatch: got %d, expected %d", len(vec), len(node.vector))
			}
			break
		}
	}
	return nil
}

func (h *HNSW) GetMetrics() *HNSWMetrics {
	return h.metrics
}

func (h *HNSW) SetFlatCutoff(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.flatCutoff = n
}

func (h *HNSW) SetEfSearch(ef int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.EfSearch = ef
}

func reverseResults(s []SearchResult) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
