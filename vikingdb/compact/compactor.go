package compact

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Action int

const (
	ActionNone           Action = 0
	ActionConvertToSort  Action = 1
	ActionMergeSorted    Action = 2
	ActionCreateSnapshot Action = 3
	ActionCleanup        Action = 4
)

func (a Action) String() string {
	switch a {
	case ActionNone:
		return "none"
	case ActionConvertToSort:
		return "convert_to_sorted"
	case ActionMergeSorted:
		return "merge_sorted"
	case ActionCreateSnapshot:
		return "create_snapshot"
	case ActionCleanup:
		return "cleanup"
	default:
		return fmt.Sprintf("unknown(%d)", a)
	}
}

type CompactorConfig struct {
	Dir                string
	IndexID            string
	MaxWALSize         int64
	MaxSortedFiles     int
	SnapshotThreshold  int64
	MinCompactInterval time.Duration
}

func DefaultCompactorConfig(dir string) CompactorConfig {
	return CompactorConfig{
		Dir:                dir,
		IndexID:            "hnsw",
		MaxWALSize:         32 * 1024 * 1024,
		MaxSortedFiles:     8,
		SnapshotThreshold:  128 * 1024 * 1024,
		MinCompactInterval: 30 * time.Second,
	}
}

type Compactor struct {
	cfg           CompactorConfig
	discovery     *FileDiscovery
	lastCompact   time.Time
	cycleCount    int
	totalMerged   int
	totalCleaned  int
}

func NewCompactor(cfg CompactorConfig) *Compactor {
	return &Compactor{
		cfg:       cfg,
		discovery: NewFileDiscovery(cfg.Dir),
	}
}

func (c *Compactor) RunCycle(shouldAbort func() bool) (Action, error) {
	if time.Since(c.lastCompact) < c.cfg.MinCompactInterval {
		return ActionNone, nil
	}

	state, err := c.discovery.Scan()
	if err != nil {
		return ActionNone, fmt.Errorf("scan: %w", err)
	}

	if shouldAbort != nil && shouldAbort() {
		return ActionNone, nil
	}

	action := c.decideAction(state)
	if action == ActionNone {
		return ActionNone, nil
	}

	var actionErr error
	switch action {
	case ActionConvertToSort:
		actionErr = c.convertToSorted(state, shouldAbort)
	case ActionMergeSorted:
		actionErr = c.mergeSorted(state, shouldAbort)
	case ActionCreateSnapshot:
		actionErr = c.createSnapshot(state, shouldAbort)
	case ActionCleanup:
		actionErr = c.cleanup(state)
	}

	c.lastCompact = time.Now()
	c.cycleCount++

	if actionErr != nil {
		return action, fmt.Errorf("action %s: %w", action, actionErr)
	}

	log.Printf("[compact] cycle %d: %s completed", c.cycleCount, action)
	return action, nil
}

func (c *Compactor) decideAction(state *DirectoryState) Action {
	if len(state.Overlaps) > 0 {
		return ActionCleanup
	}

	walSize := state.TotalWALSize()
	if walSize > c.cfg.MaxWALSize && len(state.WALFiles) > 0 {
		return ActionConvertToSort
	}

	if len(state.SortedFiles) > c.cfg.MaxSortedFiles {
		return ActionMergeSorted
	}

	totalSize := state.TotalSortedSize() + state.TotalWALSize()
	if totalSize > c.cfg.SnapshotThreshold && len(state.SortedFiles) > 2 {
		return ActionCreateSnapshot
	}

	return ActionNone
}

func (c *Compactor) convertToSorted(state *DirectoryState, shouldAbort func() bool) error {
	for _, walFile := range state.WALFiles {
		if shouldAbort != nil && shouldAbort() {
			return nil
		}

		reader, closer, err := OpenWALFile(walFile.Path)
		if err != nil {
			log.Printf("[compact] skip corrupt WAL %s: %v", walFile.Path, err)
			continue
		}

		commits, err := reader.ReadAll()
		closer.Close()

		if len(commits) == 0 {
			continue
		}

		globals, byNode := SplitByNode(commits)

		sortedPath := filepath.Join(c.cfg.Dir,
			BuildSortedFilename(c.cfg.IndexID, walFile.StartTS, time.Now().UnixNano()))

		writer, err := NewSafeFileWriter(sortedPath, 64*1024)
		if err != nil {
			return fmt.Errorf("create sorted file: %w", err)
		}

		walWriter := NewWALWriter(writer)
		if err := walWriter.WriteHeader(); err != nil {
			writer.Abort()
			return err
		}

		for _, gc := range globals {
			if err := walWriter.WriteCommit(gc); err != nil {
				writer.Abort()
				return err
			}
		}

		nodeIDs := make([]uint64, 0, len(byNode))
		for id := range byNode {
			nodeIDs = append(nodeIDs, id)
		}
		sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

		for _, id := range nodeIDs {
			nc := byNode[id]
			if err := walWriter.WriteNodeCommits(nc); err != nil {
				writer.Abort()
				return err
			}
		}

		if err := writer.Commit(); err != nil {
			return fmt.Errorf("commit sorted: %w", err)
		}

		os.Remove(walFile.Path)
		c.totalMerged++

		_ = err
	}
	return nil
}

