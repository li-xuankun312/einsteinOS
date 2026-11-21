package compact

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type MergeStrategy int

const (
	MergeTiered  MergeStrategy = 0
	MergeLeveled MergeStrategy = 1
	MergeFIFO    MergeStrategy = 2
	MergeMinSize MergeStrategy = 3
)

func (ms MergeStrategy) String() string {
	switch ms {
	case MergeTiered:
		return "tiered"
	case MergeLeveled:
		return "leveled"
	case MergeFIFO:
		return "fifo"
	case MergeMinSize:
		return "min_size"
	default:
		return "unknown"
	}
}

type MergeCandidate struct {
	Files      []FileInfo
	TotalSize  int64
	Score      float64
	Reason     string
}

func (mc MergeCandidate) String() string {
	return fmt.Sprintf("MergeCandidate{files=%d size=%s score=%.2f reason=%s}",
		len(mc.Files), formatBytes(mc.TotalSize), mc.Score, mc.Reason)
}

type MergeSelector interface {
	Select(files []FileInfo, maxInputs int) *MergeCandidate
	Name() string
}

type TieredSelector struct {
	SizeRatio     float64
	MinMergeWidth int
	MaxMergeWidth int
}

func NewTieredSelector() *TieredSelector {
	return &TieredSelector{
		SizeRatio:     1.2,
		MinMergeWidth: 2,
		MaxMergeWidth: 16,
	}
}

func (ts *TieredSelector) Name() string { return "tiered" }

func (ts *TieredSelector) Select(files []FileInfo, maxInputs int) *MergeCandidate {
	if len(files) < ts.MinMergeWidth {
		return nil
	}

	sorted := make([]FileInfo, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Size < sorted[j].Size
	})

	var bestCandidate *MergeCandidate
	bestScore := float64(0)

	for i := 0; i <= len(sorted)-ts.MinMergeWidth; i++ {
		for width := ts.MinMergeWidth; width <= ts.MaxMergeWidth && i+width <= len(sorted); width++ {
			if maxInputs > 0 && width > maxInputs {
				break
			}

			group := sorted[i : i+width]
			if !ts.isValidTier(group) {
				continue
			}

			totalSize := int64(0)
			for _, f := range group {
				totalSize += f.Size
			}

			score := ts.scoreGroup(group, totalSize)
			if score > bestScore {
				bestScore = score
				candidate := &MergeCandidate{
					Files:     make([]FileInfo, len(group)),
					TotalSize: totalSize,
					Score:     score,
					Reason:    fmt.Sprintf("tiered: %d files, ratio=%.2f", len(group), ts.sizeRatio(group)),
				}
				copy(candidate.Files, group)
				bestCandidate = candidate
			}
		}
	}

	return bestCandidate
}

func (ts *TieredSelector) isValidTier(files []FileInfo) bool {
	if len(files) < 2 {
		return false
	}
	smallest := files[0].Size
	largest := files[len(files)-1].Size
	if smallest == 0 {
		return true
	}
	ratio := float64(largest) / float64(smallest)
	return ratio <= ts.SizeRatio*float64(len(files))
}

func (ts *TieredSelector) sizeRatio(files []FileInfo) float64 {
	if len(files) < 2 || files[0].Size == 0 {
		return 1.0
	}
	return float64(files[len(files)-1].Size) / float64(files[0].Size)
}

func (ts *TieredSelector) scoreGroup(files []FileInfo, totalSize int64) float64 {
	fileCount := float64(len(files))
	ratio := ts.sizeRatio(files)
	return fileCount / (1.0 + math.Log1p(ratio))
}

type LeveledSelector struct {
	LevelSizeMultiplier float64
	BaseLevelSize       int64
	MaxLevels           int
}

func NewLeveledSelector() *LeveledSelector {
	return &LeveledSelector{
		LevelSizeMultiplier: 10.0,
		BaseLevelSize:       64 * 1024 * 1024,
		MaxLevels:           7,
	}
}

func (ls *LeveledSelector) Name() string { return "leveled" }

func (ls *LeveledSelector) Select(files []FileInfo, maxInputs int) *MergeCandidate {
	if len(files) < 2 {
		return nil
	}

	levels := ls.assignLevels(files)

	for level := 0; level < ls.MaxLevels; level++ {
		levelFiles := levels[level]
		if len(levelFiles) == 0 {
			continue
		}

		levelSize := int64(0)
		for _, f := range levelFiles {
			levelSize += f.Size
		}

		maxSize := ls.BaseLevelSize
		for i := 0; i < level; i++ {
			maxSize = int64(float64(maxSize) * ls.LevelSizeMultiplier)
		}

		if levelSize > maxSize {
			width := len(levelFiles)
			if maxInputs > 0 && width > maxInputs {
				width = maxInputs
			}

			sort.Slice(levelFiles, func(i, j int) bool {
				return levelFiles[i].Size < levelFiles[j].Size
			})

			selected := levelFiles[:width]
			totalSize := int64(0)
			for _, f := range selected {
				totalSize += f.Size
			}

			return &MergeCandidate{
				Files:     selected,
				TotalSize: totalSize,
				Score:     float64(levelSize) / float64(maxSize),
				Reason:    fmt.Sprintf("leveled L%d: %s/%s", level, formatBytes(levelSize), formatBytes(maxSize)),
			}
		}
	}

	return nil
}

