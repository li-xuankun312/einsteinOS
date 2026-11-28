package compact

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ArchiverConfig struct {
	Dir          string
	ArchiveDir   string
	IndexID      string
	Compress     bool
	MaxArchiveAge time.Duration
	MaxArchiveSize int64
}

func DefaultArchiverConfig(dir string) ArchiverConfig {
	return ArchiverConfig{
		Dir:            dir,
		ArchiveDir:     filepath.Join(dir, ".archive"),
		IndexID:        "hnsw",
		Compress:       true,
		MaxArchiveAge:  30 * 24 * time.Hour,
		MaxArchiveSize: 1 << 30,
	}
}

type Archiver struct {
	cfg       ArchiverConfig
	discovery *FileDiscovery
}

func NewArchiver(cfg ArchiverConfig) *Archiver {
	os.MkdirAll(cfg.ArchiveDir, 0755)
	return &Archiver{
		cfg:       cfg,
		discovery: NewFileDiscovery(cfg.Dir),
	}
}

type ArchiveResult struct {
	FilesArchived   int
	BytesOriginal   int64
	BytesArchived   int64
	CompressionRate float64
	Duration        time.Duration
}

func (ar *ArchiveResult) Summary() string {
	return fmt.Sprintf("Archived %d files: %s → %s (%.1f%% compression) in %v",
		ar.FilesArchived, formatBytes(ar.BytesOriginal), formatBytes(ar.BytesArchived),
		ar.CompressionRate*100, ar.Duration)
}

func (a *Archiver) Archive(files []FileInfo) (*ArchiveResult, error) {
	start := time.Now()
	result := &ArchiveResult{}

	for _, f := range files {
		archiveName := filepath.Base(f.Path)
		if a.cfg.Compress {
			archiveName += ".gz"
		}
		archivePath := filepath.Join(a.cfg.ArchiveDir, archiveName)

		var archiveSize int64
		var err error
		if a.cfg.Compress {
			archiveSize, err = a.compressFile(f.Path, archivePath)
		} else {
			err = CopyFileAtomic(f.Path, archivePath)
			if err == nil {
				info, _ := os.Stat(archivePath)
				if info != nil {
					archiveSize = info.Size()
				}
			}
		}

		if err != nil {
			return result, fmt.Errorf("archive %s: %w", f.Path, err)
		}

		result.FilesArchived++
		result.BytesOriginal += f.Size
		result.BytesArchived += archiveSize
	}

	if result.BytesOriginal > 0 {
		result.CompressionRate = 1.0 - float64(result.BytesArchived)/float64(result.BytesOriginal)
	}
	result.Duration = time.Since(start)

	return result, nil
}

func (a *Archiver) compressFile(src, dst string) (int64, error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst + ".tmp")
	if err != nil {
		return 0, err
	}

	gz, err := gzip.NewWriterLevel(dstFile, gzip.BestCompression)
	if err != nil {
		dstFile.Close()
		os.Remove(dst + ".tmp")
		return 0, err
	}

	if _, err := io.Copy(gz, srcFile); err != nil {
		gz.Close()
		dstFile.Close()
		os.Remove(dst + ".tmp")
		return 0, err
	}

	gz.Close()
	dstFile.Sync()
	info, _ := dstFile.Stat()
	size := info.Size()
	dstFile.Close()

	if err := os.Rename(dst+".tmp", dst); err != nil {
		os.Remove(dst + ".tmp")
		return 0, err
	}

	return size, nil
}

func (a *Archiver) Restore(archiveName string) error {
	archivePath := filepath.Join(a.cfg.ArchiveDir, archiveName)

	if strings.HasSuffix(archiveName, ".gz") {
		return a.decompressFile(archivePath,
			filepath.Join(a.cfg.Dir, strings.TrimSuffix(archiveName, ".gz")))
	}

	return CopyFileAtomic(archivePath, filepath.Join(a.cfg.Dir, archiveName))
}

func (a *Archiver) decompressFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	gz, err := gzip.NewReader(srcFile)
	if err != nil {
		return err
	}
	defer gz.Close()

	dstFile, err := os.Create(dst + ".tmp")
	if err != nil {
		return err
	}

	if _, err := io.Copy(dstFile, gz); err != nil {
		dstFile.Close()
		os.Remove(dst + ".tmp")
		return err
	}

	dstFile.Sync()
	dstFile.Close()

	return os.Rename(dst+".tmp", dst)
}

