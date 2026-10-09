package vikingdb

import (
	"math"
	"sort"
	"sync"
)

type PQDistanceProvider struct {
	pq        *ProductQuantizer
	fallback  DistanceProvider
}

func NewPQDistanceProvider(pq *ProductQuantizer, fallback DistanceProvider) *PQDistanceProvider {
	return &PQDistanceProvider{pq: pq, fallback: fallback}
}

func (p *PQDistanceProvider) New(a []float32) Distancer {
	table := p.pq.DistanceTable(a)
	return &pqDistancer{table: table, pq: p.pq}
}

func (p *PQDistanceProvider) SingleDist(a, b []float32) float32 {
	return p.fallback.SingleDist(a, b)
}

func (p *PQDistanceProvider) Step(a, b []float32) float32 {
	return p.fallback.Step(a, b)
}

func (p *PQDistanceProvider) Wrap(x float32) float32 { return x }
func (p *PQDistanceProvider) Type() string            { return "pq-asymmetric" }

type pqDistancer struct {
	table [][]float32
	pq    *ProductQuantizer
}

func (d *pqDistancer) Distance(b []float32) float32 {
	codes := d.pq.Encode(b)
	return d.pq.DistanceWithTable(d.table, codes)
}

type ResidualQuantizer struct {
	coarse    *ProductQuantizer
	fine      *ProductQuantizer
	dim       int
}

func NewResidualQuantizer(dim, coarseSubs, fineSubs, centroids int) *ResidualQuantizer {
	return &ResidualQuantizer{
		dim:    dim,
		coarse: NewProductQuantizer(dim, coarseSubs, centroids),
		fine:   NewProductQuantizer(dim, fineSubs, centroids),
	}
}

func (rq *ResidualQuantizer) Train(vectors [][]float32, iterations int) {
	rq.coarse.Train(vectors, iterations)
	residuals := make([][]float32, len(vectors))
	for i, v := range vectors {
		coarseCodes := rq.coarse.Encode(v)
		approx := rq.coarse.Decode(coarseCodes)
		residuals[i] = make([]float32, rq.dim)
		for d := 0; d < rq.dim; d++ {
			if d < len(v) && d < len(approx) {
				residuals[i][d] = v[d] - approx[d]
			}
		}
	}
	rq.fine.Train(residuals, iterations)
}

type ResidualCode struct {
	Coarse []uint8
	Fine   []uint8
}

func (rq *ResidualQuantizer) Encode(vec []float32) ResidualCode {
	coarse := rq.coarse.Encode(vec)
	approx := rq.coarse.Decode(coarse)
	residual := make([]float32, rq.dim)
	for i := range residual {
		if i < len(vec) && i < len(approx) {
			residual[i] = vec[i] - approx[i]
		}
	}
	fine := rq.fine.Encode(residual)
	return ResidualCode{Coarse: coarse, Fine: fine}
}

func (rq *ResidualQuantizer) Decode(code ResidualCode) []float32 {
	coarseVec := rq.coarse.Decode(code.Coarse)
	fineVec := rq.fine.Decode(code.Fine)
	result := make([]float32, rq.dim)
	for i := range result {
		if i < len(coarseVec) {
			result[i] = coarseVec[i]
		}
		if i < len(fineVec) {
			result[i] += fineVec[i]
		}
	}
	return result
}

type Reranker struct {
	distFn  DistFunc
	vectors map[uint64]Vector
	mu      sync.RWMutex
}

func NewReranker(distFn DistFunc) *Reranker {
	return &Reranker{
		distFn:  distFn,
		vectors: make(map[uint64]Vector),
	}
}

func (r *Reranker) Store(id uint64, vec Vector) {
	r.mu.Lock()
	r.vectors[id] = vec
	r.mu.Unlock()
}

func (r *Reranker) Delete(id uint64) {
	r.mu.Lock()
	delete(r.vectors, id)
	r.mu.Unlock()
}

func (r *Reranker) Rerank(query Vector, candidates []distIDPair, topK int) []distIDPair {
	r.mu.RLock()
	defer r.mu.RUnlock()
	type rerankItem struct {
		id       uint64
		exactDist float32
	}
	var items []rerankItem
	for _, c := range candidates {
		vec, ok := r.vectors[c.id]
		if !ok {
			items = append(items, rerankItem{c.id, c.dist})
			continue
		}
		exact := r.distFn(query, vec)
		items = append(items, rerankItem{c.id, exact})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].exactDist < items[j].exactDist
	})
	if len(items) > topK {
		items = items[:topK]
	}
	result := make([]distIDPair, len(items))
	for i, item := range items {
		result[i] = distIDPair{id: item.id, dist: item.exactDist}
	}
	return result
}

