package vikingdb

import (
	"fmt"
	"strings"
)

func (h *HNSW) Validate() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var issues []string

	if h.maxLevel >= 0 {
		if _, ok := h.nodes[h.entryPoint]; !ok {
			issues = append(issues, fmt.Sprintf("entry point %d not found in nodes", h.entryPoint))
		}
	}

	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}

		extID, ok := h.reverseMap[id]
		if !ok {
			issues = append(issues, fmt.Sprintf("node %d has no external ID mapping", id))
		} else {
			if mappedID, ok := h.idMap[extID]; !ok || mappedID != id {
				issues = append(issues, fmt.Sprintf("node %d: idMap mismatch for extID %s", id, extID))
			}
		}

		if len(node.vector) == 0 {
			issues = append(issues, fmt.Sprintf("node %d has empty vector", id))
		}

		for level := 0; level <= node.level; level++ {
			conns := node.neighborsAtLevel(level)
			seen := make(map[uint64]bool)
			for _, c := range conns {
				if c == id {
					issues = append(issues, fmt.Sprintf("node %d has self-link at level %d", id, level))
				}
				if seen[c] {
					issues = append(issues, fmt.Sprintf("node %d has duplicate link to %d at level %d", id, c, level))
				}
				seen[c] = true

				if _, ok := h.nodes[c]; !ok {
					issues = append(issues, fmt.Sprintf("node %d links to non-existent node %d at level %d", id, c, level))
				}
			}

			maxConn := h.maxConnections(level)
			if len(conns) > maxConn {
				issues = append(issues, fmt.Sprintf("node %d has %d connections at level %d (max %d)", id, len(conns), level, maxConn))
			}
		}
	}

	for extID, id := range h.idMap {
		if _, ok := h.nodes[id]; !ok && !h.tombstones.Has(id) {
			issues = append(issues, fmt.Sprintf("idMap entry %s -> %d points to non-existent node", extID, id))
		}
	}

	return issues
}

func (h *HNSW) CheckBidirectional() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var issues []string
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		for level := 0; level <= node.level; level++ {
			for _, neighborID := range node.neighborsAtLevel(level) {
				nNode := h.nodes[neighborID]
				if nNode == nil {
					continue
				}
				nConns := nNode.neighborsAtLevel(level)
				found := false
				for _, c := range nConns {
					if c == id {
						found = true
						break
					}
				}
				if !found {
					issues = append(issues, fmt.Sprintf("unidirectional edge: %d -> %d at level %d (reverse missing)", id, neighborID, level))
				}
			}
		}
	}
	return issues
}

func (h *HNSW) LevelDistribution() map[int]int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	dist := make(map[int]int)
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		dist[node.level]++
	}
	return dist
}

func (h *HNSW) DegreeHistogram(level int) map[int]int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	hist := make(map[int]int)
	for id, node := range h.nodes {
		if h.tombstones.Has(id) || node.level < level {
			continue
		}
		degree := len(node.neighborsAtLevel(level))
		hist[degree]++
	}
	return hist
}

func (h *HNSW) FindOrphans() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	referenced := make(map[uint64]bool)
	referenced[h.entryPoint] = true

	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		for level := 0; level <= node.level; level++ {
			for _, c := range node.neighborsAtLevel(level) {
				referenced[c] = true
			}
		}
	}

	var orphans []string
	for id := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		if !referenced[id] && id != h.entryPoint {
			extID := h.reverseMap[id]
			orphans = append(orphans, extID)
		}
	}
	return orphans
}

func (h *HNSW) ReachabilityFromEntry() (reachable, unreachable int) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.maxLevel == -1 {
		return 0, 0
	}

	visited := make(map[uint64]bool)
	queue := []uint64{h.entryPoint}
	visited[h.entryPoint] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		node := h.nodes[current]
		if node == nil {
			continue
		}

		for level := 0; level <= node.level; level++ {
			for _, n := range node.neighborsAtLevel(level) {
				if !visited[n] && !h.tombstones.Has(n) {
					visited[n] = true
					queue = append(queue, n)
				}
			}
		}
	}

	total := 0
	for id := range h.nodes {
		if !h.tombstones.Has(id) {
			total++
		}
	}

	reachable = len(visited)
	unreachable = total - reachable
	return
}