func (a *Archiver) ListArchives() ([]ArchiveInfo, error) {
	entries, err := os.ReadDir(a.cfg.ArchiveDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var archives []ArchiveInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		archives = append(archives, ArchiveInfo{
			Name:       entry.Name(),
			Size:       info.Size(),
			ModTime:    info.ModTime(),
			Compressed: strings.HasSuffix(entry.Name(), ".gz"),
		})
	}

	sort.Slice(archives, func(i, j int) bool {
		return archives[i].ModTime.After(archives[j].ModTime)
	})

	return archives, nil
}

type ArchiveInfo struct {
	Name       string
	Size       int64
	ModTime    time.Time
	Compressed bool
}

func (ai ArchiveInfo) String() string {
	comp := ""
	if ai.Compressed {
		comp = " [gz]"
	}
	return fmt.Sprintf("%s (%s, %s ago%s)", ai.Name, formatBytes(ai.Size),
		time.Since(ai.ModTime).Round(time.Hour), comp)
}

func (a *Archiver) PurgeOldArchives() (int, int64, error) {
	archives, err := a.ListArchives()
	if err != nil {
		return 0, 0, err
	}

	cutoff := time.Now().Add(-a.cfg.MaxArchiveAge)
	purged := 0
	purgedSize := int64(0)

	for _, ar := range archives {
		if ar.ModTime.Before(cutoff) {
			path := filepath.Join(a.cfg.ArchiveDir, ar.Name)
			if err := os.Remove(path); err == nil {
				purged++
				purgedSize += ar.Size
			}
		}
	}

	return purged, purgedSize, nil
}

func (a *Archiver) EnforceMaxSize() (int, int64, error) {
	archives, err := a.ListArchives()
	if err != nil {
		return 0, 0, err
	}

	var totalSize int64
	for _, ar := range archives {
		totalSize += ar.Size
	}

	if totalSize <= a.cfg.MaxArchiveSize {
		return 0, 0, nil
	}

	sort.Slice(archives, func(i, j int) bool {
		return archives[i].ModTime.Before(archives[j].ModTime)
	})

	purged := 0
	purgedSize := int64(0)
	for _, ar := range archives {
		if totalSize <= a.cfg.MaxArchiveSize {
			break
		}
		path := filepath.Join(a.cfg.ArchiveDir, ar.Name)
		if err := os.Remove(path); err == nil {
			totalSize -= ar.Size
			purged++
			purgedSize += ar.Size
		}
	}

	return purged, purgedSize, nil
}

func (a *Archiver) ArchiveStats() (*ArchiveStats, error) {
	archives, err := a.ListArchives()
	if err != nil {
		return nil, err
	}

	stats := &ArchiveStats{}
	for _, ar := range archives {
		stats.TotalFiles++
		stats.TotalSize += ar.Size
		if ar.Compressed {
			stats.CompressedFiles++
		}
		if stats.Oldest.IsZero() || ar.ModTime.Before(stats.Oldest) {
			stats.Oldest = ar.ModTime
		}
		if ar.ModTime.After(stats.Newest) {
			stats.Newest = ar.ModTime
		}
	}

	return stats, nil
}

type ArchiveStats struct {
	TotalFiles      int
	TotalSize       int64
	CompressedFiles int
	Oldest          time.Time
	Newest          time.Time
}

func (s *ArchiveStats) Summary() string {
	if s.TotalFiles == 0 {
		return "Archive: empty"
	}
	return fmt.Sprintf("Archive: %d files (%d compressed), %s, %s to %s",
		s.TotalFiles, s.CompressedFiles, formatBytes(s.TotalSize),
		s.Oldest.Format("2006-01-02"), s.Newest.Format("2006-01-02"))
}

func ArchiveAndCleanup(dir string) error {
	cfg := DefaultArchiverConfig(dir)
	archiver := NewArchiver(cfg)
	discovery := NewFileDiscovery(dir)

	state, err := discovery.Scan()
	if err != nil {
		return err
	}

	snap := state.LatestSnapshot()
	if snap == nil {
		return nil
	}

	var toArchive []FileInfo
	for _, f := range state.WALFiles {
		if f.EndTS <= snap.EndTS {
			toArchive = append(toArchive, f)
		}
	}
	for _, f := range state.SortedFiles {
		if f.EndTS <= snap.EndTS {
			toArchive = append(toArchive, f)
		}
	}

	if len(toArchive) == 0 {
		return nil
	}

	result, err := archiver.Archive(toArchive)
	if err != nil {
		return err
	}

	for _, f := range toArchive {
		os.Remove(f.Path)
	}

	archiver.PurgeOldArchives()
	archiver.EnforceMaxSize()

	fmt.Printf("[archive] %s\n", result.Summary())
	return nil
}
