package compact

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type WALIndex struct {
	mu         sync.RWMutex
	nodes      map[uint64]*WALNodeState
	globals    []Commit
	tombstones map[uint64]int64
	entryPoint uint64
	maxLevel   int
	ef         int
	seqNo      uint64
	commitCount int
	buildTime  time.Duration
}

type WALNodeState struct {
	NodeID      uint64
	Level       int
	Connections map[int][]uint64
	FirstSeen   uint64
	LastSeen    uint64
	Deleted     bool
	CommitCount int
}

func NewWALIndex() *WALIndex {
	return &WALIndex{
		nodes:      make(map[uint64]*WALNodeState),
		tombstones: make(map[uint64]int64),
		maxLevel:   -1,
	}
}

func BuildWALIndex(dir string) (*WALIndex, error) {
	start := time.Now()
	idx := NewWALIndex()
	loader := NewLoader(LoaderConfig{Dir: dir})
	result, err := loader.Load()
	if err != nil {
		return nil, err
	}
	idx.entryPoint = result.EntryPoint
	idx.maxLevel = result.MaxLevel
	idx.ef = result.Ef
	for _, gc := range result.GlobalCommits {
		idx.globals = append(idx.globals, gc)
	}
	for id, nc := range result.NodeCommits {
		state := &WALNodeState{
			NodeID:      id,
			Connections: make(map[int][]uint64),
			CommitCount: len(nc.Commits),
		}
		for _, c := range nc.Commits {
			idx.seqNo++
			switch c.Type {
			case CommitAddNode:
				state.Level = 0
				if state.FirstSeen == 0 {
					state.FirstSeen = idx.seqNo
				}
			case CommitAddNodeLevel:
				state.Level = c.Level
				if state.FirstSeen == 0 {
					state.FirstSeen = idx.seqNo
				}
			case CommitAddLink:
				conns := state.Connections[c.Level]
				found := false
				for _, existing := range conns {
					if existing == c.Target {
						found = true
						break
					}
				}
				if !found {
					state.Connections[c.Level] = append(conns, c.Target)
				}
			case CommitReplaceLinks:
				state.Connections[c.Level] = make([]uint64, len(c.Links))
				copy(state.Connections[c.Level], c.Links)
			case CommitClearLinks:
				state.Connections[c.Level] = nil
			case CommitDeleteNode:
				state.Deleted = true
			}
			state.LastSeen = idx.seqNo
		}
		idx.nodes[id] = state
		idx.commitCount += len(nc.Commits)
	}
	for id := range result.Tombstones {
		idx.tombstones[id] = int64(idx.seqNo)
	}
	idx.buildTime = time.Since(start)
	return idx, nil
}

func (wi *WALIndex) GetNode(nodeID uint64) *WALNodeState {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	return wi.nodes[nodeID]
}

func (wi *WALIndex) HasNode(nodeID uint64) bool {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	_, ok := wi.nodes[nodeID]
	return ok
}

