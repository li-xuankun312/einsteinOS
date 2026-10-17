package vikingdb

const (
	IndexFlat = "flat"
	IndexHNSW = "hnsw"
)

type IndexBackend interface {
	Upsert(id string, vec Vector, metadata map[string]interface{})
	Delete(id string)
	Search(query Vector, topK int, filter func(map[string]interface{}) bool) []SearchResult
	Count() int
}

type flatBackend struct {
	idx *VectorIndex
}

func (f *flatBackend) Upsert(id string, vec Vector, metadata map[string]interface{}) {
	f.idx.Upsert(id, vec, metadata)
}

func (f *flatBackend) Delete(id string) {
	f.idx.Delete(id)
}

func (f *flatBackend) Search(query Vector, topK int, filter func(map[string]interface{}) bool) []SearchResult {
	return f.idx.Search(query, topK, filter)
}

func (f *flatBackend) Count() int {
	return f.idx.Count()
}

type hnswBackend struct {
	idx *HNSW
}

func (h *hnswBackend) Upsert(id string, vec Vector, metadata map[string]interface{}) {
	h.idx.Insert(id, vec, metadata)
}

func (h *hnswBackend) Delete(id string) {
	h.idx.Delete(id)
}

func (h *hnswBackend) Search(query Vector, topK int, filter func(map[string]interface{}) bool) []SearchResult {
	return h.idx.SearchByVector(query, topK, filter)
}

func (h *hnswBackend) Count() int {
	return int(h.idx.Count())
}

func NewIndexBackend(kind string, dim int) IndexBackend {
	switch kind {
	case IndexHNSW:
		cfg := DefaultHNSWConfig()
		return &hnswBackend{idx: NewHNSW(cfg)}
	default:
		return &flatBackend{idx: NewVectorIndex(dim)}
	}
}
