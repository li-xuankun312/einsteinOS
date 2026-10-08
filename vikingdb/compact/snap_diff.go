package compact

import (
	"fmt"
	"strings"
)

type SnapshotDiff struct {
	BaseFile    string
	TargetFile  string
	AddedNodes  []uint64
	RemovedNodes []uint64
	ModifiedNodes []uint64
	AddedTombstones []uint64
	RemovedTombstones []uint64
	GlobalChanges []string
	BaseNodeCount   int
	TargetNodeCount int
}

func DiffSnapshots(basePath, targetPath string) (*SnapshotDiff, error) {
	baseIdx, err := LoadInMemory(basePath)
	if err != nil {
		return nil, fmt.Errorf("load base: %w", err)
	}

	targetIdx, err := LoadInMemory(targetPath)
	if err != nil {
		return nil, fmt.Errorf("load target: %w", err)
	}

	diff := &SnapshotDiff{
		BaseFile:        basePath,
		TargetFile:      targetPath,
		BaseNodeCount:   len(baseIdx.Nodes),
		TargetNodeCount: len(targetIdx.Nodes),
	}

	for id := range targetIdx.Nodes {
		if _, ok := baseIdx.Nodes[id]; !ok {
			diff.AddedNodes = append(diff.AddedNodes, id)
		}
	}

	for id := range baseIdx.Nodes {
		if _, ok := targetIdx.Nodes[id]; !ok {
			diff.RemovedNodes = append(diff.RemovedNodes, id)
		}
	}

	for id, targetNode := range targetIdx.Nodes {
		baseNode, ok := baseIdx.Nodes[id]
		if !ok {
			continue
		}
		if nodesModified(baseNode, targetNode) {
			diff.ModifiedNodes = append(diff.ModifiedNodes, id)
		}
	}

	for id := range targetIdx.Tombstones {
		if !baseIdx.Tombstones[id] {
			diff.AddedTombstones = append(diff.AddedTombstones, id)
		}
	}
	for id := range baseIdx.Tombstones {
		if !targetIdx.Tombstones[id] {
			diff.RemovedTombstones = append(diff.RemovedTombstones, id)
		}
	}

	if baseIdx.EntryPoint != targetIdx.EntryPoint {
		diff.GlobalChanges = append(diff.GlobalChanges,
			fmt.Sprintf("entry point: %d → %d", baseIdx.EntryPoint, targetIdx.EntryPoint))
	}
	if baseIdx.MaxLevel != targetIdx.MaxLevel {
		diff.GlobalChanges = append(diff.GlobalChanges,
			fmt.Sprintf("max level: %d → %d", baseIdx.MaxLevel, targetIdx.MaxLevel))
	}
	if baseIdx.Ef != targetIdx.Ef {
		diff.GlobalChanges = append(diff.GlobalChanges,
			fmt.Sprintf("ef: %d → %d", baseIdx.Ef, targetIdx.Ef))
	}

	return diff, nil
}

func nodesModified(a, b *InMemoryNode) bool {
	if a.Level != b.Level {
		return true
	}

	allLevels := make(map[int]bool)
	for l := range a.Connections {
		allLevels[l] = true
	}
	for l := range b.Connections {
		allLevels[l] = true
	}

	for level := range allLevels {
		connsA := a.Connections[level]
		connsB := b.Connections[level]
		if len(connsA) != len(connsB) {
			return true
		}
		setA := make(map[uint64]bool, len(connsA))
		for _, c := range connsA {
			setA[c] = true
		}
		for _, c := range connsB {
			if !setA[c] {
				return true
			}
		}
	}
	return false
}

func (d *SnapshotDiff) HasChanges() bool {
	return len(d.AddedNodes) > 0 || len(d.RemovedNodes) > 0 ||
		len(d.ModifiedNodes) > 0 || len(d.AddedTombstones) > 0 ||
		len(d.RemovedTombstones) > 0 || len(d.GlobalChanges) > 0
}

func (d *SnapshotDiff) TotalChanges() int {
	return len(d.AddedNodes) + len(d.RemovedNodes) + len(d.ModifiedNodes) +
		len(d.AddedTombstones) + len(d.RemovedTombstones) + len(d.GlobalChanges)
}

