package compact

import (
	"fmt"
	"sort"
)

type InMemoryIndex struct {
	Nodes       map[uint64]*InMemoryNode
	EntryPoint  uint64
	MaxLevel    int
	Tombstones  map[uint64]bool
	Ef          int
	WasReset    bool
}

type InMemoryNode struct {
	ID          uint64
	Level       int
	Connections map[int][]uint64
}

func NewInMemoryIndex() *InMemoryIndex {
	return &InMemoryIndex{
		Nodes:      make(map[uint64]*InMemoryNode),
		Tombstones: make(map[uint64]bool),
		MaxLevel:   -1,
	}
}

func (idx *InMemoryIndex) Apply(c Commit) {
	switch c.Type {
	case CommitAddNode:
		idx.Nodes[c.NodeID] = &InMemoryNode{
			ID:          c.NodeID,
			Level:       0,
			Connections: make(map[int][]uint64),
		}

	case CommitAddNodeLevel:
		idx.Nodes[c.NodeID] = &InMemoryNode{
			ID:          c.NodeID,
			Level:       c.Level,
			Connections: make(map[int][]uint64),
		}

	case CommitSetEntryPoint:
		idx.EntryPoint = c.NodeID

	case CommitAddLink:
		node := idx.Nodes[c.NodeID]
		if node != nil {
			conns := node.Connections[c.Level]
			for _, existing := range conns {
				if existing == c.Target {
					return
				}
			}
			node.Connections[c.Level] = append(conns, c.Target)
		}

	case CommitReplaceLinks:
		node := idx.Nodes[c.NodeID]
		if node != nil {
			node.Connections[c.Level] = make([]uint64, len(c.Links))
			copy(node.Connections[c.Level], c.Links)
		}

	case CommitDeleteNode:
		delete(idx.Nodes, c.NodeID)
		idx.Tombstones[c.NodeID] = true

	case CommitClearLinks:
		node := idx.Nodes[c.NodeID]
		if node != nil {
			node.Connections[c.Level] = nil
		}

	case CommitSetMaxLevel:
		idx.MaxLevel = c.Level

	case CommitAddTombstone:
		idx.Tombstones[c.NodeID] = true

	case CommitRemoveTombstone:
		delete(idx.Tombstones, c.NodeID)

	case CommitResetIndex:
		idx.Nodes = make(map[uint64]*InMemoryNode)
		idx.Tombstones = make(map[uint64]bool)
		idx.MaxLevel = -1
		idx.EntryPoint = 0
		idx.WasReset = true

	case CommitSetEf:
		idx.Ef = int(c.Value)
	}
}

func (idx *InMemoryIndex) ApplyAll(commits []Commit) {
	for _, c := range commits {
		idx.Apply(c)
	}
}

func (idx *InMemoryIndex) ActiveNodeCount() int {
	count := 0
	for id := range idx.Nodes {
		if !idx.Tombstones[id] {
			count++
		}
	}
	return count
}

func (idx *InMemoryIndex) TotalEdges() int {
	total := 0
	for _, node := range idx.Nodes {
		for _, conns := range node.Connections {
			total += len(conns)
		}
	}
	return total
}

func (idx *InMemoryIndex) LevelDistribution() map[int]int {
	dist := make(map[int]int)
	for id, node := range idx.Nodes {
		if !idx.Tombstones[id] {
			dist[node.Level]++
		}
	}
	return dist
}

func (idx *InMemoryIndex) Validate() []string {
	var issues []string

	if idx.MaxLevel >= 0 {
		if _, ok := idx.Nodes[idx.EntryPoint]; !ok {
			if !idx.Tombstones[idx.EntryPoint] {
				issues = append(issues, fmt.Sprintf("entry point %d not in nodes", idx.EntryPoint))
			}
		}
	}

	for id, node := range idx.Nodes {
		if idx.Tombstones[id] {
			continue
		}
		for level, conns := range node.Connections {
			seen := make(map[uint64]bool)
			for _, c := range conns {
				if c == id {
					issues = append(issues, fmt.Sprintf("node %d: self-link at level %d", id, level))
				}
				if seen[c] {
					issues = append(issues, fmt.Sprintf("node %d: duplicate link to %d at level %d", id, c, level))
				}
				seen[c] = true
				if _, ok := idx.Nodes[c]; !ok && !idx.Tombstones[c] {
					issues = append(issues, fmt.Sprintf("node %d: dangling link to %d at level %d", id, c, level))
				}
			}
		}
	}

	return issues
}