func (r *Reranker) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.vectors)
}

func (r *Reranker) MemoryBytes() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var total int64
	for _, v := range r.vectors {
		total += int64(len(v)) * 4
	}
	return total
}

type TwoPhaseSearch struct {
	coarseIndex *QuantizedIndex
	reranker    *Reranker
	overFetch   int
}

func NewTwoPhaseSearch(dim int, overFetch int) *TwoPhaseSearch {
	return &TwoPhaseSearch{
		coarseIndex: NewQuantizedIndex(dim, false),
		reranker:    NewReranker(L2Distance),
		overFetch:   overFetch,
	}
}

func (tps *TwoPhaseSearch) Train(vectors [][]float32) {
	tps.coarseIndex.Train(vectors)
}

func (tps *TwoPhaseSearch) Add(id uint64, vec []float32) {
	tps.coarseIndex.Add(id, vec)
	tps.reranker.Store(id, vec)
}

func (tps *TwoPhaseSearch) Search(query []float32, topK int) []distIDPair {
	fetchK := topK * tps.overFetch
	if fetchK < 100 {
		fetchK = 100
	}
	coarseResults := tps.coarseIndex.TopK(query, fetchK)
	return tps.reranker.Rerank(query, coarseResults, topK)
}

func (tps *TwoPhaseSearch) Delete(id uint64) {
	tps.reranker.Delete(id)
}

func (tps *TwoPhaseSearch) Count() int {
	return tps.coarseIndex.Count()
}

type DistanceAccumulator struct {
	partial []float32
	dim     int
}

func NewDistanceAccumulator(dim int) *DistanceAccumulator {
	return &DistanceAccumulator{
		partial: make([]float32, dim),
		dim:     dim,
	}
}

func (da *DistanceAccumulator) AddPair(a, b float32, idx int) {
	if idx < da.dim {
		d := a - b
		da.partial[idx] = d * d
	}
}

func (da *DistanceAccumulator) Sum() float32 {
	var total float32
	for _, v := range da.partial {
		total += v
	}
	return total
}

func (da *DistanceAccumulator) PartialSum(upTo int) float32 {
	var total float32
	for i := 0; i < upTo && i < da.dim; i++ {
		total += da.partial[i]
	}
	return total
}

func (da *DistanceAccumulator) Reset() {
	for i := range da.partial {
		da.partial[i] = 0
	}
}

func (da *DistanceAccumulator) Exceeds(threshold float32, afterDim int) bool {
	return da.PartialSum(afterDim) > threshold
}

func BinaryQuantize(vec []float32) []uint64 {
	words := (len(vec) + 63) / 64
	bits := make([]uint64, words)
	for i, v := range vec {
		if v > 0 {
			bits[i/64] |= 1 << (uint(i) % 64)
		}
	}
	return bits
}

func BinaryHammingDist(a, b []uint64) int {
	dist := 0
	for i := range a {
		if i >= len(b) {
			break
		}
		x := a[i] ^ b[i]
		for x != 0 {
			dist++
			x &= x - 1
		}
	}
	return dist
}

type BinaryIndex struct {
	codes map[uint64][]uint64
	dim   int
}

func NewBinaryIndex(dim int) *BinaryIndex {
	return &BinaryIndex{
		codes: make(map[uint64][]uint64),
		dim:   dim,
	}
}

func (bi *BinaryIndex) Add(id uint64, vec []float32) {
	bi.codes[id] = BinaryQuantize(vec)
}

func (bi *BinaryIndex) Search(query []float32, k int) []distIDPair {
	qCode := BinaryQuantize(query)
	type scored struct {
		id   uint64
		dist int
	}
	var results []scored
	for id, code := range bi.codes {
		d := BinaryHammingDist(qCode, code)
		results = append(results, scored{id, d})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].dist < results[j].dist
	})
	out := make([]distIDPair, 0, k)
	for i := 0; i < len(results) && i < k; i++ {
		out = append(out, distIDPair{id: results[i].id, dist: float32(results[i].dist)})
	}
	return out
}

func (bi *BinaryIndex) Count() int {
	return len(bi.codes)
}

func (bi *BinaryIndex) MemoryBytes() int64 {
	var total int64
	for _, code := range bi.codes {
		total += int64(len(code)) * 8
	}
	return total
}

var _ = math.MaxFloat32
