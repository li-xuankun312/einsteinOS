package vikingdb

import (
	"math"
	"sort"
	"sync"
)

type PrecomputedNorms struct {
	norms map[uint64]float32
	mu    sync.RWMutex
}

func NewPrecomputedNorms() *PrecomputedNorms {
	return &PrecomputedNorms{norms: make(map[uint64]float32)}
}

func (pn *PrecomputedNorms) Set(id uint64, vec []float32) {
	pn.mu.Lock()
	pn.norms[id] = computeNorm(vec)
	pn.mu.Unlock()
}

func (pn *PrecomputedNorms) Get(id uint64) (float32, bool) {
	pn.mu.RLock()
	n, ok := pn.norms[id]
	pn.mu.RUnlock()
	return n, ok
}

func (pn *PrecomputedNorms) Delete(id uint64) {
	pn.mu.Lock()
	delete(pn.norms, id)
	pn.mu.Unlock()
}

func (pn *PrecomputedNorms) Len() int {
	pn.mu.RLock()
	defer pn.mu.RUnlock()
	return len(pn.norms)
}

func computeNorm(vec []float32) float32 {
	var sum float32
	i := 0
	for ; i+3 < len(vec); i += 4 {
		sum += vec[i]*vec[i] + vec[i+1]*vec[i+1] + vec[i+2]*vec[i+2] + vec[i+3]*vec[i+3]
	}
	for ; i < len(vec); i++ {
		sum += vec[i] * vec[i]
	}
	return float32(math.Sqrt(float64(sum)))
}

func CosineDistWithNorms(a, b []float32, normA, normB float32) float32 {
	if normA == 0 || normB == 0 {
		return 2.0
	}
	var dot float32
	i := 0
	for ; i+3 < len(a); i += 4 {
		dot += a[i]*b[i] + a[i+1]*b[i+1] + a[i+2]*b[i+2] + a[i+3]*b[i+3]
	}
	for ; i < len(a); i++ {
		dot += a[i] * b[i]
	}
	return 1.0 - dot/(normA*normB)
}

type EarlyTermL2 struct {
	query    []float32
	bestDist float32
}

func NewEarlyTermL2(query []float32, bestDist float32) *EarlyTermL2 {
	return &EarlyTermL2{query: query, bestDist: bestDist}
}

func (et *EarlyTermL2) Distance(candidate []float32) (float32, bool) {
	if len(et.query) != len(candidate) {
		return math.MaxFloat32, false
	}
	var sum float32
	blockSize := 16
	i := 0
	for ; i+blockSize <= len(et.query); i += blockSize {
		var blockSum float32
		for j := 0; j < blockSize; j++ {
			d := et.query[i+j] - candidate[i+j]
			blockSum += d * d
		}
		sum += blockSum
		if sum > et.bestDist {
			return sum, false
		}
	}
	for ; i < len(et.query); i++ {
		d := et.query[i] - candidate[i]
		sum += d * d
	}
	return sum, sum < et.bestDist
}

func (et *EarlyTermL2) UpdateBest(dist float32) {
	if dist < et.bestDist {
		et.bestDist = dist
	}
}

type TopKCollector struct {
	k       int
	results []distIDPair
}

type distIDPair struct {
	id   uint64
	dist float32
}

func NewTopKCollector(k int) *TopKCollector {
	return &TopKCollector{
		k:       k,
		results: make([]distIDPair, 0, k+1),
	}
}

func (tc *TopKCollector) Add(id uint64, dist float32) bool {
	if len(tc.results) < tc.k {
		tc.results = append(tc.results, distIDPair{id, dist})
		if len(tc.results) == tc.k {
			sort.Slice(tc.results, func(i, j int) bool {
				return tc.results[i].dist < tc.results[j].dist
			})
		}
		return true
	}
	if dist >= tc.results[tc.k-1].dist {
		return false
	}
	pos := sort.Search(len(tc.results), func(i int) bool {
		return tc.results[i].dist > dist
	})
	tc.results = append(tc.results, distIDPair{})
	copy(tc.results[pos+1:], tc.results[pos:])
	tc.results[pos] = distIDPair{id, dist}
	if len(tc.results) > tc.k {
		tc.results = tc.results[:tc.k]
	}
	return true
}

func (tc *TopKCollector) WorstDist() float32 {
	if len(tc.results) < tc.k {
		return math.MaxFloat32
	}
	return tc.results[tc.k-1].dist
}

func (tc *TopKCollector) Results() []distIDPair {
	if len(tc.results) <= tc.k {
		return tc.results
	}
	return tc.results[:tc.k]
}

