package vikingdb

import (
	"math"
	"sync"
)

type Vector []float32

func CosineSim(a, b Vector) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

func DotProduct(a, b Vector) float32 {
	if len(a) != len(b) {
		return 0
	}
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return float32(sum)
}

func L2Distance(a, b Vector) float32 {
	if len(a) != len(b) {
		return math.MaxFloat32
	}
	var sum float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		sum += d * d
	}
	return float32(math.Sqrt(sum))
}

func Normalize(v Vector) Vector {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return v
	}
	out := make(Vector, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / norm)
	}
	return out
}

type SearchResult struct {
	ID    string
	Score float32
	Data  map[string]interface{}
}

type VectorIndex struct {
	mu      sync.RWMutex
	dim     int
	vectors map[string]Vector
	meta    map[string]map[string]interface{}
}

func NewVectorIndex(dim int) *VectorIndex {
	return &VectorIndex{
		dim:     dim,
		vectors: make(map[string]Vector),
		meta:    make(map[string]map[string]interface{}),
	}
}

func (vi *VectorIndex) Upsert(id string, vec Vector, metadata map[string]interface{}) {
	vi.mu.Lock()
	defer vi.mu.Unlock()
	vi.vectors[id] = Normalize(vec)
	if metadata != nil {
		vi.meta[id] = metadata
	}
}

func (vi *VectorIndex) Delete(id string) {
	vi.mu.Lock()
	defer vi.mu.Unlock()
	delete(vi.vectors, id)
	delete(vi.meta, id)
}

func (vi *VectorIndex) Get(id string) (Vector, map[string]interface{}, bool) {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	v, ok := vi.vectors[id]
	if !ok {
		return nil, nil, false
	}
	return v, vi.meta[id], true
}

func (vi *VectorIndex) Search(query Vector, topK int, filter func(map[string]interface{}) bool) []SearchResult {
	vi.mu.RLock()
	defer vi.mu.RUnlock()

	query = Normalize(query)
	results := make([]SearchResult, 0, topK)

	for id, vec := range vi.vectors {
		if filter != nil {
			m := vi.meta[id]
			if !filter(m) {
				continue
			}
		}
		score := DotProduct(query, vec)
		results = insertSorted(results, SearchResult{ID: id, Score: score, Data: vi.meta[id]}, topK)
	}
	return results
}

func (vi *VectorIndex) Count() int {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	return len(vi.vectors)
}

func (vi *VectorIndex) Dim() int {
	return vi.dim
}

func insertSorted(results []SearchResult, item SearchResult, topK int) []SearchResult {
	pos := len(results)
	for i, r := range results {
		if item.Score > r.Score {
			pos = i
			break
		}
	}
	if pos >= topK {
		return results
	}
	if len(results) < topK {
		results = append(results, SearchResult{})
	}
	copy(results[pos+1:], results[pos:])
	results[pos] = item
	if len(results) > topK {
		results = results[:topK]
	}
	return results
}
