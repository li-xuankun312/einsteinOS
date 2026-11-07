package compact

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type FileCollection struct {
	Dir   string
	Files []FileInfo
}

func NewFileCollection(dir string) *FileCollection {
	return &FileCollection{Dir: dir}
}

func (fc *FileCollection) Add(fi FileInfo) {
	fc.Files = append(fc.Files, fi)
}

func (fc *FileCollection) SortByTime() {
	sort.Slice(fc.Files, func(i, j int) bool {
		return fc.Files[i].StartTS < fc.Files[j].StartTS
	})
}

func (fc *FileCollection) SortBySize() {
	sort.Slice(fc.Files, func(i, j int) bool {
		return fc.Files[i].Size > fc.Files[j].Size
	})
}

func (fc *FileCollection) Filter(fn func(FileInfo) bool) []FileInfo {
	var result []FileInfo
	for _, f := range fc.Files {
		if fn(f) {
			result = append(result, f)
		}
	}
	return result
}

func (fc *FileCollection) WALFiles() []FileInfo {
	return fc.Filter(func(f FileInfo) bool { return f.Type == FileWAL })
}

func (fc *FileCollection) SortedFiles() []FileInfo {
	return fc.Filter(func(f FileInfo) bool { return f.Type == FileSorted })
}

func (fc *FileCollection) SnapshotFiles() []FileInfo {
	return fc.Filter(func(f FileInfo) bool { return f.Type == FileSnapshot })
}

func (fc *FileCollection) TotalSize() int64 {
	var total int64
	for _, f := range fc.Files {
		total += f.Size
	}
	return total
}

func (fc *FileCollection) Count() int {
	return len(fc.Files)
}

func (fc *FileCollection) OlderThan(d time.Duration) []FileInfo {
	cutoff := time.Now().Add(-d)
	return fc.Filter(func(f FileInfo) bool {
		return f.CreatedAt.Before(cutoff)
	})
}

func (fc *FileCollection) LargerThan(size int64) []FileInfo {
	return fc.Filter(func(f FileInfo) bool {
		return f.Size > size
	})
}

func (fc *FileCollection) TimeRange() (oldest, newest time.Time) {
	if len(fc.Files) == 0 {
		return
	}
	oldest = fc.Files[0].CreatedAt
	newest = fc.Files[0].CreatedAt
	for _, f := range fc.Files[1:] {
		if f.CreatedAt.Before(oldest) {
			oldest = f.CreatedAt
		}
		if f.CreatedAt.After(newest) {
			newest = f.CreatedAt
		}
	}
	return
}

type DirectoryStats struct {
	TotalFiles      int
	TotalSize       int64
	WALFiles        int
	WALSize         int64
	SortedFiles     int
	SortedSize      int64
	SnapshotFiles   int
	SnapshotSize    int64
	TempFiles       int
	TempSize        int64
	OldestFile      time.Time
	NewestFile      time.Time
	OverlapCount    int
	AvgFileSize     int64
	LargestFile     FileInfo
}

func GetDirectoryStats(dir string) (*DirectoryStats, error) {
	discovery := NewFileDiscovery(dir)
	state, err := discovery.Scan()
	if err != nil {
		return nil, err
	}

	stats := &DirectoryStats{
		OverlapCount: len(state.Overlaps),
	}

	for _, f := range state.WALFiles {
		stats.WALFiles++
		stats.WALSize += f.Size
		stats.TotalFiles++
		stats.TotalSize += f.Size
		stats.updateTimeRange(f)
		stats.updateLargest(f)
	}

	for _, f := range state.SortedFiles {
		stats.SortedFiles++
		stats.SortedSize += f.Size
		stats.TotalFiles++
		stats.TotalSize += f.Size
		stats.updateTimeRange(f)
		stats.updateLargest(f)
	}

	for _, f := range state.SnapshotFiles {
		stats.SnapshotFiles++
		stats.SnapshotSize += f.Size
		stats.TotalFiles++
		stats.TotalSize += f.Size
		stats.updateTimeRange(f)
		stats.updateLargest(f)
	}

	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".tmp_") {
			info, err := entry.Info()
			if err == nil {
				stats.TempFiles++
				stats.TempSize += info.Size()
			}
		}
	}

	if stats.TotalFiles > 0 {
		stats.AvgFileSize = stats.TotalSize / int64(stats.TotalFiles)
	}

	return stats, nil
}

