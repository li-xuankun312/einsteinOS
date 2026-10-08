package compact

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type RotationConfig struct {
	Dir              string
	Prefix           string
	MaxFileSize      int64
	MaxFileAge       time.Duration
	MaxOpenFiles     int
	SealedRetention  time.Duration
}

func DefaultRotationConfig(dir, prefix string) RotationConfig {
	return RotationConfig{
		Dir:             dir,
		Prefix:          prefix,
		MaxFileSize:     64 * 1024 * 1024,
		MaxFileAge:      10 * time.Minute,
		MaxOpenFiles:    32,
		SealedRetention: 24 * time.Hour,
	}
}

type WALRotator struct {
	mu           sync.Mutex
	cfg          RotationConfig
	active       *activeWAL
	sealed       []sealedWAL
	seqCounter   int
	rotateCount  int
	totalBytes   int64
}

type activeWAL struct {
	path      string
	file      *os.File
	writer    *WALWriter
	size      int64
	createdAt time.Time
	seqNo     int
}

type sealedWAL struct {
	path      string
	size      int64
	createdAt time.Time
	sealedAt  time.Time
	seqNo     int
}

func NewWALRotator(cfg RotationConfig) (*WALRotator, error) {
	if err := os.MkdirAll(cfg.Dir, 0755); err != nil {
		return nil, err
	}
	r := &WALRotator{cfg: cfg}
	if err := r.openNew(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *WALRotator) openNew() error {
	r.seqCounter++
	name := BuildWALFilename(r.cfg.Prefix, time.Now().UnixNano(), r.seqCounter)
	path := filepath.Join(r.cfg.Dir, name)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	writer := NewWALWriter(f)
	if err := writer.WriteHeader(); err != nil {
		f.Close()
		return err
	}

	r.active = &activeWAL{
		path:      path,
		file:      f,
		writer:    writer,
		createdAt: time.Now(),
		seqNo:     r.seqCounter,
	}
	return nil
}

func (r *WALRotator) Write(c Commit) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.needsRotation() {
		if err := r.rotate(); err != nil {
			return err
		}
	}

	if err := r.active.writer.WriteCommit(c); err != nil {
		return err
	}
	r.active.size = r.active.writer.Written()
	r.totalBytes += int64(commitEstSize(c))
	return nil
}

func (r *WALRotator) WriteBatch(commits []Commit) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, c := range commits {
		if r.needsRotation() {
			if err := r.rotate(); err != nil {
				return err
			}
		}
		if err := r.active.writer.WriteCommit(c); err != nil {
			return err
		}
		r.active.size = r.active.writer.Written()
	}
	r.totalBytes += int64(len(commits)) * 20
	return nil
}

func (r *WALRotator) needsRotation() bool {
	if r.active == nil {
		return true
	}
	if r.active.size >= r.cfg.MaxFileSize {
		return true
	}
	if r.cfg.MaxFileAge > 0 && time.Since(r.active.createdAt) > r.cfg.MaxFileAge {
		return true
	}
	return false
}

func (r *WALRotator) rotate() error {
	if r.active != nil {
		r.active.file.Sync()
		r.active.file.Close()

		r.sealed = append(r.sealed, sealedWAL{
			path:      r.active.path,
			size:      r.active.size,
			createdAt: r.active.createdAt,
			sealedAt:  time.Now(),
			seqNo:     r.active.seqNo,
		})
		r.rotateCount++
	}

	if err := r.openNew(); err != nil {
		return err
	}

	r.cleanupSealed()
	return nil
}

func (r *WALRotator) cleanupSealed() {
	if r.cfg.SealedRetention <= 0 {
		return
	}
	cutoff := time.Now().Add(-r.cfg.SealedRetention)
	var kept []sealedWAL
	for _, s := range r.sealed {
		if s.sealedAt.After(cutoff) {
			kept = append(kept, s)
		}
	}
	r.sealed = kept
}

func (r *WALRotator) ForceRotate() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rotate()
}

func (r *WALRotator) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil && r.active.file != nil {
		return r.active.file.Sync()
	}
	return nil
}

func (r *WALRotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil && r.active.file != nil {
		r.active.file.Sync()
		return r.active.file.Close()
	}
	return nil
}

func (r *WALRotator) ActivePath() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil {
		return r.active.path
	}
	return ""
}

func (r *WALRotator) ActiveSize() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil {
		return r.active.size
	}
	return 0
}

