package compact

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

type E2ETestConfig struct {
	Dir           string
	NodeCount     int
	MaxLevel      int
	MaxConns      int
	WALCount      int
	CommitsPerWAL int
	Seed          int64
}

func DefaultE2ETestConfig(dir string) E2ETestConfig {
	return E2ETestConfig{
		Dir:           dir,
		NodeCount:     1000,
		MaxLevel:      5,
		MaxConns:      16,
		WALCount:      5,
		CommitsPerWAL: 200,
		Seed:          42,
	}
}

type E2ETestResult struct {
	WALsGenerated     int
	TotalCommits      int
	CompactionRuns    int
	VerificationOK    bool
	Issues            []string
	Duration          time.Duration
	BeforeFiles       int
	BeforeSize        int64
	AfterFiles        int
	AfterSize         int64
}

func (r *E2ETestResult) Summary() string {
	status := "PASS"
	if !r.VerificationOK {
		status = "FAIL"
	}
	s := fmt.Sprintf("E2E Test [%s] in %v:\n", status, r.Duration)
	s += fmt.Sprintf("  Generated: %d WALs, %d commits\n", r.WALsGenerated, r.TotalCommits)
	s += fmt.Sprintf("  Compaction: %d runs\n", r.CompactionRuns)
	s += fmt.Sprintf("  Before: %d files, %s\n", r.BeforeFiles, formatBytes(r.BeforeSize))
	s += fmt.Sprintf("  After:  %d files, %s\n", r.AfterFiles, formatBytes(r.AfterSize))
	if len(r.Issues) > 0 {
		s += fmt.Sprintf("  Issues: %d\n", len(r.Issues))
		for _, issue := range r.Issues {
			s += fmt.Sprintf("    - %s\n", issue)
		}
	}
	return s
}

func RunE2ETest(cfg E2ETestConfig) (*E2ETestResult, error) {
	start := time.Now()
	result := &E2ETestResult{}

	if err := os.MkdirAll(cfg.Dir, 0755); err != nil {
		return result, err
	}

	rng := rand.New(rand.NewSource(cfg.Seed))
	totalCommits := 0

	for w := 0; w < cfg.WALCount; w++ {
		commits := generateTestCommits(rng, cfg.NodeCount, cfg.MaxLevel, cfg.MaxConns, cfg.CommitsPerWAL)
		walPath := filepath.Join(cfg.Dir,
			BuildWALFilename("test", time.Now().UnixNano()+int64(w), w+1))

		sw, err := NewSafeFileWriter(walPath, 64*1024)
		if err != nil {
			return result, fmt.Errorf("create WAL %d: %w", w, err)
		}

		writer := NewWALWriter(sw)
		writer.WriteHeader()
		for _, c := range commits {
			writer.WriteCommit(c)
		}

		if err := sw.Commit(); err != nil {
			return result, fmt.Errorf("commit WAL %d: %w", w, err)
		}

		result.WALsGenerated++
		totalCommits += len(commits)
	}
	result.TotalCommits = totalCommits

	discovery := NewFileDiscovery(cfg.Dir)
	beforeState, _ := discovery.Scan()
	result.BeforeFiles = beforeState.FileCount()
	result.BeforeSize = beforeState.TotalSize()

	compactor := NewCompactor(DefaultCompactorConfig(cfg.Dir))
	for i := 0; i < 5; i++ {
		action, err := compactor.RunCycle(nil)
		if err != nil {
			result.Issues = append(result.Issues, fmt.Sprintf("compaction %d: %v", i, err))
		}
		if action == ActionNone {
			break
		}
		result.CompactionRuns++
	}

	afterState, _ := discovery.Scan()
	result.AfterFiles = afterState.FileCount()
	result.AfterSize = afterState.TotalSize()

	result.VerificationOK = true
	verifyResult := verifyCompactedData(cfg.Dir, totalCommits)
	if len(verifyResult) > 0 {
		result.VerificationOK = false
		result.Issues = append(result.Issues, verifyResult...)
	}

	result.Duration = time.Since(start)
	return result, nil
}

