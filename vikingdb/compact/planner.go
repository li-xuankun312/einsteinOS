package compact

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type PlanAction struct {
	Type        Action
	Priority    int
	InputFiles  []FileInfo
	OutputPath  string
	EstCost     PlanCost
	Reason      string
}

type PlanCost struct {
	ReadBytes    int64
	WriteBytes   int64
	EstDuration  time.Duration
	MemRequired  int64
	DiskRequired int64
}

type CompactionPlan struct {
	Actions    []PlanAction
	TotalCost  PlanCost
	CreatedAt  time.Time
}

type PlannerConfig struct {
	Dir                 string
	IndexID             string
	MaxWALSize          int64
	MaxSortedFiles      int
	MaxTotalSize        int64
	TargetFileSize      int64
	MaxMergeInputs      int
	MaxMemoryBudget     int64
	WriteBytesPerSec    int64
}

func DefaultPlannerConfig(dir string) PlannerConfig {
	return PlannerConfig{
		Dir:              dir,
		IndexID:          "hnsw",
		MaxWALSize:       32 * 1024 * 1024,
		MaxSortedFiles:   8,
		MaxTotalSize:     512 * 1024 * 1024,
		TargetFileSize:   64 * 1024 * 1024,
		MaxMergeInputs:   8,
		MaxMemoryBudget:  256 * 1024 * 1024,
		WriteBytesPerSec: 100 * 1024 * 1024,
	}
}

type Planner struct {
	cfg       PlannerConfig
	discovery *FileDiscovery
}

func NewPlanner(cfg PlannerConfig) *Planner {
	return &Planner{
		cfg:       cfg,
		discovery: NewFileDiscovery(cfg.Dir),
	}
}

func (p *Planner) Plan() (*CompactionPlan, error) {
	state, err := p.discovery.Scan()
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	plan := &CompactionPlan{
		CreatedAt: time.Now(),
	}

	if len(state.Overlaps) > 0 {
		plan.Actions = append(plan.Actions, p.planOverlapResolution(state)...)
	}

	if walActions := p.planWALConversion(state); len(walActions) > 0 {
		plan.Actions = append(plan.Actions, walActions...)
	}

	if mergeActions := p.planSortedMerge(state); len(mergeActions) > 0 {
		plan.Actions = append(plan.Actions, mergeActions...)
	}

	if snapAction := p.planSnapshot(state); snapAction != nil {
		plan.Actions = append(plan.Actions, *snapAction)
	}

	sort.Slice(plan.Actions, func(i, j int) bool {
		return plan.Actions[i].Priority > plan.Actions[j].Priority
	})

	for _, a := range plan.Actions {
		plan.TotalCost.ReadBytes += a.EstCost.ReadBytes
		plan.TotalCost.WriteBytes += a.EstCost.WriteBytes
		plan.TotalCost.MemRequired = maxInt64(plan.TotalCost.MemRequired, a.EstCost.MemRequired)
		plan.TotalCost.DiskRequired += a.EstCost.DiskRequired
		plan.TotalCost.EstDuration += a.EstCost.EstDuration
	}

	return plan, nil
}

func (p *Planner) planOverlapResolution(state *DirectoryState) []PlanAction {
	var actions []PlanAction
	for _, overlap := range state.Overlaps {
		smaller := overlap.FileA
		if overlap.FileB.Size < smaller.Size {
			smaller = overlap.FileB
		}
		actions = append(actions, PlanAction{
			Type:       ActionCleanup,
			Priority:   100,
			InputFiles: []FileInfo{smaller},
			Reason:     fmt.Sprintf("overlap: %s ↔ %s", overlap.FileA.Path, overlap.FileB.Path),
			EstCost: PlanCost{
				EstDuration: time.Millisecond,
			},
		})
	}
	return actions
}

func (p *Planner) planWALConversion(state *DirectoryState) []PlanAction {
	walSize := state.TotalWALSize()
	if walSize <= p.cfg.MaxWALSize && len(state.WALFiles) <= 3 {
		return nil
	}

	var actions []PlanAction
	var batch []FileInfo
	var batchSize int64

	for _, wf := range state.WALFiles {
		batch = append(batch, wf)
		batchSize += wf.Size

		if batchSize >= p.cfg.TargetFileSize || len(batch) >= p.cfg.MaxMergeInputs {
			actions = append(actions, PlanAction{
				Type:       ActionConvertToSort,
				Priority:   80,
				InputFiles: append([]FileInfo{}, batch...),
				Reason:     fmt.Sprintf("convert %d WALs (%s)", len(batch), formatBytes(batchSize)),
				EstCost: PlanCost{
					ReadBytes:    batchSize,
					WriteBytes:   batchSize,
					MemRequired:  batchSize * 2,
					DiskRequired: batchSize,
					EstDuration:  p.estimateIOTime(batchSize * 2),
				},
			})
			batch = nil
			batchSize = 0
		}
	}

	if len(batch) > 0 {
		actions = append(actions, PlanAction{
			Type:       ActionConvertToSort,
			Priority:   70,
			InputFiles: batch,
			Reason:     fmt.Sprintf("convert remaining %d WALs (%s)", len(batch), formatBytes(batchSize)),
			EstCost: PlanCost{
				ReadBytes:    batchSize,
				WriteBytes:   batchSize,
				MemRequired:  batchSize * 2,
				DiskRequired: batchSize,
				EstDuration:  p.estimateIOTime(batchSize * 2),
			},
		})
	}

	return actions
}