func (r *WALRotator) SealedFiles() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	paths := make([]string, len(r.sealed))
	for i, s := range r.sealed {
		paths[i] = s.path
	}
	return paths
}

func (r *WALRotator) Stats() map[string]interface{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return map[string]interface{}{
		"active_path":   r.active.path,
		"active_size":   r.active.size,
		"sealed_count":  len(r.sealed),
		"rotate_count":  r.rotateCount,
		"total_bytes":   r.totalBytes,
		"seq_counter":   r.seqCounter,
	}
}

func commitEstSize(c Commit) int {
	base := 13
	switch c.Type {
	case CommitReplaceLinks:
		return base + 4 + len(c.Links)*8
	case CommitAddLink:
		return base + 18
	default:
		return base
	}
}

type WALTruncator struct {
	dir    string
	prefix string
}

func NewWALTruncator(dir, prefix string) *WALTruncator {
	return &WALTruncator{dir: dir, prefix: prefix}
}

func (t *WALTruncator) TruncateCorrupt(path string) (int64, error) {
	reader, closer, err := OpenWALFile(path)
	if err != nil {
		return 0, err
	}

	var validBytes int64
	lastGoodPos := int64(5)

	commits, _ := reader.ReadAll()
	closer.Close()

	if reader.Corrupted() == 0 {
		return 0, nil
	}

	for _, c := range commits {
		validBytes += int64(commitEstSize(c))
		_ = c
	}
	validBytes = lastGoodPos

	backupPath := path + ".corrupt.bak"
	if err := CopyFileAtomic(path, backupPath); err != nil {
		return 0, fmt.Errorf("backup: %w", err)
	}

	globals, byNode := SplitByNode(commits)
	nodes := make([]*NodeCommits, 0, len(byNode))
	for _, nc := range byNode {
		nodes = append(nodes, nc)
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].NodeID < nodes[j].NodeID
	})

	if err := WriteSortedFile(path, globals, nodes); err != nil {
		os.Rename(backupPath, path)
		return 0, err
	}

	log.Printf("[truncator] truncated %s: kept %d commits, dropped %d corrupt",
		path, len(commits), reader.Corrupted())

	return int64(reader.Corrupted()), nil
}

func (t *WALTruncator) TruncateAll() (int, int64, error) {
	discovery := NewFileDiscovery(t.dir)
	state, err := discovery.Scan()
	if err != nil {
		return 0, 0, err
	}

	filesFixed := 0
	totalDropped := int64(0)

	for _, f := range state.WALFiles {
		dropped, err := t.TruncateCorrupt(f.Path)
		if err != nil {
			log.Printf("[truncator] skip %s: %v", f.Path, err)
			continue
		}
		if dropped > 0 {
			filesFixed++
			totalDropped += dropped
		}
	}

	return filesFixed, totalDropped, nil
}

type FileRetirer struct {
	dir        string
	retireDir  string
}

func NewFileRetirer(dir string) *FileRetirer {
	retireDir := filepath.Join(dir, ".retired")
	os.MkdirAll(retireDir, 0755)
	return &FileRetirer{dir: dir, retireDir: retireDir}
}

func (fr *FileRetirer) Retire(paths []string) (int, error) {
	retired := 0
	for _, path := range paths {
		name := filepath.Base(path)
		dest := filepath.Join(fr.retireDir, name)
		if err := os.Rename(path, dest); err != nil {
			if err2 := CopyFileAtomic(path, dest); err2 != nil {
				continue
			}
			os.Remove(path)
		}
		retired++
	}
	return retired, nil
}

func (fr *FileRetirer) PurgeRetired(olderThan time.Duration) (int, error) {
	entries, err := os.ReadDir(fr.retireDir)
	if err != nil {
		return 0, err
	}

	cutoff := time.Now().Add(-olderThan)
	purged := 0
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(fr.retireDir, entry.Name()))
			purged++
		}
	}
	return purged, nil
}

func (fr *FileRetirer) RetiredCount() int {
	entries, _ := os.ReadDir(fr.retireDir)
	return len(entries)
}

func (fr *FileRetirer) RetiredSize() int64 {
	entries, _ := os.ReadDir(fr.retireDir)
	var total int64
	for _, entry := range entries {
		info, _ := entry.Info()
		if info != nil {
			total += info.Size()
		}
	}
	return total
}