func (idx *InMemoryIndex) ExportCommits() []Commit {
	var commits []Commit

	if idx.MaxLevel >= 0 {
		commits = append(commits, Commit{Type: CommitSetMaxLevel, Level: idx.MaxLevel})
		commits = append(commits, Commit{Type: CommitSetEntryPoint, NodeID: idx.EntryPoint})
	}
	if idx.Ef > 0 {
		commits = append(commits, Commit{Type: CommitSetEf, Value: uint64(idx.Ef)})
	}

	nodeIDs := make([]uint64, 0, len(idx.Nodes))
	for id := range idx.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

	for _, id := range nodeIDs {
		node := idx.Nodes[id]
		commits = append(commits, Commit{
			Type:   CommitAddNodeLevel,
			NodeID: id,
			Level:  node.Level,
		})

		levels := make([]int, 0, len(node.Connections))
		for l := range node.Connections {
			levels = append(levels, l)
		}
		sort.Ints(levels)

		for _, level := range levels {
			conns := node.Connections[level]
			if len(conns) > 0 {
				commits = append(commits, Commit{
					Type:   CommitReplaceLinks,
					NodeID: id,
					Level:  level,
					Links:  conns,
				})
			}
		}
	}

	for id := range idx.Tombstones {
		commits = append(commits, Commit{
			Type:   CommitAddTombstone,
			NodeID: id,
		})
	}

	return commits
}

func (idx *InMemoryIndex) WriteTo(path string) error {
	commits := idx.ExportCommits()
	globals, byNode := SplitByNode(commits)

	nodeIDs := make([]uint64, 0, len(byNode))
	for id := range byNode {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

	nodes := make([]*NodeCommits, len(nodeIDs))
	for i, id := range nodeIDs {
		nodes[i] = byNode[id]
	}

	return WriteSortedFile(path, globals, nodes)
}

func LoadInMemory(dir string) (*InMemoryIndex, error) {
	loader := NewLoader(LoaderConfig{Dir: dir})
	result, err := loader.Load()
	if err != nil {
		return nil, err
	}

	idx := NewInMemoryIndex()
	idx.EntryPoint = result.EntryPoint
	idx.MaxLevel = result.MaxLevel
	idx.Ef = result.Ef
	idx.WasReset = result.WasReset

	for id, nc := range result.NodeCommits {
		node := &InMemoryNode{
			ID:          id,
			Connections: make(map[int][]uint64),
		}

		for _, c := range nc.Commits {
			switch c.Type {
			case CommitAddNode:
				node.Level = 0
			case CommitAddNodeLevel:
				node.Level = c.Level
			case CommitAddLink:
				node.Connections[c.Level] = append(node.Connections[c.Level], c.Target)
			case CommitReplaceLinks:
				node.Connections[c.Level] = c.Links
			case CommitClearLinks:
				node.Connections[c.Level] = nil
			}
		}

		idx.Nodes[id] = node
	}

	for id := range result.Tombstones {
		idx.Tombstones[id] = true
	}

	return idx, nil
}

func CompareIndices(a, b *InMemoryIndex) []string {
	var diffs []string

	if a.EntryPoint != b.EntryPoint {
		diffs = append(diffs, fmt.Sprintf("entry point: %d vs %d", a.EntryPoint, b.EntryPoint))
	}
	if a.MaxLevel != b.MaxLevel {
		diffs = append(diffs, fmt.Sprintf("max level: %d vs %d", a.MaxLevel, b.MaxLevel))
	}

	allNodes := make(map[uint64]bool)
	for id := range a.Nodes {
		allNodes[id] = true
	}
	for id := range b.Nodes {
		allNodes[id] = true
	}

	for id := range allNodes {
		nodeA, inA := a.Nodes[id]
		nodeB, inB := b.Nodes[id]

		if inA && !inB {
			diffs = append(diffs, fmt.Sprintf("node %d: only in A", id))
			continue
		}
		if !inA && inB {
			diffs = append(diffs, fmt.Sprintf("node %d: only in B", id))
			continue
		}

		if nodeA.Level != nodeB.Level {
			diffs = append(diffs, fmt.Sprintf("node %d: level %d vs %d", id, nodeA.Level, nodeB.Level))
		}

		allLevels := make(map[int]bool)
		for l := range nodeA.Connections {
			allLevels[l] = true
		}
		for l := range nodeB.Connections {
			allLevels[l] = true
		}

		for level := range allLevels {
			connsA := nodeA.Connections[level]
			connsB := nodeB.Connections[level]
			if len(connsA) != len(connsB) {
				diffs = append(diffs, fmt.Sprintf("node %d L%d: %d vs %d connections",
					id, level, len(connsA), len(connsB)))
			}
		}
	}

	tombA := len(a.Tombstones)
	tombB := len(b.Tombstones)
	if tombA != tombB {
		diffs = append(diffs, fmt.Sprintf("tombstones: %d vs %d", tombA, tombB))
	}

	return diffs
}