func (c *Compactor) mergeSorted(state *DirectoryState, shouldAbort func() bool) error {
	if len(state.SortedFiles) < 2 {
		return nil
	}

	var allCommits []Commit
	var startTS, endTS int64

	for _, sf := range state.SortedFiles {
		if shouldAbort != nil && shouldAbort() {
			return nil
		}

		reader, closer, err := OpenWALFile(sf.Path)
		if err != nil {
			continue
		}
		commits, _ := reader.ReadAll()
		closer.Close()
		allCommits = append(allCommits, commits...)

		if startTS == 0 || sf.StartTS < startTS {
			startTS = sf.StartTS
		}
		if sf.EndTS > endTS {
			endTS = sf.EndTS
		}
	}

	if len(allCommits) == 0 {
		return nil
	}

	globals, byNode := SplitByNode(allCommits)

	mergedPath := filepath.Join(c.cfg.Dir,
		BuildSortedFilename(c.cfg.IndexID, startTS, endTS))

	writer, err := NewSafeFileWriter(mergedPath, 128*1024)
	if err != nil {
		return err
	}

	walWriter := NewWALWriter(writer)
	if err := walWriter.WriteHeader(); err != nil {
		writer.Abort()
		return err
	}

	for _, gc := range globals {
		walWriter.WriteCommit(gc)
	}

	nodeIDs := make([]uint64, 0, len(byNode))
	for id := range byNode {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

	for _, id := range nodeIDs {
		nc := byNode[id]
		nc.Commits = deduplicateCommits(nc.Commits)
		walWriter.WriteNodeCommits(nc)
	}

	if err := writer.Commit(); err != nil {
		return err
	}

	for _, sf := range state.SortedFiles {
		os.Remove(sf.Path)
	}
	c.totalMerged += len(state.SortedFiles)

	return nil
}

func (c *Compactor) createSnapshot(state *DirectoryState, shouldAbort func() bool) error {
	var allCommits []Commit

	for _, sf := range state.SortedFiles {
		reader, closer, err := OpenWALFile(sf.Path)
		if err != nil {
			continue
		}
		commits, _ := reader.ReadAll()
		closer.Close()
		allCommits = append(allCommits, commits...)
	}
	for _, wf := range state.WALFiles {
		reader, closer, err := OpenWALFile(wf.Path)
		if err != nil {
			continue
		}
		commits, _ := reader.ReadAll()
		closer.Close()
		allCommits = append(allCommits, commits...)
	}

	if len(allCommits) == 0 {
		return nil
	}

	snapPath := filepath.Join(c.cfg.Dir,
		BuildSnapshotFilename(c.cfg.IndexID, time.Now().UnixNano(), c.cycleCount))

	writer, err := NewSafeFileWriter(snapPath, 256*1024)
	if err != nil {
		return err
	}

	walWriter := NewWALWriter(writer)
	if err := walWriter.WriteHeader(); err != nil {
		writer.Abort()
		return err
	}

	globals, byNode := SplitByNode(allCommits)
	for _, gc := range globals {
		walWriter.WriteCommit(gc)
	}
	nodeIDs := make([]uint64, 0, len(byNode))
	for id := range byNode {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
	for _, id := range nodeIDs {
		nc := byNode[id]
		nc.Commits = deduplicateCommits(nc.Commits)
		walWriter.WriteNodeCommits(nc)
	}

	if err := writer.Commit(); err != nil {
		return err
	}

	for _, sf := range state.SortedFiles {
		os.Remove(sf.Path)
	}
	for _, wf := range state.WALFiles {
		os.Remove(wf.Path)
	}
	c.totalCleaned += state.FileCount()

	return nil
}

func (c *Compactor) cleanup(state *DirectoryState) error {
	cleaned, err := CleanupOrphanedTempFiles(c.cfg.Dir)
	if cleaned > 0 {
		log.Printf("[compact] cleaned %d orphaned temp files", cleaned)
	}

	for _, overlap := range state.Overlaps {
		if overlap.FileA.Size < overlap.FileB.Size {
			os.Remove(overlap.FileA.Path)
		} else {
			os.Remove(overlap.FileB.Path)
		}
		c.totalCleaned++
	}

	return err
}

func (c *Compactor) Stats() map[string]interface{} {
	return map[string]interface{}{
		"cycles":       c.cycleCount,
		"total_merged": c.totalMerged,
		"total_cleaned": c.totalCleaned,
	}
}

func deduplicateCommits(commits []Commit) []Commit {
	if len(commits) <= 1 {
		return commits
	}

	result := make([]Commit, 0, len(commits))
	linkState := make(map[int][]uint64)

	for _, c := range commits {
		switch c.Type {
		case CommitReplaceLinks:
			linkState[c.Level] = c.Links
		case CommitDeleteNode:
			return []Commit{c}
		default:
			result = append(result, c)
		}
	}

	for level, links := range linkState {
		result = append(result, Commit{
			Type:   CommitReplaceLinks,
			NodeID: commits[0].NodeID,
			Level:  level,
			Links:  links,
		})
	}

	return result
}
