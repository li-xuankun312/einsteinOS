package compact

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type MigratorConfig struct {
	Dir       string
	IndexID   string
	FromVer   int
	ToVer     int
	BackupDir string
}

type Migrator struct {
	cfg       MigratorConfig
	discovery *FileDiscovery
	migrated  int
	skipped   int
	errors    []string
}

func NewMigrator(cfg MigratorConfig) *Migrator {
	if cfg.BackupDir == "" {
		cfg.BackupDir = filepath.Join(cfg.Dir, ".backup")
	}
	return &Migrator{
		cfg:       cfg,
		discovery: NewFileDiscovery(cfg.Dir),
	}
}

func (m *Migrator) Run() error {
	log.Printf("[migrator] starting migration v%d → v%d in %s", m.cfg.FromVer, m.cfg.ToVer, m.cfg.Dir)

	state, err := m.discovery.Scan()
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	if err := os.MkdirAll(m.cfg.BackupDir, 0755); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}

	allFiles := make([]FileInfo, 0)
	allFiles = append(allFiles, state.WALFiles...)
	allFiles = append(allFiles, state.SortedFiles...)
	allFiles = append(allFiles, state.SnapshotFiles...)

	sort.Slice(allFiles, func(i, j int) bool {
		return allFiles[i].StartTS < allFiles[j].StartTS
	})

	for _, f := range allFiles {
		if err := m.migrateFile(f); err != nil {
			m.errors = append(m.errors, fmt.Sprintf("%s: %v", f.Path, err))
			log.Printf("[migrator] error migrating %s: %v", f.Path, err)
		}
	}

	log.Printf("[migrator] complete: %d migrated, %d skipped, %d errors",
		m.migrated, m.skipped, len(m.errors))
	return nil
}

