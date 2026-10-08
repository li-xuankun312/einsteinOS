package compact

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type FileDiscovery struct {
	dir string
}

func NewFileDiscovery(dir string) *FileDiscovery {
	return &FileDiscovery{dir: dir}
}

func (d *FileDiscovery) Scan() (*DirectoryState, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, fmt.Errorf("scan dir: %w", err)
	}

	state := &DirectoryState{}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		fi, err := d.parseFilename(name)
		if err != nil {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		fi.Size = info.Size()
		fi.CreatedAt = info.ModTime()

		switch fi.Type {
		case FileWAL:
			state.WALFiles = append(state.WALFiles, fi)
		case FileSorted:
			state.SortedFiles = append(state.SortedFiles, fi)
		case FileSnapshot:
			state.SnapshotFiles = append(state.SnapshotFiles, fi)
		}
	}

	sort.Slice(state.WALFiles, func(i, j int) bool {
		return state.WALFiles[i].StartTS < state.WALFiles[j].StartTS
	})
	sort.Slice(state.SortedFiles, func(i, j int) bool {
		return state.SortedFiles[i].StartTS < state.SortedFiles[j].StartTS
	})
	sort.Slice(state.SnapshotFiles, func(i, j int) bool {
		return state.SnapshotFiles[i].EndTS < state.SnapshotFiles[j].EndTS
	})

	state.Overlaps = d.detectOverlaps(state)

	return state, nil
}

func (d *FileDiscovery) parseFilename(name string) (FileInfo, error) {
	fi := FileInfo{
		Path: filepath.Join(d.dir, name),
	}

	switch {
	case strings.HasSuffix(name, ".wal"):
		fi.Type = FileWAL
		parts := strings.TrimSuffix(name, ".wal")
		ts, seq, err := parseTimestampSeq(parts)
		if err != nil {
			return fi, err
		}
		fi.StartTS = ts
		fi.EndTS = ts
		fi.SeqNo = seq

	case strings.HasSuffix(name, ".sorted"):
		fi.Type = FileSorted
		parts := strings.TrimSuffix(name, ".sorted")
		start, end, err := parseTimeRange(parts)
		if err != nil {
			return fi, err
		}
		fi.StartTS = start
		fi.EndTS = end

	case strings.HasSuffix(name, ".snap"):
		fi.Type = FileSnapshot
		parts := strings.TrimSuffix(name, ".snap")
		ts, _, err := parseTimestampSeq(parts)
		if err != nil {
			return fi, err
		}
		fi.StartTS = 0
		fi.EndTS = ts

	default:
		return fi, fmt.Errorf("unknown file type: %s", name)
	}

	return fi, nil
}

func parseTimestampSeq(s string) (int64, int, error) {
	parts := strings.Split(s, "_")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("invalid format: %s", s)
	}

	idx := len(parts) - 2
	ts, err := strconv.ParseInt(parts[idx], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse timestamp: %w", err)
	}

	seq, err := strconv.Atoi(parts[idx+1])
	if err != nil {
		return 0, 0, fmt.Errorf("parse seq: %w", err)
	}

	return ts, seq, nil
}

func parseTimeRange(s string) (int64, int64, error) {
	parts := strings.Split(s, "_")
	if len(parts) < 3 {
		return 0, 0, fmt.Errorf("invalid range format: %s", s)
	}

	idx := len(parts) - 2
	start, err := strconv.ParseInt(parts[idx-1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse start: %w", err)
	}
	end, err := strconv.ParseInt(parts[idx], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse end: %w", err)
	}

	return start, end, nil
}

func (d *FileDiscovery) detectOverlaps(state *DirectoryState) []Overlap {
	var overlaps []Overlap

	allFiles := make([]FileInfo, 0, len(state.WALFiles)+len(state.SortedFiles))
	allFiles = append(allFiles, state.WALFiles...)
	allFiles = append(allFiles, state.SortedFiles...)

	for i := 0; i < len(allFiles); i++ {
		for j := i + 1; j < len(allFiles); j++ {
			if allFiles[i].Overlaps(allFiles[j]) {
				overlaps = append(overlaps, Overlap{
					FileA: allFiles[i],
					FileB: allFiles[j],
				})
			}
		}
	}

	return overlaps
}

func BuildWALFilename(prefix string, ts int64, seq int) string {
	return fmt.Sprintf("%s_%d_%d.wal", prefix, ts, seq)
}

func BuildSortedFilename(prefix string, startTS, endTS int64) string {
	return fmt.Sprintf("%s_%d_%d.sorted", prefix, startTS, endTS)
}

func BuildSnapshotFilename(prefix string, ts int64, seq int) string {
	return fmt.Sprintf("%s_%d_%d.snap", prefix, ts, seq)
}

func CleanupOrphanedTempFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}

	cleaned := 0
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".tmp_") {
			path := filepath.Join(dir, name)
			if err := os.Remove(path); err == nil {
				cleaned++
			}
		}
	}
	return cleaned, nil
}