func (d *SnapshotDiff) ChangeRatio() float64 {
	if d.BaseNodeCount == 0 && d.TargetNodeCount == 0 {
		return 0
	}
	maxNodes := d.BaseNodeCount
	if d.TargetNodeCount > maxNodes {
		maxNodes = d.TargetNodeCount
	}
	return float64(d.TotalChanges()) / float64(maxNodes)
}

func (d *SnapshotDiff) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Snapshot Diff:\n"))
	sb.WriteString(fmt.Sprintf("  Base:   %s (%d nodes)\n", d.BaseFile, d.BaseNodeCount))
	sb.WriteString(fmt.Sprintf("  Target: %s (%d nodes)\n", d.TargetFile, d.TargetNodeCount))
	sb.WriteString(fmt.Sprintf("  Added:    %d nodes\n", len(d.AddedNodes)))
	sb.WriteString(fmt.Sprintf("  Removed:  %d nodes\n", len(d.RemovedNodes)))
	sb.WriteString(fmt.Sprintf("  Modified: %d nodes\n", len(d.ModifiedNodes)))
	sb.WriteString(fmt.Sprintf("  Tombstones: +%d -%d\n", len(d.AddedTombstones), len(d.RemovedTombstones)))

	if len(d.GlobalChanges) > 0 {
		sb.WriteString("  Globals:\n")
		for _, g := range d.GlobalChanges {
			sb.WriteString(fmt.Sprintf("    %s\n", g))
		}
	}

	ratio := d.ChangeRatio()
	sb.WriteString(fmt.Sprintf("  Change ratio: %.1f%%\n", ratio*100))
	return sb.String()
}

type IncrementalDelta struct {
	AddedCommits   []Commit
	ModifiedCommits []Commit
	DeleteCommits  []Commit
	GlobalCommits  []Commit
}

func DiffToIncrementalDelta(diff *SnapshotDiff, targetIdx *InMemoryIndex) *IncrementalDelta {
	delta := &IncrementalDelta{}

	for _, id := range diff.AddedNodes {
		node := targetIdx.Nodes[id]
		if node == nil {
			continue
		}
		delta.AddedCommits = append(delta.AddedCommits, Commit{
			Type:   CommitAddNodeLevel,
			NodeID: id,
			Level:  node.Level,
		})
		for level, conns := range node.Connections {
			if len(conns) > 0 {
				delta.AddedCommits = append(delta.AddedCommits, Commit{
					Type:   CommitReplaceLinks,
					NodeID: id,
					Level:  level,
					Links:  conns,
				})
			}
		}
	}

	for _, id := range diff.ModifiedNodes {
		node := targetIdx.Nodes[id]
		if node == nil {
			continue
		}
		for level, conns := range node.Connections {
			delta.ModifiedCommits = append(delta.ModifiedCommits, Commit{
				Type:   CommitReplaceLinks,
				NodeID: id,
				Level:  level,
				Links:  conns,
			})
		}
	}

	for _, id := range diff.RemovedNodes {
		delta.DeleteCommits = append(delta.DeleteCommits, Commit{
			Type:   CommitDeleteNode,
			NodeID: id,
		})
	}

	for _, id := range diff.AddedTombstones {
		delta.GlobalCommits = append(delta.GlobalCommits, Commit{
			Type:   CommitAddTombstone,
			NodeID: id,
		})
	}
	for _, id := range diff.RemovedTombstones {
		delta.GlobalCommits = append(delta.GlobalCommits, Commit{
			Type:   CommitRemoveTombstone,
			NodeID: id,
		})
	}

	return delta
}

func (d *IncrementalDelta) AllCommits() []Commit {
	var all []Commit
	all = append(all, d.GlobalCommits...)
	all = append(all, d.AddedCommits...)
	all = append(all, d.ModifiedCommits...)
	all = append(all, d.DeleteCommits...)
	return all
}

func (d *IncrementalDelta) CommitCount() int {
	return len(d.AddedCommits) + len(d.ModifiedCommits) +
		len(d.DeleteCommits) + len(d.GlobalCommits)
}

func (d *IncrementalDelta) Summary() string {
	return fmt.Sprintf("Delta{added=%d modified=%d deleted=%d global=%d total=%d}",
		len(d.AddedCommits), len(d.ModifiedCommits),
		len(d.DeleteCommits), len(d.GlobalCommits), d.CommitCount())
}