func (h *HNSW) DebugSummary() string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var sb strings.Builder
	active := int(h.nodeCount) - h.tombstones.Len()
	sb.WriteString(fmt.Sprintf("HNSW Debug Summary\n"))
	sb.WriteString(fmt.Sprintf("  Nodes: %d active, %d tombstoned, %d total\n", active, h.tombstones.Len(), h.nodeCount))
	sb.WriteString(fmt.Sprintf("  Max Level: %d\n", h.maxLevel))
	sb.WriteString(fmt.Sprintf("  Entry Point: %d (%s)\n", h.entryPoint, h.reverseMap[h.entryPoint]))
	sb.WriteString(fmt.Sprintf("  Config: M=%d MMax=%d MMax0=%d efC=%d efS=%d\n",
		h.cfg.M, h.cfg.MMax, h.cfg.MMax0, h.cfg.EfConstruction, h.cfg.EfSearch))

	levelDist := make(map[int]int)
	totalEdges := 0
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		levelDist[node.level]++
		for level := 0; level <= node.level; level++ {
			totalEdges += len(node.neighborsAtLevel(level))
		}
	}

	sb.WriteString(fmt.Sprintf("  Total Edges: %d\n", totalEdges))
	if active > 0 {
		sb.WriteString(fmt.Sprintf("  Avg Degree: %.1f\n", float64(totalEdges)/float64(active)))
	}
	sb.WriteString(fmt.Sprintf("  Level Distribution:\n"))
	for l := h.maxLevel; l >= 0; l-- {
		count := levelDist[l]
		if count > 0 {
			bar := strings.Repeat("█", min(count, 50))
			sb.WriteString(fmt.Sprintf("    L%d: %5d %s\n", l, count, bar))
		}
	}

	if h.metrics != nil {
		m := h.metrics.Snapshot()
		sb.WriteString(fmt.Sprintf("  Metrics: %d inserts, %d searches, %d deletes, %d dist ops\n",
			m["inserts"], m["searches"], m["deletes"], m["dist_computations"]))
		sb.WriteString(fmt.Sprintf("  Latency: insert=%dμs search=%dμs delete=%dμs\n",
			m["avg_insert_us"], m["avg_search_us"], m["avg_delete_us"]))
	}

	return sb.String()
}

func (h *HNSW) DumpNode(extID string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	id, ok := h.idMap[extID]
	if !ok {
		return fmt.Sprintf("node %s not found", extID)
	}

	node := h.nodes[id]
	if node == nil {
		return fmt.Sprintf("node %s (id=%d) has nil node struct", extID, id)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Node: %s (id=%d)\n", extID, id))
	sb.WriteString(fmt.Sprintf("  Level: %d\n", node.level))
	sb.WriteString(fmt.Sprintf("  Vector dim: %d\n", len(node.vector)))
	if h.tombstones.Has(id) {
		sb.WriteString("  TOMBSTONED\n")
	}
	if id == h.entryPoint {
		sb.WriteString("  IS ENTRY POINT\n")
	}

	for level := 0; level <= node.level; level++ {
		conns := node.neighborsAtLevel(level)
		sb.WriteString(fmt.Sprintf("  L%d connections (%d):", level, len(conns)))
		for i, c := range conns {
			if i >= 10 {
				sb.WriteString(fmt.Sprintf(" +%d more", len(conns)-10))
				break
			}
			cExt := h.reverseMap[c]
			sb.WriteString(fmt.Sprintf(" %s", cExt))
		}
		sb.WriteString("\n")
	}

	if meta, ok := h.meta[id]; ok && len(meta) > 0 {
		sb.WriteString("  Metadata:\n")
		for k, v := range meta {
			sb.WriteString(fmt.Sprintf("    %s: %v\n", k, v))
		}
	}

	return sb.String()
}
