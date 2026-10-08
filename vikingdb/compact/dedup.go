package compact

import (
	"fmt"
	"sort"
	"strings"
)

type DedupConfig struct {
	CoalesceLinks     bool
	ResolveTombstones bool
	DropOrphanLinks   bool
	CompactLevels     bool
}

func DefaultDedupConfig() DedupConfig {
	return DedupConfig{
		CoalesceLinks:     true,
		ResolveTombstones: true,
		DropOrphanLinks:   true,
		CompactLevels:     true,
	}
}

type DedupResult struct {
	InputCommits  int
	OutputCommits int
	LinksCoalesced int
	TombstonesResolved int
	OrphanLinksDropped int
	NodesCompacted int
}

func (r DedupResult) Reduction() float64 {
	if r.InputCommits == 0 {
		return 0
	}
	return 1.0 - float64(r.OutputCommits)/float64(r.InputCommits)
}

func (r DedupResult) Summary() string {
	return fmt.Sprintf("Dedup: %d→%d commits (%.0f%% reduction), coalesced=%d tombstones=%d orphans=%d",
		r.InputCommits, r.OutputCommits, r.Reduction()*100,
		r.LinksCoalesced, r.TombstonesResolved, r.OrphanLinksDropped)
}

type Deduplicator struct {
	cfg DedupConfig
}

func NewDeduplicator(cfg DedupConfig) *Deduplicator {
	return &Deduplicator{cfg: cfg}
}

