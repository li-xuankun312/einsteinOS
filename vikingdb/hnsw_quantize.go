package vikingdb

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

type ScalarQuantizer struct {
	dim    int
	mins   []float32
	maxs   []float32
	scales []float32
	trained bool
}

func NewScalarQuantizer(dim int) *ScalarQuantizer {
	return &ScalarQuantizer{
		dim:  dim,
		mins: make([]float32, dim),
		maxs: make([]float32, dim),
		scales: make([]float32, dim),
	}
}

func (sq *ScalarQuantizer) Train(vectors [][]float32) {
	if len(vectors) == 0 {
		return
	}
	for i := 0; i < sq.dim; i++ {
		sq.mins[i] = math.MaxFloat32
		sq.maxs[i] = -math.MaxFloat32
	}
	for _, v := range vectors {
		for i := 0; i < sq.dim && i < len(v); i++ {
			if v[i] < sq.mins[i] {
				sq.mins[i] = v[i]
			}
			if v[i] > sq.maxs[i] {
				sq.maxs[i] = v[i]
			}
		}
	}
	for i := 0; i < sq.dim; i++ {
		r := sq.maxs[i] - sq.mins[i]
		if r > 0 {
			sq.scales[i] = 255.0 / r
		} else {
			sq.scales[i] = 0
		}
	}
	sq.trained = true
}

func (sq *ScalarQuantizer) Encode(vec []float32) []uint8 {
	out := make([]uint8, sq.dim)
	for i := 0; i < sq.dim && i < len(vec); i++ {
		if sq.scales[i] == 0 {
			out[i] = 128
		} else {
			val := (vec[i] - sq.mins[i]) * sq.scales[i]
			if val < 0 {
				val = 0
			}
			if val > 255 {
				val = 255
			}
			out[i] = uint8(val)
		}
	}
	return out
}

func (sq *ScalarQuantizer) Decode(encoded []uint8) []float32 {
	out := make([]float32, sq.dim)
	for i := 0; i < sq.dim && i < len(encoded); i++ {
		if sq.scales[i] == 0 {
			out[i] = sq.mins[i]
		} else {
			out[i] = float32(encoded[i])/sq.scales[i] + sq.mins[i]
		}
	}
	return out
}

func (sq *ScalarQuantizer) DistanceEncoded(a, b []uint8) float32 {
	var sum uint32
	for i := range a {
		if i >= len(b) {
			break
		}
		d := int32(a[i]) - int32(b[i])
		sum += uint32(d * d)
	}
	return float32(sum)
}

func (sq *ScalarQuantizer) CompressionRatio() float64 {
	return float64(sq.dim*4) / float64(sq.dim)
}

func (sq *ScalarQuantizer) QuantizationError(vectors [][]float32) float64 {
	if !sq.trained || len(vectors) == 0 {
		return 0
	}
	var totalError float64
	for _, v := range vectors {
		encoded := sq.Encode(v)
		decoded := sq.Decode(encoded)
		var err float64
		for i := range v {
			if i >= len(decoded) {
				break
			}
			d := float64(v[i]) - float64(decoded[i])
			err += d * d
		}
		totalError += math.Sqrt(err)
	}
	return totalError / float64(len(vectors))
}

type ProductQuantizer struct {
	dim         int
	subDim      int
	numSubs     int
	numCentroids int
	centroids   [][][]float32
	trained     bool
}

func NewProductQuantizer(dim, numSubs, numCentroids int) *ProductQuantizer {
	subDim := dim / numSubs
	return &ProductQuantizer{
		dim:          dim,
		subDim:       subDim,
		numSubs:      numSubs,
		numCentroids: numCentroids,
		centroids:    make([][][]float32, numSubs),
	}
}

func (pq *ProductQuantizer) Train(vectors [][]float32, iterations int) {
	if len(vectors) == 0 {
		return
	}
	rng := rand.New(rand.NewSource(42))
	for s := 0; s < pq.numSubs; s++ {
		start := s * pq.subDim
		end := start + pq.subDim
		subVecs := make([][]float32, len(vectors))
		for i, v := range vectors {
			if end <= len(v) {
				subVecs[i] = v[start:end]
			} else {
				sub := make([]float32, pq.subDim)
				copy(sub, v[start:])
				subVecs[i] = sub
			}
		}
		pq.centroids[s] = kmeansSimple(subVecs, pq.numCentroids, iterations, rng)
	}
	pq.trained = true
}

func (pq *ProductQuantizer) Encode(vec []float32) []uint8 {
	codes := make([]uint8, pq.numSubs)
	for s := 0; s < pq.numSubs; s++ {
		start := s * pq.subDim
		end := start + pq.subDim
		var sub []float32
		if end <= len(vec) {
			sub = vec[start:end]
		} else {
			sub = make([]float32, pq.subDim)
			copy(sub, vec[start:])
		}
		bestIdx := 0
		bestDist := float32(math.MaxFloat32)
		for i, c := range pq.centroids[s] {
			d := l2SquaredDist(sub, c)
			if d < bestDist {
				bestDist = d
				bestIdx = i
			}
		}
		codes[s] = uint8(bestIdx)
	}
	return codes
}

func (pq *ProductQuantizer) Decode(codes []uint8) []float32 {
	vec := make([]float32, pq.dim)
	for s := 0; s < pq.numSubs && s < len(codes); s++ {
		start := s * pq.subDim
		idx := int(codes[s])
		if idx < len(pq.centroids[s]) {
			copy(vec[start:start+pq.subDim], pq.centroids[s][idx])
		}
	}
	return vec
}

