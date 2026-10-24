package compact

import (
	"fmt"
	"strings"
)

type DiagnosticResult struct {
	Valid         bool
	TotalCommits int
	Corrupted    int
	Nodes        int
	Tombstones   int
	Issues       []string
	FileStats    map[string]FileDiag
}

type FileDiag struct {
	Path      string
	Size      int64
	Commits   int
	Corrupted int
	Valid     bool
}

func DiagnoseDirectory(dir string) (*DiagnosticResult, error) {
	discovery := NewFileDiscovery(dir)
	state, err := discovery.Scan()
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	result := &DiagnosticResult{
		Valid:     true,
		FileStats: make(map[string]FileDiag),
	}

	allFiles := make([]FileInfo, 0)
	allFiles = append(allFiles, state.WALFiles...)
	allFiles = append(allFiles, state.SortedFiles...)
	allFiles = append(allFiles, state.SnapshotFiles...)

	for _, f := range allFiles {
		fd := FileDiag{
			Path:  f.Path,
			Size:  f.Size,
			Valid: true,
		}

		valid, corrupted, err := ValidateWAL(f.Path)
		if err != nil {
			fd.Valid = false
			result.Issues = append(result.Issues, fmt.Sprintf("file %s: %v", f.Path, err))
			result.Valid = false
		}
		fd.Commits = valid
		fd.Corrupted = corrupted
		result.TotalCommits += valid
		result.Corrupted += corrupted

		if corrupted > 0 {
			result.Issues = append(result.Issues, fmt.Sprintf("file %s: %d corrupted records", f.Path, corrupted))
		}

		result.FileStats[f.Path] = fd
	}

	if len(state.Overlaps) > 0 {
		for _, o := range state.Overlaps {
			result.Issues = append(result.Issues,
				fmt.Sprintf("overlap: %s [%d-%d] ↔ %s [%d-%d]",
					o.FileA.Path, o.FileA.StartTS, o.FileA.EndTS,
					o.FileB.Path, o.FileB.StartTS, o.FileB.EndTS))
		}
	}

	loader := NewLoader(LoaderConfig{Dir: dir})
	loadResult, err := loader.Load()
	if err != nil {
		result.Issues = append(result.Issues, fmt.Sprintf("load error: %v", err))
		result.Valid = false
	} else {
		result.Nodes = loadResult.NodeCount()
		result.Tombstones = len(loadResult.Tombstones)

		for id := range loadResult.Tombstones {
			if _, ok := loadResult.NodeCommits[id]; !ok {
				result.Issues = append(result.Issues,
					fmt.Sprintf("tombstone %d references non-existent node", id))
			}
		}

		for id, nc := range loadResult.NodeCommits {
			hasAdd := false
			hasDelete := false
			for _, c := range nc.Commits {
				if c.Type == CommitAddNode || c.Type == CommitAddNodeLevel {
					hasAdd = true
				}
				if c.Type == CommitDeleteNode {
					hasDelete = true
				}
			}
			if !hasAdd && !loadResult.Tombstones[id] {
				result.Issues = append(result.Issues,
					fmt.Sprintf("node %d has commits but no AddNode", id))
			}
			if hasDelete && !loadResult.Tombstones[id] {
				result.Issues = append(result.Issues,
					fmt.Sprintf("node %d was deleted but not tombstoned", id))
			}
		}
	}

	if len(result.Issues) > 0 {
		result.Valid = false
	}

	return result, nil
}

func (d *DiagnosticResult) Summary() string {
	var sb strings.Builder
	status := "HEALTHY"
	if !d.Valid {
		status = "ISSUES FOUND"
	}

	sb.WriteString(fmt.Sprintf("Diagnostic: %s\n", status))
	sb.WriteString(fmt.Sprintf("  Files: %d\n", len(d.FileStats)))
	sb.WriteString(fmt.Sprintf("  Total commits: %d\n", d.TotalCommits))
	sb.WriteString(fmt.Sprintf("  Corrupted: %d\n", d.Corrupted))
	sb.WriteString(fmt.Sprintf("  Nodes: %d\n", d.Nodes))
	sb.WriteString(fmt.Sprintf("  Tombstones: %d\n", d.Tombstones))

	if len(d.Issues) > 0 {
		sb.WriteString(fmt.Sprintf("  Issues (%d):\n", len(d.Issues)))
		for _, issue := range d.Issues {
			sb.WriteString(fmt.Sprintf("    - %s\n", issue))
		}
	}

	return sb.String()
}