func (wi *WALIndex) ActiveNodes() []uint64 {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	var ids []uint64
	for id, state := range wi.nodes {
		if !state.Deleted && !wi.isTombstoned(id) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (wi *WALIndex) DeletedNodes() []uint64 {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	var ids []uint64
	for id, state := range wi.nodes {
		if state.Deleted || wi.isTombstoned(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (wi *WALIndex) isTombstoned(id uint64) bool {
	_, ok := wi.tombstones[id]
	return ok
}

func (wi *WALIndex) NodesByLevel(level int) []uint64 {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	var ids []uint64
	for id, state := range wi.nodes {
		if state.Level == level && !state.Deleted {
			ids = append(ids, id)
		}
	}
	return ids
}

func (wi *WALIndex) Neighbors(nodeID uint64, level int) []uint64 {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	state := wi.nodes[nodeID]
	if state == nil {
		return nil
	}
	return state.Connections[level]
}

func (wi *WALIndex) AllNeighbors(nodeID uint64) map[int][]uint64 {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	state := wi.nodes[nodeID]
	if state == nil {
		return nil
	}
	result := make(map[int][]uint64)
	for level, conns := range state.Connections {
		result[level] = append([]uint64{}, conns...)
	}
	return result
}

func (wi *WALIndex) TotalEdges() int {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	total := 0
	for _, state := range wi.nodes {
		for _, conns := range state.Connections {
			total += len(conns)
		}
	}
	return total
}

func (wi *WALIndex) LevelDistribution() map[int]int {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	dist := make(map[int]int)
	for _, state := range wi.nodes {
		if !state.Deleted {
			dist[state.Level]++
		}
	}
	return dist
}

func (wi *WALIndex) FindDanglingLinks() []DanglingLink {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	var dangling []DanglingLink
	for id, state := range wi.nodes {
		if state.Deleted {
			continue
		}
		for level, conns := range state.Connections {
			for _, target := range conns {
				targetState := wi.nodes[target]
				if targetState == nil || targetState.Deleted || wi.isTombstoned(target) {
					dangling = append(dangling, DanglingLink{
						FromID:   id,
						ToID:     target,
						Level:    level,
						TargetDeleted: targetState != nil && targetState.Deleted,
						TargetMissing: targetState == nil,
					})
				}
			}
		}
	}
	return dangling
}

type DanglingLink struct {
	FromID        uint64
	ToID          uint64
	Level         int
	TargetDeleted bool
	TargetMissing bool
}

func (dl DanglingLink) String() string {
	reason := "missing"
	if dl.TargetDeleted {
		reason = "deleted"
	}
	return fmt.Sprintf("%d → %d (level %d, target %s)", dl.FromID, dl.ToID, dl.Level, reason)
}

func (wi *WALIndex) FindOrphanNodes() []uint64 {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	referenced := make(map[uint64]bool)
	referenced[wi.entryPoint] = true
	for _, state := range wi.nodes {
		for _, conns := range state.Connections {
			for _, c := range conns {
				referenced[c] = true
			}
		}
	}
	var orphans []uint64
	for id, state := range wi.nodes {
		if state.Deleted || wi.isTombstoned(id) {
			continue
		}
		if !referenced[id] && id != wi.entryPoint {
			orphans = append(orphans, id)
		}
	}
	return orphans
}

func (wi *WALIndex) CheckBidirectional() []string {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	var issues []string
	for id, state := range wi.nodes {
		if state.Deleted {
			continue
		}
		for level, conns := range state.Connections {
			for _, target := range conns {
				targetState := wi.nodes[target]
				if targetState == nil || targetState.Deleted {
					continue
				}
				reverseConns := targetState.Connections[level]
				found := false
				for _, rc := range reverseConns {
					if rc == id {
						found = true
						break
					}
				}
				if !found {
					issues = append(issues, fmt.Sprintf("unidirectional: %d → %d at level %d", id, target, level))
				}
			}
		}
	}
	return issues
}

func (wi *WALIndex) ExportToCommits() []Commit {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	var commits []Commit
	if wi.maxLevel >= 0 {
		commits = append(commits, Commit{Type: CommitSetMaxLevel, Level: wi.maxLevel})
	}
	if wi.entryPoint > 0 {
		commits = append(commits, Commit{Type: CommitSetEntryPoint, NodeID: wi.entryPoint})
	}
	if wi.ef > 0 {
		commits = append(commits, Commit{Type: CommitSetEf, Value: uint64(wi.ef)})
	}
	nodeIDs := make([]uint64, 0, len(wi.nodes))
	for id := range wi.nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
	for _, id := range nodeIDs {
		state := wi.nodes[id]
		if state.Deleted {
			commits = append(commits, Commit{Type: CommitDeleteNode, NodeID: id})
			continue
		}
		commits = append(commits, Commit{Type: CommitAddNodeLevel, NodeID: id, Level: state.Level})
		levels := make([]int, 0, len(state.Connections))
		for l := range state.Connections {
			levels = append(levels, l)
		}
		sort.Ints(levels)
		for _, level := range levels {
			conns := state.Connections[level]
			if len(conns) > 0 {
				commits = append(commits, Commit{
					Type: CommitReplaceLinks, NodeID: id, Level: level, Links: conns,
				})
			}
		}
	}
	for id := range wi.tombstones {
		commits = append(commits, Commit{Type: CommitAddTombstone, NodeID: id})
	}
	return commits
}

func (wi *WALIndex) Summary() string {
	wi.mu.RLock()
	defer wi.mu.RUnlock()
	active := 0
	deleted := 0
	for _, state := range wi.nodes {
		if state.Deleted {
			deleted++
		} else {
			active++
		}
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("WALIndex:\n"))
	sb.WriteString(fmt.Sprintf("  Nodes: %d active, %d deleted, %d tombstoned\n", active, deleted, len(wi.tombstones)))
	sb.WriteString(fmt.Sprintf("  Edges: %d total\n", wi.TotalEdges()))
	sb.WriteString(fmt.Sprintf("  Entry: %d, MaxLevel: %d, Ef: %d\n", wi.entryPoint, wi.maxLevel, wi.ef))
	sb.WriteString(fmt.Sprintf("  Commits: %d, SeqNo: %d\n", wi.commitCount, wi.seqNo))
	sb.WriteString(fmt.Sprintf("  Build time: %v\n", wi.buildTime))
	return sb.String()
}