func (pq *ProductQuantizer) DistanceTable(query []float32) [][]float32 {
	table := make([][]float32, pq.numSubs)
	for s := 0; s < pq.numSubs; s++ {
		start := s * pq.subDim
		end := start + pq.subDim
		var sub []float32
		if end <= len(query) {
			sub = query[start:end]
		} else {
			sub = make([]float32, pq.subDim)
			copy(sub, query[start:])
		}
		table[s] = make([]float32, pq.numCentroids)
		for i, c := range pq.centroids[s] {
			table[s][i] = l2SquaredDist(sub, c)
		}
	}
	return table
}

func (pq *ProductQuantizer) DistanceWithTable(table [][]float32, codes []uint8) float32 {
	var sum float32
	for s := 0; s < pq.numSubs && s < len(codes); s++ {
		idx := int(codes[s])
		if idx < len(table[s]) {
			sum += table[s][idx]
		}
	}
	return sum
}

func (pq *ProductQuantizer) CompressionRatio() float64 {
	return float64(pq.dim*4) / float64(pq.numSubs)
}

func (pq *ProductQuantizer) Summary() string {
	return fmt.Sprintf("PQ{dim=%d subs=%d×%d centroids=%d compression=%.0fx}",
		pq.dim, pq.numSubs, pq.subDim, pq.numCentroids, pq.CompressionRatio())
}

func kmeansSimple(vectors [][]float32, k, iterations int, rng *rand.Rand) [][]float32 {
	if len(vectors) == 0 || k == 0 {
		return nil
	}
	if k > len(vectors) {
		k = len(vectors)
	}
	dim := len(vectors[0])
	centroids := make([][]float32, k)
	perm := rng.Perm(len(vectors))
	for i := 0; i < k; i++ {
		centroids[i] = make([]float32, dim)
		copy(centroids[i], vectors[perm[i]])
	}
	assignments := make([]int, len(vectors))
	for iter := 0; iter < iterations; iter++ {
		for i, v := range vectors {
			bestIdx := 0
			bestDist := float32(math.MaxFloat32)
			for j, c := range centroids {
				d := l2SquaredDist(v, c)
				if d < bestDist {
					bestDist = d
					bestIdx = j
				}
			}
			assignments[i] = bestIdx
		}
		newCentroids := make([][]float32, k)
		counts := make([]int, k)
		for i := range newCentroids {
			newCentroids[i] = make([]float32, dim)
		}
		for i, v := range vectors {
			a := assignments[i]
			counts[a]++
			for d := range v {
				newCentroids[a][d] += v[d]
			}
		}
		for i := range centroids {
			if counts[i] > 0 {
				for d := range centroids[i] {
					centroids[i][d] = newCentroids[i][d] / float32(counts[i])
				}
			}
		}
	}
	return centroids
}

type QuantizedIndex struct {
	sq         *ScalarQuantizer
	pq         *ProductQuantizer
	useProduct bool
	encoded    map[uint64][]uint8
	dim        int
}

func NewQuantizedIndex(dim int, useProduct bool) *QuantizedIndex {
	qi := &QuantizedIndex{
		dim:     dim,
		encoded: make(map[uint64][]uint8),
		useProduct: useProduct,
	}
	if useProduct {
		numSubs := dim / 8
		if numSubs < 1 {
			numSubs = 1
		}
		qi.pq = NewProductQuantizer(dim, numSubs, 256)
	} else {
		qi.sq = NewScalarQuantizer(dim)
	}
	return qi
}

func (qi *QuantizedIndex) Train(vectors [][]float32) {
	if qi.useProduct {
		qi.pq.Train(vectors, 10)
	} else {
		qi.sq.Train(vectors)
	}
}

func (qi *QuantizedIndex) Add(id uint64, vec []float32) {
	if qi.useProduct {
		qi.encoded[id] = qi.pq.Encode(vec)
	} else {
		qi.encoded[id] = qi.sq.Encode(vec)
	}
}

func (qi *QuantizedIndex) Distance(id uint64, query []float32) float32 {
	codes, ok := qi.encoded[id]
	if !ok {
		return math.MaxFloat32
	}
	if qi.useProduct {
		table := qi.pq.DistanceTable(query)
		return qi.pq.DistanceWithTable(table, codes)
	}
	qCodes := qi.sq.Encode(query)
	return qi.sq.DistanceEncoded(qCodes, codes)
}

func (qi *QuantizedIndex) TopK(query []float32, k int) []distIDPair {
	type scored struct {
		id   uint64
		dist float32
	}
	var all []scored
	if qi.useProduct {
		table := qi.pq.DistanceTable(query)
		for id, codes := range qi.encoded {
			d := qi.pq.DistanceWithTable(table, codes)
			all = append(all, scored{id, d})
		}
	} else {
		qCodes := qi.sq.Encode(query)
		for id, codes := range qi.encoded {
			d := qi.sq.DistanceEncoded(qCodes, codes)
			all = append(all, scored{id, d})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].dist < all[j].dist })
	result := make([]distIDPair, 0, k)
	for i := 0; i < len(all) && i < k; i++ {
		result = append(result, distIDPair{id: all[i].id, dist: all[i].dist})
	}
	return result
}

func (qi *QuantizedIndex) Count() int {
	return len(qi.encoded)
}

func (qi *QuantizedIndex) MemoryUsage() int64 {
	total := int64(0)
	for _, codes := range qi.encoded {
		total += int64(len(codes))
	}
	return total
}
