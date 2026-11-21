package vikingdb

import (
	"sync"
)

type hnswPools struct {
	visitedSets   sync.Pool
	minPQs        sync.Pool
	maxPQs        sync.Pool
	candidateBufs sync.Pool
	resultBufs    sync.Pool
}

func newHNSWPools(initialVisitedSize int, pqCapacity int) *hnswPools {
	return &hnswPools{
		visitedSets: sync.Pool{
			New: func() interface{} {
				return newVisitedSet(initialVisitedSize)
			},
		},
		minPQs: sync.Pool{
			New: func() interface{} {
				return newMinPQ(pqCapacity)
			},
		},
		maxPQs: sync.Pool{
			New: func() interface{} {
				return newMaxPQ(pqCapacity)
			},
		},
		candidateBufs: sync.Pool{
			New: func() interface{} {
				s := make([]uint64, 0, 128)
				return &s
			},
		},
		resultBufs: sync.Pool{
			New: func() interface{} {
				s := make([]SearchResult, 0, 32)
				return &s
			},
		},
	}
}

func (p *hnswPools) getVisited() *visitedSet {
	v := p.visitedSets.Get().(*visitedSet)
	v.Reset()
	return v
}

func (p *hnswPools) putVisited(v *visitedSet) {
	p.visitedSets.Put(v)
}

func (p *hnswPools) getMinPQ() *minPQ {
	pq := p.minPQs.Get().(*minPQ)
	pq.items = pq.items[:0]
	return pq
}

func (p *hnswPools) putMinPQ(pq *minPQ) {
	p.minPQs.Put(pq)
}

func (p *hnswPools) getMaxPQ() *maxPQ {
	pq := p.maxPQs.Get().(*maxPQ)
	pq.items = pq.items[:0]
	return pq
}

func (p *hnswPools) putMaxPQ(pq *maxPQ) {
	p.maxPQs.Put(pq)
}

func (p *hnswPools) getCandidateBuf() []uint64 {
	buf := p.candidateBufs.Get().(*[]uint64)
	return (*buf)[:0]
}

func (p *hnswPools) putCandidateBuf(buf []uint64) {
	p.candidateBufs.Put(&buf)
}

func (p *hnswPools) getResultBuf() []SearchResult {
	buf := p.resultBufs.Get().(*[]SearchResult)
	return (*buf)[:0]
}

func (p *hnswPools) putResultBuf(buf []SearchResult) {
	p.resultBufs.Put(&buf)
}

type distCacheEntry struct {
	id   uint64
	dist float32
}

type distCache struct {
	mu      sync.RWMutex
	entries map[uint64]map[uint64]float32
	maxSize int
}

func newDistCache(maxSize int) *distCache {
	return &distCache{
		entries: make(map[uint64]map[uint64]float32),
		maxSize: maxSize,
	}
}

func (dc *distCache) Get(a, b uint64) (float32, bool) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	if m, ok := dc.entries[a]; ok {
		if d, ok := m[b]; ok {
			return d, true
		}
	}
	if m, ok := dc.entries[b]; ok {
		if d, ok := m[a]; ok {
			return d, true
		}
	}
	return 0, false
}

func (dc *distCache) Put(a, b uint64, dist float32) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if len(dc.entries) >= dc.maxSize {
		for k := range dc.entries {
			delete(dc.entries, k)
			break
		}
	}
	if dc.entries[a] == nil {
		dc.entries[a] = make(map[uint64]float32)
	}
	dc.entries[a][b] = dist
}

func (dc *distCache) Clear() {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.entries = make(map[uint64]map[uint64]float32)
}

func (dc *distCache) Size() int {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	total := 0
	for _, m := range dc.entries {
		total += len(m)
	}
	return total
}
