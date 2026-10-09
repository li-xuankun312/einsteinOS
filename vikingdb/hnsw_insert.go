package vikingdb

import (
	"fmt"
	"sync"
	"time"
)

func (h *HNSW) Add(extID string, vec Vector) (uint64, error) {
	if err := h.ValidateBeforeInsert(vec); err != nil {
		return 0, err
	}
	start := time.Now()
	id := h.Insert(extID, vec, nil)
	if h.metrics != nil {
		h.metrics.TrackInsert(start)
	}
	return id, nil
}

func (h *HNSW) AddWithMeta(extID string, vec Vector, meta map[string]interface{}) (uint64, error) {
	if err := h.ValidateBeforeInsert(vec); err != nil {
		return 0, err
	}
	start := time.Now()
	id := h.Insert(extID, vec, meta)
	if h.metrics != nil {
		h.metrics.TrackInsert(start)
	}
	return id, nil
}

type BatchItem struct {
	ID       string
	Vector   Vector
	Metadata map[string]interface{}
}

type BatchResult struct {
	ID     uint64
	ExtID  string
	Error  error
}

func (h *HNSW) AddBatch(items []BatchItem) []BatchResult {
	results := make([]BatchResult, len(items))

	if len(items) > 0 {
		if err := h.ValidateBeforeInsert(items[0].Vector); err != nil {
			for i := range results {
				results[i].Error = err
			}
			return results
		}
	}

	for i, item := range items {
		for j := 0; j < i; j++ {
			if items[j].ID == item.ID {
				results[i].Error = fmt.Errorf("duplicate ID: %s", item.ID)
				break
			}
		}
	}

	start := time.Now()
	for i, item := range items {
		if results[i].Error != nil {
			continue
		}
		if len(item.Vector) == 0 {
			results[i].Error = fmt.Errorf("empty vector for %s", item.ID)
			continue
		}
		id := h.Insert(item.ID, item.Vector, item.Metadata)
		results[i].ID = id
		results[i].ExtID = item.ID
	}

	if h.metrics != nil {
		elapsed := time.Since(start)
		for range items {
			h.metrics.insertCount.Add(1)
		}
		h.metrics.totalInsertNs.Add(elapsed.Nanoseconds())
	}

	return results
}

func (h *HNSW) AddBatchConcurrent(items []BatchItem, workers int) []BatchResult {
	if workers <= 1 || len(items) <= 10 {
		return h.AddBatch(items)
	}

	results := make([]BatchResult, len(items))

	if len(items) > 0 {
		if err := h.ValidateBeforeInsert(items[0].Vector); err != nil {
			for i := range results {
				results[i].Error = err
			}
			return results
		}
	}

	first := items[0]
	id := h.Insert(first.ID, first.Vector, first.Metadata)
	results[0] = BatchResult{ID: id, ExtID: first.ID}

	remaining := items[1:]
	remainingResults := results[1:]

	chunkSize := (len(remaining) + workers - 1) / workers
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= len(remaining) {
			break
		}
		if end > len(remaining) {
			end = len(remaining)
		}

		wg.Add(1)
		go func(items []BatchItem, results []BatchResult) {
			defer wg.Done()
			for i, item := range items {
				if len(item.Vector) == 0 {
					results[i].Error = fmt.Errorf("empty vector")
					continue
				}
				id := h.Insert(item.ID, item.Vector, item.Metadata)
				results[i].ID = id
				results[i].ExtID = item.ID
			}
		}(remaining[start:end], remainingResults[start:end])
	}

	wg.Wait()
	return results
}

func (h *HNSW) Upsert(extID string, vec Vector, meta map[string]interface{}) uint64 {
	h.mu.Lock()
	if existing, ok := h.idMap[extID]; ok {
		node := h.nodes[existing]
		if node != nil {
			node.vector = vec
		}
		if meta != nil {
			h.meta[existing] = meta
		}
		if h.tombstones.Has(existing) {
			h.tombstones.Remove(existing)
		}
		h.mu.Unlock()
		return existing
	}
	h.mu.Unlock()

	return h.Insert(extID, vec, meta)
}

