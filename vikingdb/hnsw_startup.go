package vikingdb

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/adk/v2/vikingdb/compact"
)

type PersistentHNSWConfig struct {
	Dir            string
	IndexID        string
	HNSWCfg        HNSWConfig
	CompactorCfg   compact.CompactorConfig
	AutoCompact    bool
	CompactInterval time.Duration
}

func DefaultPersistentConfig(dir string) PersistentHNSWConfig {
	return PersistentHNSWConfig{
		Dir:             dir,
		IndexID:         "hnsw",
		HNSWCfg:         DefaultHNSWConfig(),
		CompactorCfg:    compact.DefaultCompactorConfig(dir),
		AutoCompact:     true,
		CompactInterval: 60 * time.Second,
	}
}

type PersistentHNSW struct {
	mu          sync.RWMutex
	index       *HNSW
	commitLog   *CommitLogger
	compactor   *compact.Compactor
	cfg         PersistentHNSWConfig
	stopCh      chan struct{}
	stopped     bool
	startupTime time.Duration
}

func NewPersistentHNSW(cfg PersistentHNSWConfig) (*PersistentHNSW, error) {
	if err := os.MkdirAll(cfg.Dir, 0755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}

	ph := &PersistentHNSW{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}

	start := time.Now()

	if err := ph.restore(); err != nil {
		log.Printf("[persistent-hnsw] restore failed, starting fresh: %v", err)
		ph.index = NewHNSW(cfg.HNSWCfg)
	}

	cl, err := NewCommitLogger(cfg.Dir, cfg.IndexID, 64*1024*1024)
	if err != nil {
		return nil, fmt.Errorf("create commit logger: %w", err)
	}
	ph.commitLog = cl

	ph.compactor = compact.NewCompactor(cfg.CompactorCfg)
	ph.startupTime = time.Since(start)

	log.Printf("[persistent-hnsw] started in %v, %d nodes", ph.startupTime, ph.index.Count())

	if cfg.AutoCompact {
		go ph.compactLoop()
	}

	return ph, nil
}

func (ph *PersistentHNSW) restore() error {
	snapPattern := filepath.Join(ph.cfg.Dir, "*.hnsw.snap")
	snapFiles, _ := filepath.Glob(snapPattern)

	if len(snapFiles) > 0 {
		latest := snapFiles[len(snapFiles)-1]
		idx, err := LoadSnapshot(latest, ph.cfg.HNSWCfg.Dist)
		if err == nil {
			ph.index = idx
			log.Printf("[persistent-hnsw] restored from snapshot: %s (%d nodes)", latest, idx.Count())
			return nil
		}
		log.Printf("[persistent-hnsw] snapshot load failed: %v", err)
	}

	loader := compact.NewLoader(compact.LoaderConfig{
		Dir:     ph.cfg.Dir,
		IndexID: ph.cfg.IndexID,
	})

	result, err := loader.Load()
	if err != nil {
		return err
	}

	ph.index = NewHNSW(ph.cfg.HNSWCfg)

	if result.MaxLevel >= 0 {
		ph.index.maxLevel = result.MaxLevel
	}
	if result.EntryPoint > 0 {
		ph.index.entryPoint = result.EntryPoint
	}

	log.Printf("[persistent-hnsw] loaded %s", result.Summary())
	return nil
}

func (ph *PersistentHNSW) Insert(extID string, vec Vector, meta map[string]interface{}) (uint64, error) {
	id := ph.index.Insert(extID, vec, meta)

	ph.mu.RLock()
	cl := ph.commitLog
	ph.mu.RUnlock()

	if cl != nil {
		cl.AddNode(id, ph.index.nodes[id].level)
	}

	return id, nil
}

func (ph *PersistentHNSW) Delete(extID string) error {
	ph.index.DeleteWithTombstone(extID)

	ph.mu.RLock()
	cl := ph.commitLog
	ph.mu.RUnlock()

	if cl != nil {
		if id, ok := ph.index.idMap[extID]; ok {
			cl.AddTombstone(id)
		}
	}

	return nil
}

func (ph *PersistentHNSW) Search(query Vector, topK int) []SearchResult {
	return ph.index.SearchByVector(query, topK, nil)
}

func (ph *PersistentHNSW) SearchWithFilter(query Vector, topK int, filter func(map[string]interface{}) bool) []SearchResult {
	return ph.index.SearchByVector(query, topK, filter)
}

func (ph *PersistentHNSW) Flush() error {
	ph.mu.RLock()
	cl := ph.commitLog
	ph.mu.RUnlock()

	if cl != nil {
		return cl.Flush()
	}
	return nil
}

func (ph *PersistentHNSW) Snapshot() error {
	snapPath := filepath.Join(ph.cfg.Dir,
		fmt.Sprintf("%s_%d.hnsw.snap", ph.cfg.IndexID, time.Now().UnixNano()))
	return SaveSnapshot(ph.index, snapPath)
}

func (ph *PersistentHNSW) compactLoop() {
	ticker := time.NewTicker(ph.cfg.CompactInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ph.stopCh:
			return
		case <-ticker.C:
			ph.compactor.RunCycle(func() bool {
				select {
				case <-ph.stopCh:
					return true
				default:
					return false
				}
			})
		}
	}
}

func (ph *PersistentHNSW) Shutdown() error {
	ph.mu.Lock()
	defer ph.mu.Unlock()

	if ph.stopped {
		return nil
	}
	ph.stopped = true
	close(ph.stopCh)

	if ph.commitLog != nil {
		ph.commitLog.Flush()
		ph.commitLog.Close()
	}

	snapPath := filepath.Join(ph.cfg.Dir,
		fmt.Sprintf("%s_shutdown_%d.hnsw.snap", ph.cfg.IndexID, time.Now().UnixNano()))
	if err := SaveSnapshot(ph.index, snapPath); err != nil {
		log.Printf("[persistent-hnsw] shutdown snapshot failed: %v", err)
	}

	log.Printf("[persistent-hnsw] shutdown complete, %d nodes saved", ph.index.Count())
	return nil
}

func (ph *PersistentHNSW) Index() *HNSW {
	return ph.index
}

func (ph *PersistentHNSW) Stats() map[string]interface{} {
	stats := ph.index.Stats()
	stats["startup_time"] = ph.startupTime.String()
	stats["dir"] = ph.cfg.Dir
	stats["auto_compact"] = ph.cfg.AutoCompact

	if ph.index.metrics != nil {
		stats["metrics"] = ph.index.metrics.Snapshot()
	}
	stats["compactor"] = ph.compactor.Stats()

	return stats
}

func (ph *PersistentHNSW) Count() uint64 {
	return ph.index.Count()
}

func (ph *PersistentHNSW) Compact() error {
	_, err := ph.compactor.RunCycle(nil)
	return err
}

func (ph *PersistentHNSW) Repair() error {
	result, err := compact.RepairDirectory(ph.cfg.Dir)
	if err != nil {
		return err
	}
	if result.FilesRepaired > 0 {
		log.Printf("[persistent-hnsw] repaired %d files, dropped %d commits",
			result.FilesRepaired, result.CommitsDropped)
	}
	return nil
}