func generateTestCommits(rng *rand.Rand, nodeCount, maxLevel, maxConns, count int) []Commit {
	commits := make([]Commit, 0, count)
	existingNodes := make(map[uint64]int)

	for i := 0; i < count; i++ {
		if len(existingNodes) < nodeCount/2 || (rng.Float32() < 0.3 && len(existingNodes) < nodeCount) {
			nodeID := uint64(len(existingNodes) + 1)
			level := rng.Intn(maxLevel + 1)
			existingNodes[nodeID] = level
			commits = append(commits, Commit{
				Type:   CommitAddNodeLevel,
				NodeID: nodeID,
				Level:  level,
			})
			continue
		}

		var nodeID uint64
		var nodeLevel int
		n := rng.Intn(len(existingNodes))
		j := 0
		for id, lvl := range existingNodes {
			if j == n {
				nodeID = id
				nodeLevel = lvl
				break
			}
			j++
		}

		r := rng.Float32()
		switch {
		case r < 0.5:
			level := rng.Intn(nodeLevel + 1)
			numLinks := rng.Intn(maxConns) + 1
			links := make([]uint64, numLinks)
			for l := 0; l < numLinks; l++ {
				links[l] = uint64(rng.Intn(len(existingNodes)) + 1)
			}
			commits = append(commits, Commit{
				Type:   CommitReplaceLinks,
				NodeID: nodeID,
				Level:  level,
				Links:  links,
			})

		case r < 0.7:
			target := uint64(rng.Intn(len(existingNodes)) + 1)
			level := rng.Intn(nodeLevel + 1)
			commits = append(commits, Commit{
				Type:   CommitAddLink,
				NodeID: nodeID,
				Target: target,
				Level:  level,
			})

		case r < 0.8:
			commits = append(commits, Commit{
				Type:   CommitSetEntryPoint,
				NodeID: nodeID,
			})

		case r < 0.85:
			commits = append(commits, Commit{
				Type:  CommitSetMaxLevel,
				Level: rng.Intn(maxLevel + 1),
			})

		case r < 0.90:
			commits = append(commits, Commit{
				Type:   CommitAddTombstone,
				NodeID: nodeID,
			})

		case r < 0.95:
			commits = append(commits, Commit{
				Type:   CommitDeleteNode,
				NodeID: nodeID,
			})
			delete(existingNodes, nodeID)

		default:
			commits = append(commits, Commit{
				Type:   CommitClearLinks,
				NodeID: nodeID,
				Level:  rng.Intn(nodeLevel + 1),
			})
		}
	}

	return commits
}

func verifyCompactedData(dir string, expectedMinCommits int) []string {
	var issues []string

	loader := NewLoader(LoaderConfig{Dir: dir})
	result, err := loader.Load()
	if err != nil {
		issues = append(issues, fmt.Sprintf("load failed: %v", err))
		return issues
	}

	if result.Corrupted > 0 {
		issues = append(issues, fmt.Sprintf("%d corrupted records after compaction", result.Corrupted))
	}

	totalCommits := len(result.GlobalCommits)
	for _, nc := range result.NodeCommits {
		totalCommits += len(nc.Commits)
	}

	if totalCommits == 0 {
		issues = append(issues, "no commits found after compaction")
	}

	for id := range result.Tombstones {
		if nc, ok := result.NodeCommits[id]; ok {
			hasDelete := false
			for _, c := range nc.Commits {
				if c.Type == CommitDeleteNode {
					hasDelete = true
				}
			}
			_ = hasDelete
		}
	}

	return issues
}

func GenerateTestWAL(path string, nodeCount, commitCount int) error {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	commits := generateTestCommits(rng, nodeCount, 3, 16, commitCount)

	sw, err := NewSafeFileWriter(path, 64*1024)
	if err != nil {
		return err
	}

	writer := NewWALWriter(sw)
	writer.WriteHeader()
	for _, c := range commits {
		writer.WriteCommit(c)
	}

	return sw.Commit()
}

func BenchmarkCompaction(dir string, nodeCount, walCount, commitsPerWAL int) (map[string]interface{}, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	genStart := time.Now()
	for i := 0; i < walCount; i++ {
		path := filepath.Join(dir, BuildWALFilename("bench", time.Now().UnixNano(), i))
		if err := GenerateTestWAL(path, nodeCount, commitsPerWAL); err != nil {
			return nil, err
		}
	}
	genDuration := time.Since(genStart)

	discovery := NewFileDiscovery(dir)
	beforeState, _ := discovery.Scan()

	compactStart := time.Now()
	compactor := NewCompactor(DefaultCompactorConfig(dir))
	runs := 0
	for {
		action, _ := compactor.RunCycle(nil)
		if action == ActionNone {
			break
		}
		runs++
		if runs > 20 {
			break
		}
	}
	compactDuration := time.Since(compactStart)

	afterState, _ := discovery.Scan()

	return map[string]interface{}{
		"node_count":       nodeCount,
		"wal_count":        walCount,
		"commits_per_wal":  commitsPerWAL,
		"total_commits":    walCount * commitsPerWAL,
		"gen_duration":     genDuration.String(),
		"compact_duration": compactDuration.String(),
		"compact_runs":     runs,
		"before_files":     beforeState.FileCount(),
		"before_size":      formatBytes(beforeState.TotalSize()),
		"after_files":      afterState.FileCount(),
		"after_size":       formatBytes(afterState.TotalSize()),
		"size_reduction":   fmt.Sprintf("%.1f%%", (1-float64(afterState.TotalSize())/float64(beforeState.TotalSize()))*100),
	}, nil
}