func (p *Planner) planSortedMerge(state *DirectoryState) []PlanAction {
	if len(state.SortedFiles) <= p.cfg.MaxSortedFiles {
		return nil
	}

	var actions []PlanAction

	groups := p.groupBySize(state.SortedFiles)
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}

		totalSize := int64(0)
		for _, f := range group {
			totalSize += f.Size
		}

		actions = append(actions, PlanAction{
			Type:       ActionMergeSorted,
			Priority:   60,
			InputFiles: group,
			Reason:     fmt.Sprintf("merge %d sorted files (%s)", len(group), formatBytes(totalSize)),
			EstCost: PlanCost{
				ReadBytes:    totalSize,
				WriteBytes:   totalSize,
				MemRequired:  totalSize,
				DiskRequired: totalSize,
				EstDuration:  p.estimateIOTime(totalSize * 2),
			},
		})
	}

	return actions
}

func (p *Planner) planSnapshot(state *DirectoryState) *PlanAction {
	totalSize := state.TotalSortedSize() + state.TotalWALSize()
	if totalSize <= p.cfg.MaxTotalSize/2 {
		return nil
	}
	if len(state.SortedFiles) < 3 && len(state.WALFiles) < 5 {
		return nil
	}

	var allFiles []FileInfo
	allFiles = append(allFiles, state.SortedFiles...)
	allFiles = append(allFiles, state.WALFiles...)

	return &PlanAction{
		Type:       ActionCreateSnapshot,
		Priority:   40,
		InputFiles: allFiles,
		Reason:     fmt.Sprintf("snapshot: total %s exceeds threshold", formatBytes(totalSize)),
		EstCost: PlanCost{
			ReadBytes:    totalSize,
			WriteBytes:   totalSize / 2,
			MemRequired:  totalSize,
			DiskRequired: totalSize / 2,
			EstDuration:  p.estimateIOTime(totalSize + totalSize/2),
		},
	}
}

func (p *Planner) groupBySize(files []FileInfo) [][]FileInfo {
	if len(files) == 0 {
		return nil
	}

	sorted := make([]FileInfo, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Size < sorted[j].Size
	})

	var groups [][]FileInfo
	var current []FileInfo
	var currentSize int64

	for _, f := range sorted {
		if len(current) >= p.cfg.MaxMergeInputs ||
			(currentSize > 0 && f.Size > currentSize*10) {
			if len(current) >= 2 {
				groups = append(groups, current)
			}
			current = nil
			currentSize = 0
		}
		current = append(current, f)
		currentSize += f.Size
	}
	if len(current) >= 2 {
		groups = append(groups, current)
	}

	return groups
}

func (p *Planner) estimateIOTime(bytes int64) time.Duration {
	if p.cfg.WriteBytesPerSec <= 0 {
		return 0
	}
	secs := float64(bytes) / float64(p.cfg.WriteBytesPerSec)
	return time.Duration(secs * float64(time.Second))
}

func (plan *CompactionPlan) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Compaction Plan (%d actions):\n", len(plan.Actions)))
	for i, a := range plan.Actions {
		sb.WriteString(fmt.Sprintf("  %d. [P%d] %s: %s\n", i+1, a.Priority, a.Type, a.Reason))
		sb.WriteString(fmt.Sprintf("     IO: read %s, write %s, mem %s, est %v\n",
			formatBytes(a.EstCost.ReadBytes), formatBytes(a.EstCost.WriteBytes),
			formatBytes(a.EstCost.MemRequired), a.EstCost.EstDuration))
	}
	sb.WriteString(fmt.Sprintf("  Total: read %s, write %s, mem %s, est %v\n",
		formatBytes(plan.TotalCost.ReadBytes), formatBytes(plan.TotalCost.WriteBytes),
		formatBytes(plan.TotalCost.MemRequired), plan.TotalCost.EstDuration))
	return sb.String()
}

func (plan *CompactionPlan) IsEmpty() bool {
	return len(plan.Actions) == 0
}

func (plan *CompactionPlan) FitsMemoryBudget(budget int64) bool {
	return plan.TotalCost.MemRequired <= budget
}

func (plan *CompactionPlan) FilterByPriority(minPriority int) *CompactionPlan {
	filtered := &CompactionPlan{CreatedAt: plan.CreatedAt}
	for _, a := range plan.Actions {
		if a.Priority >= minPriority {
			filtered.Actions = append(filtered.Actions, a)
		}
	}
	return filtered
}

type SpaceAmplification struct {
	LiveDataSize     int64
	TotalStorageSize int64
	Ratio            float64
	Acceptable       bool
}

func CalculateSpaceAmplification(dir string) (*SpaceAmplification, error) {
	stats, err := GetDirectoryStats(dir)
	if err != nil {
		return nil, err
	}

	liveSize := stats.SnapshotSize
	if liveSize == 0 {
		liveSize = stats.SortedSize
	}
	if liveSize == 0 {
		liveSize = stats.WALSize
	}

	totalSize := stats.TotalSize
	ratio := float64(1.0)
	if liveSize > 0 {
		ratio = float64(totalSize) / float64(liveSize)
	}

	return &SpaceAmplification{
		LiveDataSize:     liveSize,
		TotalStorageSize: totalSize,
		Ratio:            ratio,
		Acceptable:       ratio < 3.0,
	}, nil
}

func (sa *SpaceAmplification) Summary() string {
	status := "OK"
	if !sa.Acceptable {
		status = "HIGH"
	}
	return fmt.Sprintf("Space amplification: %.1fx (%s live, %s total) [%s]",
		sa.Ratio, formatBytes(sa.LiveDataSize), formatBytes(sa.TotalStorageSize), status)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

var _ = math.MaxFloat64
