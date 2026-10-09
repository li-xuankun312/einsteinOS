package vikingdb

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

func (h *HNSW) EnsureConnectivity() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.nodeCount < 2 || h.maxLevel == -1 {
		return 0
	}
	reachable := make(map[uint64]bool)
	queue := []uint64{h.entryPoint}
	reachable[h.entryPoint] = true
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		node := h.nodes[cur]
		if node == nil {
			continue
		}
		for level := 0; level <= node.level; level++ {
			for _, n := range node.neighborsAtLevel(level) {
				if !reachable[n] && !h.tombstones.Has(n) {
					reachable[n] = true
					queue = append(queue, n)
				}
			}
		}
	}
	connected := 0
	for id, node := range h.nodes {
		if h.tombstones.Has(id) || reachable[id] {
			continue
		}
		closest := h.findClosestReachable(node.vector, reachable)
		if closest == 0 {
			continue
		}
		node.Lock()
		conns := node.neighborsAtLevel(0)
		conns = append(conns, closest)
		node.setNeighborsAtLevel(0, conns)
		node.Unlock()
		closestNode := h.nodes[closest]
		if closestNode != nil {
			closestNode.Lock()
			cc := closestNode.neighborsAtLevel(0)
			cc = append(cc, id)
			maxC := h.maxConnections(0)
			if len(cc) > maxC {
				cc = h.shrinkConnections(closestNode.vector, cc, maxC)
			}
			closestNode.setNeighborsAtLevel(0, cc)
			closestNode.Unlock()
		}
		reachable[id] = true
		connected++
	}
	return connected
}

func (h *HNSW) findClosestReachable(query Vector, reachable map[uint64]bool) uint64 {
	bestID := uint64(0)
	bestDist := float32(math.MaxFloat32)
	for id := range reachable {
		node := h.nodes[id]
		if node == nil {
			continue
		}
		d := h.cfg.Dist(query, node.vector)
		if d < bestDist {
			bestDist = d
			bestID = id
		}
	}
	return bestID
}

func (h *HNSW) PruneWeakEdges(threshold float32) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	pruned := 0
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		for level := 0; level <= node.level; level++ {
			conns := node.neighborsAtLevel(level)
			if len(conns) <= 1 {
				continue
			}
			var kept []uint64
			for _, nid := range conns {
				nNode := h.nodes[nid]
				if nNode == nil {
					pruned++
					continue
				}
				dist := h.cfg.Dist(node.vector, nNode.vector)
				if dist <= threshold {
					kept = append(kept, nid)
				} else {
					pruned++
				}
			}
			if len(kept) < len(conns) {
				node.Lock()
				node.setNeighborsAtLevel(level, kept)
				node.Unlock()
			}
		}
	}
	return pruned
}

func (h *HNSW) RebalanceLevels() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	rebalanced := 0
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		expectedLevel := h.randomLevel()
		if expectedLevel > node.level {
			node.Lock()
			for level := node.level + 1; level <= expectedLevel; level++ {
				node.setNeighborsAtLevel(level, []uint64{})
			}
			node.level = expectedLevel
			node.Unlock()
			if expectedLevel > h.maxLevel {
				h.maxLevel = expectedLevel
				h.entryPoint = id
			}
			rebalanced++
		}
	}
	return rebalanced
}

type Subgraph struct {
	NodeIDs    []uint64
	Edges      map[uint64][]uint64
	Center     uint64
	Radius     float32
	Density    float64
}