type TombstoneDiff struct {
	InIndex     []uint64
	InLog       []uint64
	OnlyInIndex []uint64
	OnlyInLog   []uint64
	Consistent  bool
}

func DiagnoseTombstoneDiff(indexTombstones, logTombstones []uint64) *TombstoneDiff {
	diff := &TombstoneDiff{
		InIndex: indexTombstones,
		InLog:   logTombstones,
	}

	indexSet := make(map[uint64]bool)
	for _, id := range indexTombstones {
		indexSet[id] = true
	}

	logSet := make(map[uint64]bool)
	for _, id := range logTombstones {
		logSet[id] = true
	}

	for _, id := range indexTombstones {
		if !logSet[id] {
			diff.OnlyInIndex = append(diff.OnlyInIndex, id)
		}
	}

	for _, id := range logTombstones {
		if !indexSet[id] {
			diff.OnlyInLog = append(diff.OnlyInLog, id)
		}
	}

	diff.Consistent = len(diff.OnlyInIndex) == 0 && len(diff.OnlyInLog) == 0
	return diff
}

func (d *TombstoneDiff) Summary() string {
	if d.Consistent {
		return fmt.Sprintf("Tombstones consistent: %d entries", len(d.InIndex))
	}
	return fmt.Sprintf("Tombstone MISMATCH: index=%d log=%d onlyIndex=%d onlyLog=%d",
		len(d.InIndex), len(d.InLog), len(d.OnlyInIndex), len(d.OnlyInLog))
}

type CompactionHealth struct {
	WALCount         int
	SortedCount      int
	SnapshotCount    int
	TotalSizeBytes   int64
	WALSizeBytes     int64
	SortedSizeBytes  int64
	OverlapCount     int
	NeedsCompaction  bool
	RecommendedAction Action
}

func CheckCompactionHealth(dir string, cfg CompactorConfig) (*CompactionHealth, error) {
	discovery := NewFileDiscovery(dir)
	state, err := discovery.Scan()
	if err != nil {
		return nil, err
	}

	health := &CompactionHealth{
		WALCount:        len(state.WALFiles),
		SortedCount:     len(state.SortedFiles),
		SnapshotCount:   len(state.SnapshotFiles),
		TotalSizeBytes:  state.TotalSize(),
		WALSizeBytes:    state.TotalWALSize(),
		SortedSizeBytes: state.TotalSortedSize(),
		OverlapCount:    len(state.Overlaps),
	}

	if health.OverlapCount > 0 {
		health.NeedsCompaction = true
		health.RecommendedAction = ActionCleanup
	} else if health.WALSizeBytes > cfg.MaxWALSize {
		health.NeedsCompaction = true
		health.RecommendedAction = ActionConvertToSort
	} else if health.SortedCount > cfg.MaxSortedFiles {
		health.NeedsCompaction = true
		health.RecommendedAction = ActionMergeSorted
	} else if health.TotalSizeBytes > cfg.SnapshotThreshold {
		health.NeedsCompaction = true
		health.RecommendedAction = ActionCreateSnapshot
	}

	return health, nil
}

func (h *CompactionHealth) Summary() string {
	status := "OK"
	if h.NeedsCompaction {
		status = fmt.Sprintf("NEEDS %s", h.RecommendedAction)
	}

	return fmt.Sprintf("Compaction: %s (wal=%d sorted=%d snap=%d size=%dKB overlaps=%d)",
		status, h.WALCount, h.SortedCount, h.SnapshotCount,
		h.TotalSizeBytes/1024, h.OverlapCount)
}