func (d *Deduplicator) Deduplicate(commits []Commit) ([]Commit, DedupResult) {
	result := DedupResult{InputCommits: len(commits)}
	globals, byNode := SplitByNode(commits)
	var output []Commit
	globals = d.dedupGlobals(globals)
	output = append(output, globals...)
	nodeIDs := make([]uint64, 0, len(byNode))
	for id := range byNode {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
	activeNodes := make(map[uint64]bool)
	for _, id := range nodeIDs {
		nc := byNode[id]
		isDeleted := false
		for _, c := range nc.Commits {
			if c.Type == CommitDeleteNode {
				isDeleted = true
			}
		}
		if isDeleted && d.cfg.ResolveTombstones {
			result.TombstonesResolved++
			output = append(output, Commit{Type: CommitDeleteNode, NodeID: id})
			continue
		}
		activeNodes[id] = true
	}
	for _, id := range nodeIDs {
		if !activeNodes[id] {
			continue
		}
		nc := byNode[id]
		compacted := d.compactNode(nc, activeNodes, &result)
		output = append(output, compacted...)
	}
	result.OutputCommits = len(output)
	return output, result
}

func (d *Deduplicator) dedupGlobals(globals []Commit) []Commit {
	var lastEntryPoint *Commit
	var lastMaxLevel *Commit
	var lastEf *Commit
	hasReset := false
	for i := len(globals) - 1; i >= 0; i-- {
		c := globals[i]
		switch c.Type {
		case CommitSetEntryPoint:
			if lastEntryPoint == nil {
				lastEntryPoint = &globals[i]
			}
		case CommitSetMaxLevel:
			if lastMaxLevel == nil {
				lastMaxLevel = &globals[i]
			}
		case CommitResetIndex:
			hasReset = true
		case CommitSetEf:
			if lastEf == nil {
				lastEf = &globals[i]
			}
		}
	}
	var result []Commit
	if hasReset {
		result = append(result, Commit{Type: CommitResetIndex})
	}
	if lastMaxLevel != nil {
		result = append(result, *lastMaxLevel)
	}
	if lastEntryPoint != nil {
		result = append(result, *lastEntryPoint)
	}
	if lastEf != nil {
		result = append(result, *lastEf)
	}
	return result
}

func (d *Deduplicator) compactNode(nc *NodeCommits, activeNodes map[uint64]bool, result *DedupResult) []Commit {
	var addCommit *Commit
	linksByLevel := make(map[int][]uint64)
	for _, c := range nc.Commits {
		switch c.Type {
		case CommitAddNode:
			ac := Commit{Type: CommitAddNodeLevel, NodeID: c.NodeID, Level: 0}
			addCommit = &ac
		case CommitAddNodeLevel:
			ac := c
			addCommit = &ac
		case CommitAddLink:
			if d.cfg.CoalesceLinks {
				conns := linksByLevel[c.Level]
				found := false
				for _, existing := range conns {
					if existing == c.Target {
						found = true
						break
					}
				}
				if !found {
					linksByLevel[c.Level] = append(conns, c.Target)
				}
				result.LinksCoalesced++
			}
		case CommitReplaceLinks:
			linksByLevel[c.Level] = make([]uint64, len(c.Links))
			copy(linksByLevel[c.Level], c.Links)
			if d.cfg.CoalesceLinks {
				result.LinksCoalesced++
			}
		case CommitClearLinks:
			linksByLevel[c.Level] = nil
		}
	}
	if addCommit == nil {
		return nil
	}
	var output []Commit
	output = append(output, *addCommit)
	levels := make([]int, 0, len(linksByLevel))
	for l := range linksByLevel {
		levels = append(levels, l)
	}
	sort.Ints(levels)
	for _, level := range levels {
		conns := linksByLevel[level]
		if d.cfg.DropOrphanLinks && activeNodes != nil {
			var filtered []uint64
			for _, c := range conns {
				if activeNodes[c] {
					filtered = append(filtered, c)
				} else {
					result.OrphanLinksDropped++
				}
			}
			conns = filtered
		}
		if len(conns) > 0 {
			output = append(output, Commit{
				Type:   CommitReplaceLinks,
				NodeID: nc.NodeID,
				Level:  level,
				Links:  conns,
			})
		}
	}
	result.NodesCompacted++
	return output
}

func (d *Deduplicator) DedupFiles(dir string) (*DedupResult, error) {
	loader := NewLoader(LoaderConfig{Dir: dir})
	loadResult, err := loader.Load()
	if err != nil {
		return nil, err
	}
	var allCommits []Commit
	allCommits = append(allCommits, loadResult.GlobalCommits...)
	for _, nc := range loadResult.NodeCommits {
		allCommits = append(allCommits, nc.Commits...)
	}
	for id := range loadResult.Tombstones {
		allCommits = append(allCommits, Commit{Type: CommitAddTombstone, NodeID: id})
	}
	deduped, result := d.Deduplicate(allCommits)
	globals, byNode := SplitByNode(deduped)
	nodes := sortedNodeList(byNode)
	outputPath := dir + "/deduped.sorted"
	if err := WriteSortedFile(outputPath, globals, nodes); err != nil {
		return &result, err
	}
	return &result, nil
}

type CommitStats struct {
	TotalCommits    int
	ByType          map[CommitType]int
	UniqueNodes     int
	AvgCommitsPerNode float64
	MaxCommitsPerNode int
	GlobalCommits   int
	LinkCommits     int
	DeleteCommits   int
}

func AnalyzeCommits(commits []Commit) CommitStats {
	stats := CommitStats{
		TotalCommits: len(commits),
		ByType:       make(map[CommitType]int),
	}
	nodeCommitCount := make(map[uint64]int)
	for _, c := range commits {
		stats.ByType[c.Type]++
		if c.Type.IsGlobal() {
			stats.GlobalCommits++
		} else {
			nodeCommitCount[c.NodeID]++
		}
		switch c.Type {
		case CommitAddLink, CommitReplaceLinks, CommitClearLinks:
			stats.LinkCommits++
		case CommitDeleteNode:
			stats.DeleteCommits++
		}
	}
	stats.UniqueNodes = len(nodeCommitCount)
	if stats.UniqueNodes > 0 {
		total := 0
		for _, count := range nodeCommitCount {
			total += count
			if count > stats.MaxCommitsPerNode {
				stats.MaxCommitsPerNode = count
			}
		}
		stats.AvgCommitsPerNode = float64(total) / float64(stats.UniqueNodes)
	}
	return stats
}

func (cs CommitStats) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("CommitStats: %d total, %d nodes\n", cs.TotalCommits, cs.UniqueNodes))
	sb.WriteString(fmt.Sprintf("  Global: %d, Links: %d, Deletes: %d\n",
		cs.GlobalCommits, cs.LinkCommits, cs.DeleteCommits))
	sb.WriteString(fmt.Sprintf("  Per node: avg=%.1f max=%d\n", cs.AvgCommitsPerNode, cs.MaxCommitsPerNode))
	sb.WriteString("  By type:\n")
	types := make([]CommitType, 0, len(cs.ByType))
	for t := range cs.ByType {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool { return cs.ByType[types[i]] > cs.ByType[types[j]] })
	for _, t := range types {
		sb.WriteString(fmt.Sprintf("    %s: %d\n", t, cs.ByType[t]))
	}
	return sb.String()
}

func EstimateCompactionGain(dir string) (float64, error) {
	loader := NewLoader(LoaderConfig{Dir: dir})
	result, err := loader.Load()
	if err != nil {
		return 0, err
	}
	totalCommits := len(result.GlobalCommits)
	for _, nc := range result.NodeCommits {
		totalCommits += len(nc.Commits)
	}
	deduper := NewDeduplicator(DefaultDedupConfig())
	var allCommits []Commit
	allCommits = append(allCommits, result.GlobalCommits...)
	for _, nc := range result.NodeCommits {
		allCommits = append(allCommits, nc.Commits...)
	}
	_, dedupResult := deduper.Deduplicate(allCommits)
	return dedupResult.Reduction(), nil
}
