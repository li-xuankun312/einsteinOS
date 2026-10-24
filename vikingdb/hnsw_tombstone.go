package vikingdb

import (
	"sync"
)

type tombstoneSet struct {
	mu    sync.RWMutex
	nodes map[uint64]bool
}

func newTombstoneSet() *tombstoneSet {
	return &tombstoneSet{
		nodes: make(map[uint64]bool),
	}
}

func (t *tombstoneSet) Add(ids ...uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, id := range ids {
		t.nodes[id] = true
	}
}

func (t *tombstoneSet) Has(id uint64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.nodes[id]
}

func (t *tombstoneSet) HasAny(ids []uint64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, id := range ids {
		if t.nodes[id] {
			return true
		}
	}
	return false
}

func (t *tombstoneSet) Remove(ids ...uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, id := range ids {
		delete(t.nodes, id)
	}
}

func (t *tombstoneSet) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.nodes)
}

func (t *tombstoneSet) List() []uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]uint64, 0, len(t.nodes))
	for id := range t.nodes {
		out = append(out, id)
	}
	return out
}

func (t *tombstoneSet) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nodes = make(map[uint64]bool)
}

func (h *HNSW) DeleteWithTombstone(extIDs ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, extID := range extIDs {
		id, ok := h.idMap[extID]
		if !ok {
			continue
		}
		h.tombstones.Add(id)
		if h.metrics != nil {
			h.metrics.AddTombstone()
		}
	}
}

func (h *HNSW) CleanupTombstones(maxPerCycle int) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	tombstoneList := h.tombstones.List()
	if len(tombstoneList) == 0 {
		return 0
	}

	cleaned := 0
	for _, id := range tombstoneList {
		if cleaned >= maxPerCycle {
			break
		}

		node := h.nodes[id]
		if node == nil {
			h.tombstones.Remove(id)
			continue
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

		h.reassignOrphanedNeighbors(id, node)

		extID := h.reverseMap[id]
		delete(h.nodes, id)
		delete(h.idMap, extID)
		delete(h.reverseMap, id)
		delete(h.meta, id)
		h.nodeCount--
		h.tombstones.Remove(id)
		cleaned++

		if h.metrics != nil {
			h.metrics.RemoveTombstone()
			h.metrics.TrackCleanup()
		}
	}

	if h.tombstones.Has(h.entryPoint) || h.nodes[h.entryPoint] == nil {
		h.findNewEntryPoint()
	}

	return cleaned
}

func (h *HNSW) reassignOrphanedNeighbors(deletedID uint64, deletedNode *hnswNode) {
	for level := 0; level <= deletedNode.level; level++ {
		neighbors := deletedNode.neighborsAtLevel(level)
		for _, neighborID := range neighbors {
			nNode, ok := h.nodes[neighborID]
			if !ok || h.tombstones.Has(neighborID) {
				continue
			}

			conns := nNode.neighborsAtLevel(level)
			hasDeleted := false
			for _, c := range conns {
				if c == deletedID {
					hasDeleted = true
					break
				}
			}
			if !hasDeleted {
				continue
			}

			candidates := make(map[uint64]bool)
			for _, c := range conns {
				if c != deletedID && !h.tombstones.Has(c) {
					candidates[c] = true
				}
			}
			for _, dn := range neighbors {
				if dn != deletedID && dn != neighborID && !h.tombstones.Has(dn) {
					candidates[dn] = true
				}
			}

			candidateList := make([]uint64, 0, len(candidates))
			for c := range candidates {
				candidateList = append(candidateList, c)
			}

			maxConn := h.maxConnections(level)
			selected := h.selectNeighborsHeuristic(nNode.vector, candidateList, maxConn)

			nNode.Lock()
			nNode.setNeighborsAtLevel(level, selected)
			nNode.Unlock()
		}
	}
}

func (h *HNSW) TombstoneCount() int {
	return h.tombstones.Len()
}

func (h *HNSW) HasTombstone(extID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	id, ok := h.idMap[extID]
	if !ok {
		return false
	}
	return h.tombstones.Has(id)
}
