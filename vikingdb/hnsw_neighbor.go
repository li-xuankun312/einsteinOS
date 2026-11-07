package vikingdb

import (
	"sync"
)

type neighborFinderConnector struct {
	graph           *HNSW
	nodeID          uint64
	node            *hnswNode
	entryPointID    uint64
	entryPointDist  float32
	nodeVec         Vector
	targetLevel     int
	currentMaxLevel int
	connectionsBuf  []uint64
	pendingBuf      []uint64
}

func newNeighborFinderConnector(graph *HNSW, nodeID uint64, node *hnswNode,
	entryPointID uint64, nodeVec Vector, targetLevel, currentMaxLevel int) *neighborFinderConnector {
	return &neighborFinderConnector{
		graph:           graph,
		nodeID:          nodeID,
		node:            node,
		entryPointID:    entryPointID,
		nodeVec:         nodeVec,
		targetLevel:     targetLevel,
		currentMaxLevel: currentMaxLevel,
	}
}

func (n *neighborFinderConnector) Do() {
	ep := n.entryPointID

	for lc := n.currentMaxLevel; lc > n.targetLevel; lc-- {
		ep = n.greedyClosest(ep, lc)
	}

	for lc := min(n.targetLevel, n.currentMaxLevel); lc >= 0; lc-- {
		n.doAtLevel(lc, ep)
		neighbors := n.node.neighborsAtLevel(lc)
		if len(neighbors) > 0 {
			ep = n.closestAmong(neighbors)
		}
	}
}

func (n *neighborFinderConnector) greedyClosest(ep uint64, level int) uint64 {
	epNode := n.graph.nodes[ep]
	if epNode == nil {
		return ep
	}
	bestDist := n.graph.cfg.Dist(n.nodeVec, epNode.vector)
	changed := true
	for changed {
		changed = false
		neighbors := epNode.neighborsAtLevel(level)
		for _, neighborID := range neighbors {
			if n.graph.tombstones.Has(neighborID) {
				continue
			}
			nNode := n.graph.nodes[neighborID]
			if nNode == nil {
				continue
			}
			d := n.graph.cfg.Dist(n.nodeVec, nNode.vector)
			if d < bestDist {
				ep = neighborID
				epNode = nNode
				bestDist = d
				changed = true
			}
		}
	}
	return ep
}

func (n *neighborFinderConnector) doAtLevel(level int, ep uint64) {
	ef := n.graph.cfg.EfConstruction
	candidates := n.graph.searchLayer(n.nodeVec, ep, ef, level)

	filteredCandidates := make([]uint64, 0, len(candidates))
	for _, c := range candidates {
		if c != n.nodeID && !n.graph.tombstones.Has(c) {
			filteredCandidates = append(filteredCandidates, c)
		}
	}

	neighbors := n.graph.selectNeighborsHeuristic(n.nodeVec, filteredCandidates, n.graph.cfg.M)
	n.node.setNeighborsAtLevel(level, neighbors)

	maxConn := n.graph.maxConnections(level)
	for _, neighborID := range neighbors {
		n.connectNeighborAtLevel(neighborID, level, maxConn)
	}
}

func (n *neighborFinderConnector) connectNeighborAtLevel(neighborID uint64, level, maxConn int) {
	nNode := n.graph.nodes[neighborID]
	if nNode == nil {
		return
	}

	nNode.Lock()
	defer nNode.Unlock()

	conns := nNode.neighborsAtLevel(level)

	for _, c := range conns {
		if c == n.nodeID {
			return
		}
	}

	conns = append(conns, n.nodeID)

	if len(conns) > maxConn {
		conns = n.graph.shrinkConnections(nNode.vector, conns, maxConn)
	}

	nNode.setNeighborsAtLevel(level, conns)
}

func (n *neighborFinderConnector) closestAmong(ids []uint64) uint64 {
	best := ids[0]
	bestDist := n.graph.cfg.Dist(n.nodeVec, n.graph.nodes[best].vector)
	for _, id := range ids[1:] {
		node := n.graph.nodes[id]
		if node == nil {
			continue
		}
		d := n.graph.cfg.Dist(n.nodeVec, node.vector)
		if d < bestDist {
			best = id
			bestDist = d
		}
	}
	return best
}