func (h *HNSW) ExtractSubgraph(centerID string, hops int) *Subgraph {
	h.mu.RLock()
	defer h.mu.RUnlock()
	cid, ok := h.idMap[centerID]
	if !ok {
		return nil
	}
	visited := make(map[uint64]bool)
	visited[cid] = true
	frontier := []uint64{cid}
	edges := make(map[uint64][]uint64)
	for hop := 0; hop < hops && len(frontier) > 0; hop++ {
		var nextFrontier []uint64
		for _, id := range frontier {
			node := h.nodes[id]
			if node == nil {
				continue
			}
			for level := 0; level <= node.level; level++ {
				for _, n := range node.neighborsAtLevel(level) {
					if h.tombstones.Has(n) {
						continue
					}
					edges[id] = append(edges[id], n)
					if !visited[n] {
						visited[n] = true
						nextFrontier = append(nextFrontier, n)
					}
				}
			}
		}
		frontier = nextFrontier
	}
	nodeIDs := make([]uint64, 0, len(visited))
	for id := range visited {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
	maxDist := float32(0)
	centerNode := h.nodes[cid]
	if centerNode != nil {
		for _, id := range nodeIDs {
			if id == cid {
				continue
			}
			n := h.nodes[id]
			if n == nil {
				continue
			}
			d := h.cfg.Dist(centerNode.vector, n.vector)
			if d > maxDist {
				maxDist = d
			}
		}
	}
	totalEdges := 0
	for _, e := range edges {
		totalEdges += len(e)
	}
	density := float64(0)
	if len(nodeIDs) > 1 {
		maxEdges := len(nodeIDs) * (len(nodeIDs) - 1)
		density = float64(totalEdges) / float64(maxEdges)
	}
	return &Subgraph{
		NodeIDs: nodeIDs,
		Edges:   edges,
		Center:  cid,
		Radius:  maxDist,
		Density: density,
	}
}

func (sg *Subgraph) Summary() string {
	return fmt.Sprintf("Subgraph{nodes=%d edges=%d center=%d radius=%.4f density=%.3f}",
		len(sg.NodeIDs), sg.TotalEdges(), sg.Center, sg.Radius, sg.Density)
}

func (sg *Subgraph) TotalEdges() int {
	total := 0
	for _, e := range sg.Edges {
		total += len(e)
	}
	return total
}

type GraphPartition struct {
	Partitions [][]uint64
	Modularity float64
}

func (h *HNSW) SimplePartition(k int) *GraphPartition {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if int(h.nodeCount) <= k {
		single := make([]uint64, 0, h.nodeCount)
		for id := range h.nodes {
			if !h.tombstones.Has(id) {
				single = append(single, id)
			}
		}
		return &GraphPartition{Partitions: [][]uint64{single}}
	}
	seeds := h.selectDiverseSeeds(k)
	assignments := make(map[uint64]int)
	for i, seed := range seeds {
		assignments[seed] = i
	}
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		if _, ok := assignments[id]; ok {
			continue
		}
		bestPart := 0
		bestDist := float32(math.MaxFloat32)
		for i, seed := range seeds {
			seedNode := h.nodes[seed]
			if seedNode == nil {
				continue
			}
			d := h.cfg.Dist(node.vector, seedNode.vector)
			if d < bestDist {
				bestDist = d
				bestPart = i
			}
		}
		assignments[id] = bestPart
	}
	partitions := make([][]uint64, k)
	for id, part := range assignments {
		partitions[part] = append(partitions[part], id)
	}
	return &GraphPartition{Partitions: partitions}
}

func (h *HNSW) selectDiverseSeeds(k int) []uint64 {
	var allIDs []uint64
	for id := range h.nodes {
		if !h.tombstones.Has(id) {
			allIDs = append(allIDs, id)
		}
	}
	if len(allIDs) <= k {
		return allIDs
	}
	seeds := []uint64{allIDs[0]}
	for len(seeds) < k {
		bestID := uint64(0)
		bestMinDist := float32(0)
		for _, id := range allIDs {
			isSeed := false
			for _, s := range seeds {
				if id == s {
					isSeed = true
					break
				}
			}
			if isSeed {
				continue
			}
			minDist := float32(math.MaxFloat32)
			node := h.nodes[id]
			if node == nil {
				continue
			}
			for _, s := range seeds {
				sNode := h.nodes[s]
				if sNode == nil {
					continue
				}
				d := h.cfg.Dist(node.vector, sNode.vector)
				if d < minDist {
					minDist = d
				}
			}
			if minDist > bestMinDist {
				bestMinDist = minDist
				bestID = id
			}
		}
		if bestID == 0 {
			break
		}
		seeds = append(seeds, bestID)
	}
	return seeds
}

type GraphStats struct {
	NodeCount       int
	EdgeCount       int
	MaxLevel        int
	AvgDegree       float64
	MaxDegree       int
	MinDegree       int
	OrphanCount     int
	Components      int
	LargestComp     int
	DiameterEstimate int
}

