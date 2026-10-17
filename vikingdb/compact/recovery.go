package compact

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type RecoveryConfig struct {
	Dir               string
	IndexID           string
	RepairCorrupt     bool
	RemoveOrphans     bool
	ResolveOverlaps   bool
	ValidateAfter     bool
	BackupBeforeRepair bool
}

func DefaultRecoveryConfig(dir string) RecoveryConfig {
	return RecoveryConfig{
		Dir:                dir,
		IndexID:            "hnsw",
		RepairCorrupt:      true,
		RemoveOrphans:      true,
		ResolveOverlaps:    true,
		ValidateAfter:      true,
		BackupBeforeRepair: true,
	}
}

type RecoveryResult struct {
	StartedAt        time.Time
	Duration         time.Duration
	FilesScanned     int
	FilesRepaired    int
	FilesRemoved     int
	OverlapsResolved int
	OrphansRemoved   int
	CommitsRecovered int
	CommitsDropped   int
	BytesSaved       int64
	Issues           []string
	PostValidation   *ValidationResult
}

func (r *RecoveryResult) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Recovery completed in %v:\n", r.Duration))
	sb.WriteString(fmt.Sprintf("  Scanned:  %d files\n", r.FilesScanned))
	sb.WriteString(fmt.Sprintf("  Repaired: %d files\n", r.FilesRepaired))
	sb.WriteString(fmt.Sprintf("  Removed:  %d files\n", r.FilesRemoved))
	sb.WriteString(fmt.Sprintf("  Overlaps: %d resolved\n", r.OverlapsResolved))
	sb.WriteString(fmt.Sprintf("  Orphans:  %d removed\n", r.OrphansRemoved))
	sb.WriteString(fmt.Sprintf("  Commits:  %d recovered, %d dropped\n", r.CommitsRecovered, r.CommitsDropped))
	if r.BytesSaved > 0 {
		sb.WriteString(fmt.Sprintf("  Space saved: %s\n", formatBytes(r.BytesSaved)))
	}
	if len(r.Issues) > 0 {
		sb.WriteString(fmt.Sprintf("  Issues (%d):\n", len(r.Issues)))
		for _, i := range r.Issues {
			sb.WriteString(fmt.Sprintf("    - %s\n", i))
		}
	}
	if r.PostValidation != nil {
		if r.PostValidation.Valid {
			sb.WriteString("  Post-validation: PASS\n")
		} else {
			sb.WriteString(fmt.Sprintf("  Post-validation: FAIL (%d issues)\n", len(r.PostValidation.Issues)))
		}
	}
	return sb.String()
}

type Recovery struct {
	cfg       RecoveryConfig
	discovery *FileDiscovery
}

func NewRecovery(cfg RecoveryConfig) *Recovery {
	return &Recovery{
		cfg:       cfg,
		discovery: NewFileDiscovery(cfg.Dir),
	}
}

func (r *Recovery) Run() (*RecoveryResult, error) {
	start := time.Now()
	result := &RecoveryResult{StartedAt: start}

	state, err := r.discovery.Scan()
	if err != nil {
		return result, fmt.Errorf("scan: %w", err)
	}

	if r.cfg.RemoveOrphans {
		orphans, err := r.cleanOrphans()
		if err != nil {
			result.Issues = append(result.Issues, "orphan cleanup: "+err.Error())
		}
		result.OrphansRemoved = orphans
		result.FilesRemoved += orphans
	}

	if r.cfg.ResolveOverlaps && len(state.Overlaps) > 0 {
		resolved := r.resolveOverlaps(state)
		result.OverlapsResolved = resolved
		result.FilesRemoved += resolved
		state, _ = r.discovery.Scan()
	}

	if r.cfg.RepairCorrupt {
		repaired, recovered, dropped := r.repairCorruptFiles(state, result)
		result.FilesRepaired = repaired
		result.CommitsRecovered = recovered
		result.CommitsDropped = dropped
	}

	allFiles := collectAllFiles(state)
	result.FilesScanned = len(allFiles)

	if r.cfg.ValidateAfter {
		validator := NewValidator(r.cfg.Dir, ValidateStandard)
		valResult, err := validator.Validate()
		if err != nil {
			result.Issues = append(result.Issues, "validation: "+err.Error())
		}
		result.PostValidation = valResult
	}

	result.Duration = time.Since(start)
	return result, nil
}

func (r *Recovery) cleanOrphans() (int, error) {
	count, err := CleanupOrphanedTempFiles(r.cfg.Dir)
	return count, err
}

func (r *Recovery) resolveOverlaps(state *DirectoryState) int {
	resolved := 0
	for _, o := range state.Overlaps {
		smaller := o.FileA
		if o.FileB.Size < smaller.Size {
			smaller = o.FileB
		}
		if r.cfg.BackupBeforeRepair {
			backupDir := filepath.Join(r.cfg.Dir, ".recovery-backup")
			os.MkdirAll(backupDir, 0755)
			dest := filepath.Join(backupDir, filepath.Base(smaller.Path))
			CopyFileAtomic(smaller.Path, dest)
		}
		os.Remove(smaller.Path)
		resolved++
	}
	return resolved
}