func (s *DirectoryStats) updateTimeRange(f FileInfo) {
	if s.OldestFile.IsZero() || f.CreatedAt.Before(s.OldestFile) {
		s.OldestFile = f.CreatedAt
	}
	if f.CreatedAt.After(s.NewestFile) {
		s.NewestFile = f.CreatedAt
	}
}

func (s *DirectoryStats) updateLargest(f FileInfo) {
	if f.Size > s.LargestFile.Size {
		s.LargestFile = f
	}
}

func (s *DirectoryStats) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Directory Stats:\n"))
	sb.WriteString(fmt.Sprintf("  Total: %d files, %s\n", s.TotalFiles, formatBytes(s.TotalSize)))
	sb.WriteString(fmt.Sprintf("  WAL:      %d files, %s\n", s.WALFiles, formatBytes(s.WALSize)))
	sb.WriteString(fmt.Sprintf("  Sorted:   %d files, %s\n", s.SortedFiles, formatBytes(s.SortedSize)))
	sb.WriteString(fmt.Sprintf("  Snapshot: %d files, %s\n", s.SnapshotFiles, formatBytes(s.SnapshotSize)))
	if s.TempFiles > 0 {
		sb.WriteString(fmt.Sprintf("  Temp:     %d files, %s (need cleanup)\n", s.TempFiles, formatBytes(s.TempSize)))
	}
	if s.OverlapCount > 0 {
		sb.WriteString(fmt.Sprintf("  Overlaps: %d (need compaction)\n", s.OverlapCount))
	}
	sb.WriteString(fmt.Sprintf("  Avg size: %s\n", formatBytes(s.AvgFileSize)))
	if s.LargestFile.Size > 0 {
		sb.WriteString(fmt.Sprintf("  Largest:  %s (%s)\n", filepath.Base(s.LargestFile.Path), formatBytes(s.LargestFile.Size)))
	}
	if !s.OldestFile.IsZero() {
		sb.WriteString(fmt.Sprintf("  Time range: %s to %s\n",
			s.OldestFile.Format("2006-01-02 15:04:05"),
			s.NewestFile.Format("2006-01-02 15:04:05")))
	}
	return sb.String()
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

type RetentionPolicy struct {
	MaxAge      time.Duration
	MaxFiles    int
	MaxSize     int64
	KeepSnapshots int
}

func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		MaxAge:        7 * 24 * time.Hour,
		MaxFiles:      100,
		MaxSize:       1 << 30,
		KeepSnapshots: 2,
	}
}

func (rp RetentionPolicy) Apply(dir string) ([]string, error) {
	discovery := NewFileDiscovery(dir)
	state, err := discovery.Scan()
	if err != nil {
		return nil, err
	}

	var toDelete []string

	if rp.MaxAge > 0 {
		cutoff := time.Now().Add(-rp.MaxAge)
		for _, f := range state.WALFiles {
			if f.CreatedAt.Before(cutoff) {
				toDelete = append(toDelete, f.Path)
			}
		}
		for _, f := range state.SortedFiles {
			if f.CreatedAt.Before(cutoff) {
				toDelete = append(toDelete, f.Path)
			}
		}
	}

	if rp.KeepSnapshots > 0 && len(state.SnapshotFiles) > rp.KeepSnapshots {
		sort.Slice(state.SnapshotFiles, func(i, j int) bool {
			return state.SnapshotFiles[i].EndTS > state.SnapshotFiles[j].EndTS
		})
		for _, f := range state.SnapshotFiles[rp.KeepSnapshots:] {
			toDelete = append(toDelete, f.Path)
		}
	}

	seen := make(map[string]bool)
	var unique []string
	for _, path := range toDelete {
		if !seen[path] {
			seen[path] = true
			unique = append(unique, path)
			os.Remove(path)
		}
	}

	return unique, nil
}