func (h *HNSW) UpsertBatch(items []BatchItem) []uint64 {
	ids := make([]uint64, len(items))
	for i, item := range items {
		ids[i] = h.Upsert(item.ID, item.Vector, item.Metadata)
	}
	return ids
}

func (h *HNSW) UpdateMeta(extID string, meta map[string]interface{}) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	id, ok := h.idMap[extID]
	if !ok {
		return fmt.Errorf("not found: %s", extID)
	}

	existing := h.meta[id]
	if existing == nil {
		existing = make(map[string]interface{})
	}
	for k, v := range meta {
		existing[k] = v
	}
	h.meta[id] = existing
	return nil
}

func (h *HNSW) UpdateVector(extID string, vec Vector) error {
	if err := h.ValidateBeforeInsert(vec); err != nil {
		return err
	}

	h.mu.Lock()
	id, ok := h.idMap[extID]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("not found: %s", extID)
	}

	node := h.nodes[id]
	if node == nil {
		h.mu.Unlock()
		return fmt.Errorf("node struct nil: %s", extID)
	}

	meta := h.meta[id]
	h.mu.Unlock()

	h.Delete(extID)
	h.Insert(extID, vec, meta)
	return nil
}

func (h *HNSW) Preload(extID string, vec Vector) {
	h.mu.Lock()
	defer h.mu.Unlock()

	id := h.nextID.Add(1) - 1
	node := &hnswNode{
		id:          id,
		level:       0,
		connections: [][]uint64{{}},
		vector:      vec,
	}
	h.nodes[id] = node
	h.idMap[extID] = id
	h.reverseMap[id] = extID
	h.nodeCount++

	if h.maxLevel == -1 {
		h.entryPoint = id
		h.maxLevel = 0
	}
}

func (h *HNSW) PreloadBatch(items []BatchItem) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, item := range items {
		id := h.nextID.Add(1) - 1
		node := &hnswNode{
			id:          id,
			level:       0,
			connections: [][]uint64{{}},
			vector:      item.Vector,
		}
		h.nodes[id] = node
		h.idMap[item.ID] = id
		h.reverseMap[id] = item.ID
		if item.Metadata != nil {
			h.meta[id] = item.Metadata
		}
		h.nodeCount++

		if h.maxLevel == -1 {
			h.entryPoint = id
			h.maxLevel = 0
		}
	}
}

func (h *HNSW) BuildConnections() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.nodeCount < 2 {
		return 0
	}

	connected := 0
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		if len(node.neighborsAtLevel(0)) > 0 {
			continue
		}

		nfc := newNeighborFinderConnector(h, id, node, h.entryPoint, node.vector, node.level, h.maxLevel)
		nfc.Do()
		connected++
	}
	return connected
}

func EstimateBatchMemory(items []BatchItem) int64 {
	if len(items) == 0 {
		return 0
	}
	dim := len(items[0].Vector)
	perVector := int64(dim*4 + 64)
	return int64(len(items)) * perVector
}

func (h *HNSW) Exists(extID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	id, ok := h.idMap[extID]
	if !ok {
		return false
	}
	return !h.tombstones.Has(id)
}

func (h *HNSW) Get(extID string) (Vector, map[string]interface{}, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	id, ok := h.idMap[extID]
	if !ok || h.tombstones.Has(id) {
		return nil, nil, false
	}
	node := h.nodes[id]
	if node == nil {
		return nil, nil, false
	}
	return node.vector, h.meta[id], true
}

func (h *HNSW) AllIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.idMap))
	for extID, id := range h.idMap {
		if !h.tombstones.Has(id) {
			ids = append(ids, extID)
		}
	}
	return ids
}