func (h *HNSW) ComputeGraphStats() GraphStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	stats := GraphStats{
		MaxLevel:  h.maxLevel,
		MinDegree: int(^uint(0) >> 1),
	}
	degrees := make(map[uint64]int)
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		stats.NodeCount++
		deg := 0
		for level := 0; level <= node.level; level++ {
			deg += len(node.neighborsAtLevel(level))
		}
		degrees[id] = deg
		stats.EdgeCount += deg
		if deg > stats.MaxDegree {
			stats.MaxDegree = deg
		}
		if deg < stats.MinDegree {
			stats.MinDegree = deg
		}
		if deg == 0 {
			stats.OrphanCount++
		}
	}
	if stats.NodeCount > 0 {
		stats.AvgDegree = float64(stats.EdgeCount) / float64(stats.NodeCount)
	}
	if stats.NodeCount == 0 {
		stats.MinDegree = 0
	}
	components := h.countComponents()
	stats.Components = len(components)
	for _, comp := range components {
		if len(comp) > stats.LargestComp {
			stats.LargestComp = len(comp)
		}
	}
	return stats
}

func (h *HNSW) countComponents() [][]uint64 {
	visited := make(map[uint64]bool)
	var components [][]uint64
	for id := range h.nodes {
		if h.tombstones.Has(id) || visited[id] {
			continue
		}
		var comp []uint64
		queue := []uint64{id}
		visited[id] = true
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			comp = append(comp, cur)
			node := h.nodes[cur]
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
		components = append(components, comp)
	}
	return components
}

func (gs GraphStats) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("GraphStats:\n"))
	sb.WriteString(fmt.Sprintf("  Nodes: %d, Edges: %d\n", gs.NodeCount, gs.EdgeCount))
	sb.WriteString(fmt.Sprintf("  Degree: avg=%.1f max=%d min=%d\n", gs.AvgDegree, gs.MaxDegree, gs.MinDegree))
	sb.WriteString(fmt.Sprintf("  MaxLevel: %d, Orphans: %d\n", gs.MaxLevel, gs.OrphanCount))
	sb.WriteString(fmt.Sprintf("  Components: %d (largest=%d)\n", gs.Components, gs.LargestComp))
	return sb.String()
}

type EdgeIterator struct {
	h       *HNSW
	nodeIDs []uint64
	nodeIdx int
	level   int
	connIdx int
}

func (h *HNSW) NewEdgeIterator() *EdgeIterator {
	h.mu.RLock()
	ids := make([]uint64, 0, len(h.nodes))
	for id := range h.nodes {
		if !h.tombstones.Has(id) {
			ids = append(ids, id)
		}
	}
	h.mu.RUnlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return &EdgeIterator{h: h, nodeIDs: ids}
}

type Edge struct {
	From  uint64
	To    uint64
	Level int
	Dist  float32
}

func (ei *EdgeIterator) Next() (Edge, bool) {
	ei.h.mu.RLock()
	defer ei.h.mu.RUnlock()
	for ei.nodeIdx < len(ei.nodeIDs) {
		id := ei.nodeIDs[ei.nodeIdx]
		node := ei.h.nodes[id]
		if node == nil {
			ei.nodeIdx++
			ei.level = 0
			ei.connIdx = 0
			continue
		}
		for ei.level <= node.level {
			conns := node.neighborsAtLevel(ei.level)
			if ei.connIdx < len(conns) {
				to := conns[ei.connIdx]
				ei.connIdx++
				dist := float32(0)
				toNode := ei.h.nodes[to]
				if toNode != nil {
					dist = ei.h.cfg.Dist(node.vector, toNode.vector)
				}
				return Edge{From: id, To: to, Level: ei.level, Dist: dist}, true
			}
			ei.level++
			ei.connIdx = 0
		}
		ei.nodeIdx++
		ei.level = 0
		ei.connIdx = 0
	}
	return Edge{}, false
}

func (h *HNSW) RepairEntryPointLevel() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	epNode := h.nodes[h.entryPoint]
	if epNode == nil {
		h.repairEntryPoint()
		return true
	}
	if epNode.level < h.maxLevel {
		bestLevel := -1
		bestID := uint64(0)
		for id, node := range h.nodes {
			if !h.tombstones.Has(id) && node.level > bestLevel {
				bestLevel = node.level
				bestID = id
			}
		}
		if bestID != h.entryPoint {
			h.entryPoint = bestID
			h.maxLevel = bestLevel
			return true
		}
	}
	return false
}

func (h *HNSW) ShrinkToFit() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	removed := 0
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		for level := 0; level <= node.level; level++ {
			conns := node.neighborsAtLevel(level)
			var valid []uint64
			for _, c := range conns {
				if h.nodes[c] != nil && !h.tombstones.Has(c) {
					valid = append(valid, c)
				} else {
					removed++
				}
			}
			if len(valid) < len(conns) {
				node.Lock()
				node.setNeighborsAtLevel(level, valid)
				node.Unlock()
			}
		}
	}
	return removed
}

var _ sync.Mutex