func (r *Recovery) repairCorruptFiles(state *DirectoryState, result *RecoveryResult) (int, int, int) {
	repaired := 0
	totalRecovered := 0
	totalDropped := 0

	allFiles := collectAllFiles(state)
	for _, f := range allFiles {
		valid, corrupted, err := ValidateWAL(f.Path)
		if err != nil && valid == 0 {
			result.Issues = append(result.Issues,
				fmt.Sprintf("unreadable: %s (%v)", filepath.Base(f.Path), err))
			continue
		}
		if corrupted == 0 {
			totalRecovered += valid
			continue
		}

		reader, closer, err := OpenWALFile(f.Path)
		if err != nil {
			continue
		}
		commits, _ := reader.ReadAll()
		closer.Close()

		if r.cfg.BackupBeforeRepair {
			backupDir := filepath.Join(r.cfg.Dir, ".recovery-backup")
			os.MkdirAll(backupDir, 0755)
			CopyFileAtomic(f.Path, filepath.Join(backupDir, filepath.Base(f.Path)))
		}

		globals, byNode := SplitByNode(commits)
		nodes := sortedNodeList(byNode)

		if err := WriteSortedFile(f.Path, globals, nodes); err != nil {
			result.Issues = append(result.Issues,
				fmt.Sprintf("repair write failed: %s", filepath.Base(f.Path)))
			continue
		}

		repaired++
		totalRecovered += len(commits)
		totalDropped += corrupted

		log.Printf("[recovery] repaired %s: kept %d, dropped %d",
			filepath.Base(f.Path), len(commits), corrupted)
	}

	return repaired, totalRecovered, totalDropped
}

func collectAllFiles(state *DirectoryState) []FileInfo {
	var all []FileInfo
	all = append(all, state.WALFiles...)
	all = append(all, state.SortedFiles...)
	all = append(all, state.SnapshotFiles...)
	return all
}

type WALTailRepair struct {
	dir string
}

func NewWALTailRepair(dir string) *WALTailRepair {
	return &WALTailRepair{dir: dir}
}

func (tr *WALTailRepair) RepairTail(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}

	stat, _ := f.Stat()
	totalSize := stat.Size()

	reader := NewWALReader(f)
	if err := reader.ReadHeader(); err != nil {
		f.Close()
		return 0, fmt.Errorf("invalid header: %w", err)
	}

	validBytes := int64(5)
	for {
		_, err := reader.ReadCommit()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		pos, _ := f.Seek(0, io.SeekCurrent)
		validBytes = pos
	}
	f.Close()

	if validBytes >= totalSize {
		return 0, nil
	}

	truncated := totalSize - validBytes
	if err := os.Truncate(path, validBytes); err != nil {
		return 0, fmt.Errorf("truncate: %w", err)
	}

	log.Printf("[tail-repair] %s: truncated %d bytes (valid: %d/%d)",
		filepath.Base(path), truncated, validBytes, totalSize)

	return truncated, nil
}

func (tr *WALTailRepair) RepairAll() (int, int64, error) {
	discovery := NewFileDiscovery(tr.dir)
	state, err := discovery.Scan()
	if err != nil {
		return 0, 0, err
	}

	fixed := 0
	totalTruncated := int64(0)

	for _, f := range state.WALFiles {
		truncated, err := tr.RepairTail(f.Path)
		if err != nil {
			log.Printf("[tail-repair] skip %s: %v", f.Path, err)
			continue
		}
		if truncated > 0 {
			fixed++
			totalTruncated += truncated
		}
	}

	return fixed, totalTruncated, nil
}

func RecoverFromCrash(dir string) (*RecoveryResult, error) {
	cfg := DefaultRecoveryConfig(dir)
	recovery := NewRecovery(cfg)
	return recovery.Run()
}

func VerifyAndRepair(dir string) error {
	validator := NewValidator(dir, ValidateDeep)
	result, err := validator.Validate()
	if err != nil {
		return err
	}

	if result.Valid {
		log.Printf("[verify] directory healthy: %d files, %d commits",
			result.FilesChecked, result.CommitsChecked)
		return nil
	}

	log.Printf("[verify] found %d issues, running recovery", len(result.Issues))

	recovery := NewRecovery(DefaultRecoveryConfig(dir))
	recResult, err := recovery.Run()
	if err != nil {
		return err
	}

	log.Printf("[verify] %s", recResult.Summary())
	return nil
}

func SafeSnapshotAndCleanup(dir, indexID string) error {
	discovery := NewFileDiscovery(dir)
	state, err := discovery.Scan()
	if err != nil {
		return err
	}

	var allCommits []Commit
	allFiles := collectAllFiles(state)
	for _, f := range allFiles {
		reader, closer, err := OpenWALFile(f.Path)
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

	globals, byNode := SplitByNode(allCommits)
	nodes := sortedNodeList(byNode)
	for _, nc := range nodes {
		nc.Commits = deduplicateCommits(nc.Commits)
	}

	snapPath := filepath.Join(dir,
		BuildSnapshotFilename(indexID, time.Now().UnixNano(), 0))

	if err := WriteSortedFile(snapPath, globals, nodes); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}

	sort.Slice(allFiles, func(i, j int) bool {
		return allFiles[i].StartTS < allFiles[j].StartTS
	})
	for _, f := range allFiles {
		os.Remove(f.Path)
	}

	log.Printf("[safe-snapshot] created %s, removed %d old files",
		filepath.Base(snapPath), len(allFiles))
	return nil
}