func (h *HNSW) reconnectNeighborsOf(deletedID uint64, deletedNode *hnswNode) {
	for level := 0; level <= deletedNode.level; level++ {
		neighbors := deletedNode.neighborsAtLevel(level)
		for _, neighborID := range neighbors {
			nNode := h.nodes[neighborID]
			if nNode == nil || h.tombstones.Has(neighborID) {
				continue
			}

			nNode.Lock()
			conns := nNode.neighborsAtLevel(level)
			newConns := make([]uint64, 0, len(conns))
			for _, c := range conns {
				if c != deletedID && !h.tombstones.Has(c) {
					newConns = append(newConns, c)
				}
			}

			for _, otherNeighbor := range neighbors {
				if otherNeighbor == deletedID || otherNeighbor == neighborID || h.tombstones.Has(otherNeighbor) {
					continue
				}
				found := false
				for _, c := range newConns {
					if c == otherNeighbor {
						found = true
						break
					}
				}
				if !found {
					newConns = append(newConns, otherNeighbor)
				}
			}

			maxConn := h.maxConnections(level)
			if len(newConns) > maxConn {
				newConns = h.selectNeighborsHeuristic(nNode.vector, newConns, maxConn)
			}
			nNode.setNeighborsAtLevel(level, newConns)
			nNode.Unlock()
		}
	}
}

func (h *HNSW) repairConnections(nodeID uint64, level int) {
	node := h.nodes[nodeID]
	if node == nil {
		return
	}

	conns := node.neighborsAtLevel(level)
	validConns := make([]uint64, 0, len(conns))
	for _, c := range conns {
		if h.nodes[c] != nil && !h.tombstones.Has(c) {
			validConns = append(validConns, c)
		}
	}

	if len(validConns) < len(conns) {
		node.Lock()
		node.setNeighborsAtLevel(level, validConns)
		node.Unlock()
	}
}

func (h *HNSW) repairAllConnections() int {
	repaired := 0
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		for level := 0; level <= node.level; level++ {
			before := len(node.neighborsAtLevel(level))
			h.repairConnections(id, level)
			after := len(node.neighborsAtLevel(level))
			if after < before {
				repaired += before - after
			}
		}
	}
	return repaired
}

type connectivityStats struct {
	TotalNodes      int
	TotalEdges      int
	OrphanNodes     int
	AvgDegree       float64
	MaxDegree       int
	MinDegree       int
	LevelStats      map[int]levelStat
}

type levelStat struct {
	Nodes     int
	Edges     int
	AvgDegree float64
	MaxDegree int
}

func (h *HNSW) ConnectivityStats() connectivityStats {
	h.mu.RLock()
	defer h.mu.RUnlock()

	stats := connectivityStats{
		LevelStats: make(map[int]levelStat),
		MinDegree:  int(^uint(0) >> 1),
	}

	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		stats.TotalNodes++

		totalDegree := 0
		isOrphan := true

		for level := 0; level <= node.level; level++ {
			conns := node.neighborsAtLevel(level)
			degree := len(conns)
			totalDegree += degree

			if degree > 0 {
				isOrphan = false
			}

			ls := stats.LevelStats[level]
			ls.Nodes++
			ls.Edges += degree
			if degree > ls.MaxDegree {
				ls.MaxDegree = degree
			}
			stats.LevelStats[level] = ls
		}

		stats.TotalEdges += totalDegree
		if totalDegree > stats.MaxDegree {
			stats.MaxDegree = totalDegree
		}
		if totalDegree < stats.MinDegree {
			stats.MinDegree = totalDegree
		}
		if isOrphan {
			stats.OrphanNodes++
		}
	}

	if stats.TotalNodes > 0 {
		stats.AvgDegree = float64(stats.TotalEdges) / float64(stats.TotalNodes)
	}
	for level, ls := range stats.LevelStats {
		if ls.Nodes > 0 {
			ls.AvgDegree = float64(ls.Edges) / float64(ls.Nodes)
		}
		stats.LevelStats[level] = ls
	}
	if stats.TotalNodes == 0 {
		stats.MinDegree = 0
	}

	return stats
}

var neighborBufPool = sync.Pool{
	New: func() interface{} {
		s := make([]uint64, 0, 64)
		return &s
	},
}

func getNeighborBuf() []uint64 {
	p := neighborBufPool.Get().(*[]uint64)
	return (*p)[:0]
}

func putNeighborBuf(buf []uint64) {
	neighborBufPool.Put(&buf)
}
