package compact

import (
	"fmt"
	"log"
	"sort"
	"time"
)

type LoaderConfig struct {
	Dir     string
	IndexID string
}

type LoadResult struct {
	GlobalCommits []Commit
	NodeCommits   map[uint64]*NodeCommits
	Tombstones    map[uint64]bool
	EntryPoint    uint64
	MaxLevel      int
	Ef            int
	WasReset      bool

	FilesRead  int
	TotalBytes int64
	Corrupted  int
	LoadTime   time.Duration
}

type Loader struct {
	cfg       LoaderConfig
	discovery *FileDiscovery
}

func NewLoader(cfg LoaderConfig) *Loader {
	return &Loader{
		cfg:       cfg,
		discovery: NewFileDiscovery(cfg.Dir),
	}
}

func (l *Loader) Load() (*LoadResult, error) {
	start := time.Now()

	state, err := l.discovery.Scan()
	if err != nil {
		return nil, fmt.Errorf("scan dir: %w", err)
	}

	result := &LoadResult{
		NodeCommits: make(map[uint64]*NodeCommits),
		Tombstones:  make(map[uint64]bool),
		MaxLevel:    -1,
		Ef:          -1,
	}

	snapEndTS := int64(0)
	if snap := state.LatestSnapshot(); snap != nil {
		if err := l.loadFile(snap.Path, result); err != nil {
			log.Printf("[loader] warning: snapshot load failed: %v", err)
		} else {
			snapEndTS = snap.EndTS
			result.FilesRead++
			result.TotalBytes += snap.Size
		}
	}

	walFiles := l.collectFiles(state, snapEndTS)
	sort.Slice(walFiles, func(i, j int) bool {
		return walFiles[i].StartTS < walFiles[j].StartTS
	})

	for _, f := range walFiles {
		if err := l.loadFile(f.Path, result); err != nil {
			log.Printf("[loader] warning: file %s: %v", f.Path, err)
			result.Corrupted++
			continue
		}
		result.FilesRead++
		result.TotalBytes += f.Size
	}

	result.LoadTime = time.Since(start)
	log.Printf("[loader] loaded %d files (%d bytes) in %v, %d corrupted",
		result.FilesRead, result.TotalBytes, result.LoadTime, result.Corrupted)

	return result, nil
}

func (l *Loader) loadFile(path string, result *LoadResult) error {
	reader, closer, err := OpenWALFile(path)
	if err != nil {
		return err
	}
	defer closer.Close()

	commits, err := reader.ReadAll()
	if err != nil {
		return err
	}
	result.Corrupted += reader.Corrupted()

	for _, c := range commits {
		l.applyCommit(c, result)
	}

	return nil
}

func (l *Loader) applyCommit(c Commit, result *LoadResult) {
	switch c.Type {
	case CommitSetEntryPoint:
		result.EntryPoint = c.NodeID

	case CommitSetMaxLevel:
		result.MaxLevel = c.Level

	case CommitSetEf:
		result.Ef = int(c.Value)

	case CommitResetIndex:
		result.NodeCommits = make(map[uint64]*NodeCommits)
		result.Tombstones = make(map[uint64]bool)
		result.GlobalCommits = nil
		result.WasReset = true

	case CommitAddTombstone:
		result.Tombstones[c.NodeID] = true

	case CommitRemoveTombstone:
		delete(result.Tombstones, c.NodeID)

	case CommitDeleteNode:
		delete(result.NodeCommits, c.NodeID)
		result.Tombstones[c.NodeID] = true

	default:
		if c.Type.IsGlobal() {
			result.GlobalCommits = append(result.GlobalCommits, c)
		} else {
			nc, ok := result.NodeCommits[c.NodeID]
			if !ok {
				nc = &NodeCommits{NodeID: c.NodeID}
				result.NodeCommits[c.NodeID] = nc
			}
			nc.Commits = append(nc.Commits, c)
		}
	}
}

func (l *Loader) collectFiles(state *DirectoryState, afterTS int64) []FileInfo {
	var files []FileInfo

	for _, sf := range state.SortedFiles {
		if sf.EndTS > afterTS {
			files = append(files, sf)
		}
	}

	for _, wf := range state.WALFiles {
		if wf.StartTS > afterTS {
			files = append(files, wf)
		}
	}

	return files
}

func (lr *LoadResult) Summary() string {
	nodeCount := len(lr.NodeCommits)
	tombstoneCount := len(lr.Tombstones)
	totalCommits := 0
	for _, nc := range lr.NodeCommits {
		totalCommits += len(nc.Commits)
	}
	totalCommits += len(lr.GlobalCommits)

	return fmt.Sprintf("LoadResult{nodes=%d tombstones=%d commits=%d ep=%d maxLevel=%d files=%d corrupted=%d time=%v}",
		nodeCount, tombstoneCount, totalCommits, lr.EntryPoint, lr.MaxLevel,
		lr.FilesRead, lr.Corrupted, lr.LoadTime)
}

func (lr *LoadResult) NodeCount() int {
	return len(lr.NodeCommits)
}

func (lr *LoadResult) ActiveNodeCount() int {
	active := 0
	for id := range lr.NodeCommits {
		if !lr.Tombstones[id] {
			active++
		}
	}
	return active
}
