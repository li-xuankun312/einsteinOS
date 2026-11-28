package vikingdb

import (
	"fmt"
	"log"
	"time"
)

func (h *HNSW) DeleteByID(extID string) error {
	start := time.Now()
	defer func() {
		if h.metrics != nil {
			h.metrics.TrackDelete(start)
		}
	}()

	h.mu.Lock()
	defer h.mu.Unlock()

	id, ok := h.idMap[extID]
	if !ok {
		return fmt.Errorf("not found: %s", extID)
	}

	if h.tombstones.Has(id) {
		return fmt.Errorf("already deleted: %s", extID)
	}

	node := h.nodes[id]
	if node == nil {
		delete(h.idMap, extID)
		delete(h.reverseMap, id)
		return nil
	}

	h.disconnectNode(id, node)

	delete(h.nodes, id)
	delete(h.idMap, extID)
	delete(h.reverseMap, id)
	delete(h.meta, id)
	h.nodeCount--

	if h.entryPoint == id {
		h.repairEntryPoint()
	}

	return nil
}

func (h *HNSW) DeleteBatch(extIDs []string) map[string]error {
	start := time.Now()
	errors := make(map[string]error)

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, extID := range extIDs {
		id, ok := h.idMap[extID]
		if !ok {
			errors[extID] = fmt.Errorf("not found")
			continue
		}
		if h.tombstones.Has(id) {
			errors[extID] = fmt.Errorf("already deleted")
			continue
		}

		node := h.nodes[id]
		if node != nil {
			h.disconnectNode(id, node)
		}

		delete(h.nodes, id)
		delete(h.idMap, extID)
		delete(h.reverseMap, id)
		delete(h.meta, id)
		h.nodeCount--
	}

	h.repairEntryPoint()

	if h.metrics != nil {
		h.metrics.totalDeleteNs.Add(time.Since(start).Nanoseconds())
		h.metrics.deleteCount.Add(int64(len(extIDs) - len(errors)))
	}

	return errors
}

func (h *HNSW) SoftDelete(extIDs ...string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	deleted := 0
	for _, extID := range extIDs {
		id, ok := h.idMap[extID]
		if !ok {
			continue
		}
		if h.tombstones.Has(id) {
			continue
		}
		h.tombstones.Add(id)
		if h.metrics != nil {
			h.metrics.AddTombstone()
		}
		deleted++
	}
	return deleted
}

func (h *HNSW) Restore(extIDs ...string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	restored := 0
	for _, extID := range extIDs {
		id, ok := h.idMap[extID]
		if !ok {
			continue
		}
		if !h.tombstones.Has(id) {
			continue
		}
		h.tombstones.Remove(id)
		if h.metrics != nil {
			h.metrics.RemoveTombstone()
		}
		restored++
	}
	return restored
}

func (h *HNSW) disconnectNode(id uint64, node *hnswNode) {
	for level := 0; level <= node.level; level++ {
		for _, neighborID := range node.neighborsAtLevel(level) {
			nNode := h.nodes[neighborID]
			if nNode == nil {
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
}

func (h *HNSW) repairEntryPoint() {
	if h.nodeCount == 0 {
		h.maxLevel = -1
		h.entryPoint = 0
		return
	}

	if h.nodes[h.entryPoint] != nil && !h.tombstones.Has(h.entryPoint) {
		return
	}

	bestLevel := -1
	bestID := uint64(0)
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		if node.level > bestLevel {
			bestLevel = node.level
			bestID = id
		}
	}

	h.entryPoint = bestID
	h.maxLevel = bestLevel
}

func (h *HNSW) PurgeDeleted() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	tombstoneList := h.tombstones.List()
	purged := 0

	for _, id := range tombstoneList {
		node := h.nodes[id]
		if node != nil {
			h.disconnectNode(id, node)
			h.reconnectNeighborsOf(id, node)
		}

		extID := h.reverseMap[id]
		delete(h.nodes, id)
		delete(h.idMap, extID)
		delete(h.reverseMap, id)
		delete(h.meta, id)
		h.nodeCount--
		h.tombstones.Remove(id)
		purged++
	}

	if purged > 0 {
		h.repairEntryPoint()
		log.Printf("[hnsw] purged %d tombstoned nodes", purged)
	}

	if h.metrics != nil {
		h.metrics.SetTombstoneCount(0)
	}

	return purged
}

func (h *HNSW) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nodes = make(map[uint64]*hnswNode)
	h.idMap = make(map[string]uint64)
	h.reverseMap = make(map[uint64]string)
	h.meta = make(map[uint64]map[string]interface{})
	h.tombstones.Clear()
	h.nodeCount = 0
	h.maxLevel = -1
	h.entryPoint = 0
	h.nextID.Store(0)

	if h.metrics != nil {
		h.metrics.Reset()
	}
}

func (h *HNSW) DeleteWhere(predicate func(map[string]interface{}) bool) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	var toDelete []uint64
	for id, meta := range h.meta {
		if h.tombstones.Has(id) {
			continue
		}
		if predicate(meta) {
			toDelete = append(toDelete, id)
		}
	}

	for _, id := range toDelete {
		h.tombstones.Add(id)
		if h.metrics != nil {
			h.metrics.AddTombstone()
		}
	}

	return len(toDelete)
}

func (h *HNSW) ExpireOlderThan(field string, before int64) int {
	return h.DeleteWhere(func(meta map[string]interface{}) bool {
		if meta == nil {
			return false
		}
		val, ok := meta[field]
		if !ok {
			return false
		}
		switch v := val.(type) {
		case int64:
			return v < before
		case int:
			return int64(v) < before
		case float64:
			return int64(v) < before
		default:
			return false
		}
	})
}