func (tc *TopKCollector) Full() bool {
	return len(tc.results) >= tc.k
}

func (tc *TopKCollector) Len() int {
	if len(tc.results) > tc.k {
		return tc.k
	}
	return len(tc.results)
}

type AlignedVectors struct {
	dim     int
	data    []float32
	ids     []uint64
	count   int
	stride  int
}

func NewAlignedVectors(dim int, capacity int) *AlignedVectors {
	stride := ((dim + 7) / 8) * 8
	return &AlignedVectors{
		dim:    dim,
		data:   make([]float32, 0, stride*capacity),
		ids:    make([]uint64, 0, capacity),
		stride: stride,
	}
}

func (av *AlignedVectors) Add(id uint64, vec []float32) {
	padded := make([]float32, av.stride)
	copy(padded, vec)
	av.data = append(av.data, padded...)
	av.ids = append(av.ids, id)
	av.count++
}

func (av *AlignedVectors) Get(index int) (uint64, []float32) {
	if index >= av.count {
		return 0, nil
	}
	start := index * av.stride
	return av.ids[index], av.data[start : start+av.dim]
}

func (av *AlignedVectors) Len() int {
	return av.count
}

func (av *AlignedVectors) BatchL2(query []float32) []distIDPair {
	results := make([]distIDPair, av.count)
	padQuery := make([]float32, av.stride)
	copy(padQuery, query)
	for i := 0; i < av.count; i++ {
		start := i * av.stride
		vec := av.data[start : start+av.stride]
		var sum float32
		for j := 0; j < av.stride; j += 8 {
			d0 := padQuery[j] - vec[j]
			d1 := padQuery[j+1] - vec[j+1]
			d2 := padQuery[j+2] - vec[j+2]
			d3 := padQuery[j+3] - vec[j+3]
			d4 := padQuery[j+4] - vec[j+4]
			d5 := padQuery[j+5] - vec[j+5]
			d6 := padQuery[j+6] - vec[j+6]
			d7 := padQuery[j+7] - vec[j+7]
			sum += d0*d0 + d1*d1 + d2*d2 + d3*d3 + d4*d4 + d5*d5 + d6*d6 + d7*d7
		}
		results[i] = distIDPair{id: av.ids[i], dist: sum}
	}
	return results
}

func (av *AlignedVectors) BatchDot(query []float32) []distIDPair {
	results := make([]distIDPair, av.count)
	padQuery := make([]float32, av.stride)
	copy(padQuery, query)
	for i := 0; i < av.count; i++ {
		start := i * av.stride
		vec := av.data[start : start+av.stride]
		var sum float32
		for j := 0; j < av.stride; j += 8 {
			sum += padQuery[j]*vec[j] + padQuery[j+1]*vec[j+1] +
				padQuery[j+2]*vec[j+2] + padQuery[j+3]*vec[j+3] +
				padQuery[j+4]*vec[j+4] + padQuery[j+5]*vec[j+5] +
				padQuery[j+6]*vec[j+6] + padQuery[j+7]*vec[j+7]
		}
		results[i] = distIDPair{id: av.ids[i], dist: -sum}
	}
	return results
}

func (av *AlignedVectors) TopK(query []float32, k int, distType string) []distIDPair {
	var results []distIDPair
	switch distType {
	case "dot":
		results = av.BatchDot(query)
	default:
		results = av.BatchL2(query)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].dist < results[j].dist
	})
	if len(results) > k {
		results = results[:k]
	}
	return results
}

func BatchDistances(query []float32, candidates [][]float32, distFn DistFunc) []float32 {
	dists := make([]float32, len(candidates))
	for i, c := range candidates {
		dists[i] = distFn(query, c)
	}
	return dists
}

func BatchDistancesConcurrent(query []float32, candidates [][]float32, distFn DistFunc, workers int) []float32 {
	if workers <= 1 || len(candidates) < 64 {
		return BatchDistances(query, candidates, distFn)
	}
	dists := make([]float32, len(candidates))
	chunkSize := (len(candidates) + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		s := w * chunkSize
		e := s + chunkSize
		if s >= len(candidates) {
			break
		}
		if e > len(candidates) {
			e = len(candidates)
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				dists[i] = distFn(query, candidates[i])
			}
		}(s, e)
	}
	wg.Wait()
	return dists
}

func FindNearest(query []float32, candidates [][]float32, ids []uint64, k int, distFn DistFunc) []distIDPair {
	collector := NewTopKCollector(k)
	for i, c := range candidates {
		dist := distFn(query, c)
		collector.Add(ids[i], dist)
	}
	return collector.Results()
}