func (m *Migrator) migrateFile(f FileInfo) error {
	reader, closer, err := OpenWALFile(f.Path)
	if err != nil {
		return err
	}

	commits, err := reader.ReadAll()
	closer.Close()

	if err != nil && len(commits) == 0 {
		m.skipped++
		return nil
	}

	needsMigration := false
	migratedCommits := make([]Commit, 0, len(commits))

	for _, c := range commits {
		mc, changed := m.migrateCommit(c)
		if changed {
			needsMigration = true
		}
		migratedCommits = append(migratedCommits, mc)
	}

	if !needsMigration {
		m.skipped++
		return nil
	}

	backupPath := filepath.Join(m.cfg.BackupDir, filepath.Base(f.Path))
	if err := CopyFileAtomic(f.Path, backupPath); err != nil {
		return fmt.Errorf("backup: %w", err)
	}

	globals, byNode := SplitByNode(migratedCommits)
	nodeIDs := make([]uint64, 0, len(byNode))
	for id := range byNode {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

	nodes := make([]*NodeCommits, len(nodeIDs))
	for i, id := range nodeIDs {
		nodes[i] = byNode[id]
	}

	if err := WriteSortedFile(f.Path, globals, nodes); err != nil {
		return fmt.Errorf("write migrated: %w", err)
	}

	m.migrated++
	return nil
}

func (m *Migrator) migrateCommit(c Commit) (Commit, bool) {
	if m.cfg.FromVer == 1 && m.cfg.ToVer == 2 {
		if c.Type == CommitAddNode {
			return Commit{
				Type:   CommitAddNodeLevel,
				NodeID: c.NodeID,
				Level:  0,
				SeqNo:  c.SeqNo,
			}, true
		}
	}
	return c, false
}

func (m *Migrator) Stats() map[string]interface{} {
	return map[string]interface{}{
		"migrated": m.migrated,
		"skipped":  m.skipped,
		"errors":   len(m.errors),
	}
}

func (m *Migrator) Errors() []string {
	return m.errors
}

func DetectVersion(dir string) (int, error) {
	discovery := NewFileDiscovery(dir)
	state, err := discovery.Scan()
	if err != nil {
		return 0, err
	}

	allFiles := make([]FileInfo, 0)
	allFiles = append(allFiles, state.WALFiles...)
	allFiles = append(allFiles, state.SortedFiles...)

	if len(allFiles) == 0 {
		return walVersion, nil
	}

	f := allFiles[0]
	reader, closer, err := OpenWALFile(f.Path)
	if err != nil {
		return 0, err
	}
	defer closer.Close()

	commits, _ := reader.ReadAll()
	hasOldAddNode := false
	hasNewAddNode := false
	for _, c := range commits {
		if c.Type == CommitAddNode {
			hasOldAddNode = true
		}
		if c.Type == CommitAddNodeLevel {
			hasNewAddNode = true
		}
	}

	if hasOldAddNode && !hasNewAddNode {
		return 1, nil
	}
	return 2, nil
}

func NeedsMigration(dir string) (bool, int, int, error) {
	currentVer, err := DetectVersion(dir)
	if err != nil {
		return false, 0, 0, err
	}
	targetVer := int(walVersion)
	return currentVer < targetVer, currentVer, targetVer, nil
}

type GarbageCollector struct {
	dir       string
	discovery *FileDiscovery
	dryRun    bool
}

func NewGarbageCollector(dir string, dryRun bool) *GarbageCollector {
	return &GarbageCollector{
		dir:       dir,
		discovery: NewFileDiscovery(dir),
		dryRun:    dryRun,
	}
}

func (gc *GarbageCollector) Run() ([]string, error) {
	state, err := gc.discovery.Scan()
	if err != nil {
		return nil, err
	}

	var toDelete []string

	cleaned, _ := CleanupOrphanedTempFiles(gc.dir)
	if cleaned > 0 {
		toDelete = append(toDelete, fmt.Sprintf("%d temp files", cleaned))
	}

	if snap := state.LatestSnapshot(); snap != nil {
		for _, wf := range state.WALFiles {
			if wf.EndTS <= snap.EndTS {
				toDelete = append(toDelete, wf.Path)
				if !gc.dryRun {
					os.Remove(wf.Path)
				}
			}
		}
		for _, sf := range state.SortedFiles {
			if sf.EndTS <= snap.EndTS {
				toDelete = append(toDelete, sf.Path)
				if !gc.dryRun {
					os.Remove(sf.Path)
				}
			}
		}

		if len(state.SnapshotFiles) > 2 {
			sort.Slice(state.SnapshotFiles, func(i, j int) bool {
				return state.SnapshotFiles[i].EndTS > state.SnapshotFiles[j].EndTS
			})
			for _, oldSnap := range state.SnapshotFiles[2:] {
				toDelete = append(toDelete, oldSnap.Path)
				if !gc.dryRun {
					os.Remove(oldSnap.Path)
				}
			}
		}
	}

	entries, _ := os.ReadDir(gc.dir)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".backup") || strings.HasSuffix(name, ".old") {
			path := filepath.Join(gc.dir, name)
			info, _ := entry.Info()
			if info != nil && time.Since(info.ModTime()) > 7*24*time.Hour {
				toDelete = append(toDelete, path)
				if !gc.dryRun {
					os.Remove(path)
				}
			}
		}
	}

	return toDelete, nil
}

type RepairResult struct {
	FilesChecked   int
	FilesRepaired  int
	CommitsDropped int
	Issues         []string
}

func RepairDirectory(dir string) (*RepairResult, error) {
	discovery := NewFileDiscovery(dir)
	state, err := discovery.Scan()
	if err != nil {
		return nil, err
	}

	result := &RepairResult{}

	allFiles := make([]FileInfo, 0)
	allFiles = append(allFiles, state.WALFiles...)
	allFiles = append(allFiles, state.SortedFiles...)

	for _, f := range allFiles {
		result.FilesChecked++

		reader, closer, err := OpenWALFile(f.Path)
		if err != nil {
			result.Issues = append(result.Issues, fmt.Sprintf("cannot open %s: %v", f.Path, err))
			continue
		}

		commits, _ := reader.ReadAll()
		corrupted := reader.Corrupted()
		closer.Close()

		if corrupted == 0 {
			continue
		}

		result.CommitsDropped += corrupted
		result.Issues = append(result.Issues,
			fmt.Sprintf("%s: %d corrupted records dropped", f.Path, corrupted))

		if len(commits) == 0 {
			os.Remove(f.Path)
			result.FilesRepaired++
			continue
		}

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

		repairedPath := f.Path + ".repaired"
		if err := WriteSortedFile(repairedPath, globals, nodes); err != nil {
			result.Issues = append(result.Issues,
				fmt.Sprintf("repair write failed for %s: %v", f.Path, err))
			continue
		}

		backupPath := f.Path + ".corrupt"
		os.Rename(f.Path, backupPath)
		os.Rename(repairedPath, f.Path)
		result.FilesRepaired++
	}

	return result, nil
}