func (ls *LeveledSelector) assignLevels(files []FileInfo) map[int][]FileInfo {
	levels := make(map[int][]FileInfo)
	for _, f := range files {
		level := 0
		size := f.Size
		threshold := ls.BaseLevelSize
		for level < ls.MaxLevels-1 && size > threshold {
			level++
			threshold = int64(float64(threshold) * ls.LevelSizeMultiplier)
		}
		levels[level] = append(levels[level], f)
	}
	return levels
}

type FIFOSelector struct {
	MaxTotalSize int64
	MaxFiles     int
}

func NewFIFOSelector(maxSize int64, maxFiles int) *FIFOSelector {
	return &FIFOSelector{MaxTotalSize: maxSize, MaxFiles: maxFiles}
}

func (fs *FIFOSelector) Name() string { return "fifo" }

func (fs *FIFOSelector) Select(files []FileInfo, maxInputs int) *MergeCandidate {
	totalSize := int64(0)
	for _, f := range files {
		totalSize += f.Size
	}

	if totalSize <= fs.MaxTotalSize && len(files) <= fs.MaxFiles {
		return nil
	}

	sorted := make([]FileInfo, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].StartTS < sorted[j].StartTS
	})

	var toDelete []FileInfo
	deleteSize := int64(0)
	target := totalSize - fs.MaxTotalSize

	for _, f := range sorted {
		if deleteSize >= target && len(files)-len(toDelete) <= fs.MaxFiles {
			break
		}
		toDelete = append(toDelete, f)
		deleteSize += f.Size
	}

	if len(toDelete) == 0 {
		return nil
	}

	return &MergeCandidate{
		Files:     toDelete,
		TotalSize: deleteSize,
		Score:     float64(deleteSize) / float64(totalSize),
		Reason:    fmt.Sprintf("fifo: drop %d oldest files (%s)", len(toDelete), formatBytes(deleteSize)),
	}
}

type MinSizeSelector struct {
	MinFileSize int64
}

func NewMinSizeSelector(minSize int64) *MinSizeSelector {
	return &MinSizeSelector{MinFileSize: minSize}
}

func (ms *MinSizeSelector) Name() string { return "min_size" }

func (ms *MinSizeSelector) Select(files []FileInfo, maxInputs int) *MergeCandidate {
	var small []FileInfo
	var totalSize int64

	for _, f := range files {
		if f.Size < ms.MinFileSize {
			small = append(small, f)
			totalSize += f.Size
		}
	}

	if len(small) < 2 {
		return nil
	}

	if maxInputs > 0 && len(small) > maxInputs {
		sort.Slice(small, func(i, j int) bool {
			return small[i].Size < small[j].Size
		})
		small = small[:maxInputs]
		totalSize = 0
		for _, f := range small {
			totalSize += f.Size
		}
	}

	return &MergeCandidate{
		Files:     small,
		TotalSize: totalSize,
		Score:     float64(len(small)),
		Reason:    fmt.Sprintf("min_size: merge %d files below %s", len(small), formatBytes(ms.MinFileSize)),
	}
}

type CompositeSelector struct {
	selectors []MergeSelector
}

func NewCompositeSelector(selectors ...MergeSelector) *CompositeSelector {
	return &CompositeSelector{selectors: selectors}
}

func (cs *CompositeSelector) Name() string {
	names := make([]string, len(cs.selectors))
	for i, s := range cs.selectors {
		names[i] = s.Name()
	}
	return "composite(" + strings.Join(names, "+") + ")"
}

func (cs *CompositeSelector) Select(files []FileInfo, maxInputs int) *MergeCandidate {
	var best *MergeCandidate

	for _, selector := range cs.selectors {
		candidate := selector.Select(files, maxInputs)
		if candidate == nil {
			continue
		}
		if best == nil || candidate.Score > best.Score {
			best = candidate
		}
	}

	return best
}

func DefaultMergeSelector() MergeSelector {
	return NewCompositeSelector(
		NewMinSizeSelector(1*1024*1024),
		NewTieredSelector(),
	)
}
