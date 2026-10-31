package compact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type CheckpointData struct {
	Version         int               `json:"version"`
	IndexID         string            `json:"index_id"`
	LastCompactTS   int64             `json:"last_compact_ts"`
	LastSnapshotTS  int64             `json:"last_snapshot_ts"`
	ProcessedFiles  map[string]int64  `json:"processed_files"`
	NodeCount       int               `json:"node_count"`
	TombstoneCount  int               `json:"tombstone_count"`
	EntryPoint      uint64            `json:"entry_point"`
	MaxLevel        int               `json:"max_level"`
	Ef              int               `json:"ef"`
	CompactCycles   int               `json:"compact_cycles"`
	CreatedAt       int64             `json:"created_at"`
	UpdatedAt       int64             `json:"updated_at"`
}

type PersistentCheckpoint struct {
	mu       sync.Mutex
	dir      string
	indexID  string
	data     CheckpointData
	dirty    bool
}

func NewPersistentCheckpoint(dir, indexID string) *PersistentCheckpoint {
	return &PersistentCheckpoint{
		dir:     dir,
		indexID: indexID,
		data: CheckpointData{
			Version:        1,
			IndexID:        indexID,
			ProcessedFiles: make(map[string]int64),
			CreatedAt:      time.Now().UnixNano(),
		},
	}
}

func (pc *PersistentCheckpoint) Load() error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	path := pc.path()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read checkpoint: %w", err)
	}

	var cp CheckpointData
	if err := json.Unmarshal(data, &cp); err != nil {
		return fmt.Errorf("parse checkpoint: %w", err)
	}

	pc.data = cp
	if pc.data.ProcessedFiles == nil {
		pc.data.ProcessedFiles = make(map[string]int64)
	}
	return nil
}

func (pc *PersistentCheckpoint) Save() error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if !pc.dirty {
		return nil
	}

	pc.data.UpdatedAt = time.Now().UnixNano()
	data, err := json.MarshalIndent(pc.data, "", "  ")
	if err != nil {
		return err
	}

	if err := AtomicWriteFile(pc.path(), data); err != nil {
		return err
	}

	pc.dirty = false
	return nil
}

func (pc *PersistentCheckpoint) MarkFile(path string) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.data.ProcessedFiles[filepath.Base(path)] = time.Now().UnixNano()
	pc.dirty = true
}

func (pc *PersistentCheckpoint) IsProcessed(path string) bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	_, ok := pc.data.ProcessedFiles[filepath.Base(path)]
	return ok
}

func (pc *PersistentCheckpoint) SetCompactTS(ts int64) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.data.LastCompactTS = ts
	pc.dirty = true
}

func (pc *PersistentCheckpoint) SetSnapshotTS(ts int64) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.data.LastSnapshotTS = ts
	pc.dirty = true
}

func (pc *PersistentCheckpoint) SetIndexState(nodeCount, tombstoneCount int, ep uint64, maxLevel, ef int) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.data.NodeCount = nodeCount
	pc.data.TombstoneCount = tombstoneCount
	pc.data.EntryPoint = ep
	pc.data.MaxLevel = maxLevel
	pc.data.Ef = ef
	pc.dirty = true
}

func (pc *PersistentCheckpoint) IncrCompactCycles() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.data.CompactCycles++
	pc.dirty = true
}

func (pc *PersistentCheckpoint) Data() CheckpointData {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	cp := pc.data
	cp.ProcessedFiles = make(map[string]int64)
	for k, v := range pc.data.ProcessedFiles {
		cp.ProcessedFiles[k] = v
	}
	return cp
}

func (pc *PersistentCheckpoint) CleanStaleFiles(validPaths map[string]bool) int {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	cleaned := 0
	for name := range pc.data.ProcessedFiles {
		if !validPaths[name] {
			delete(pc.data.ProcessedFiles, name)
			cleaned++
		}
	}
	if cleaned > 0 {
		pc.dirty = true
	}
	return cleaned
}

func (pc *PersistentCheckpoint) Reset() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.data.ProcessedFiles = make(map[string]int64)
	pc.data.LastCompactTS = 0
	pc.data.LastSnapshotTS = 0
	pc.data.CompactCycles = 0
	pc.dirty = true
}

func (pc *PersistentCheckpoint) path() string {
	return filepath.Join(pc.dir, pc.indexID+".checkpoint.json")
}

func (pc *PersistentCheckpoint) Summary() string {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return fmt.Sprintf("Checkpoint{id=%s nodes=%d tombstones=%d ep=%d level=%d cycles=%d files=%d}",
		pc.data.IndexID, pc.data.NodeCount, pc.data.TombstoneCount,
		pc.data.EntryPoint, pc.data.MaxLevel, pc.data.CompactCycles,
		len(pc.data.ProcessedFiles))
}
