package compact

import (
	"fmt"
	"strings"
)

type ValidationLevel int

const (
	ValidateBasic    ValidationLevel = 0
	ValidateStandard ValidationLevel = 1
	ValidateDeep     ValidationLevel = 2
)

type ValidationResult struct {
	Level           ValidationLevel
	FilesChecked    int
	CommitsChecked  int
	CRCErrors       int
	SequenceErrors  int
	NodeErrors      int
	LinkErrors      int
	Issues          []ValidationIssue
	Valid           bool
}

type ValidationIssue struct {
	Severity string
	File     string
	SeqNo    uint64
	NodeID   uint64
	Message  string
}

func (vi ValidationIssue) String() string {
	parts := []string{vi.Severity}
	if vi.File != "" {
		parts = append(parts, vi.File)
	}
	if vi.SeqNo > 0 {
		parts = append(parts, fmt.Sprintf("seq=%d", vi.SeqNo))
	}
	if vi.NodeID > 0 {
		parts = append(parts, fmt.Sprintf("node=%d", vi.NodeID))
	}
	parts = append(parts, vi.Message)
	return strings.Join(parts, " ")
}

type Validator struct {
	dir   string
	level ValidationLevel
}

func NewValidator(dir string, level ValidationLevel) *Validator {
	return &Validator{dir: dir, level: level}
}

func (v *Validator) Validate() (*ValidationResult, error) {
	discovery := NewFileDiscovery(v.dir)
	state, err := discovery.Scan()
	if err != nil {
		return nil, err
	}

	result := &ValidationResult{
		Level: v.level,
		Valid: true,
	}

	allFiles := make([]FileInfo, 0)
	allFiles = append(allFiles, state.WALFiles...)
	allFiles = append(allFiles, state.SortedFiles...)
	allFiles = append(allFiles, state.SnapshotFiles...)

	for _, f := range allFiles {
		result.FilesChecked++
		v.validateFile(f, result)
	}

	if v.level >= ValidateStandard {
		v.validateOverlaps(state, result)
	}

	if v.level >= ValidateDeep {
		v.validateConsistency(state, result)
	}

	result.Valid = len(result.Issues) == 0

	return result, nil
}

func (v *Validator) validateFile(f FileInfo, result *ValidationResult) {
	reader, closer, err := OpenWALFile(f.Path)
	if err != nil {
		result.Issues = append(result.Issues, ValidationIssue{
			Severity: "ERROR",
			File:     f.Path,
			Message:  fmt.Sprintf("cannot open: %v", err),
		})
		return
	}
	defer closer.Close()

	commits, err := reader.ReadAll()
	result.CommitsChecked += len(commits)
	result.CRCErrors += reader.Corrupted()

	if reader.Corrupted() > 0 {
		result.Issues = append(result.Issues, ValidationIssue{
			Severity: "WARNING",
			File:     f.Path,
			Message:  fmt.Sprintf("%d CRC errors", reader.Corrupted()),
		})
	}

	if v.level >= ValidateStandard {
		v.validateCommitSequence(commits, f.Path, result)
	}

	if v.level >= ValidateDeep {
		v.validateCommitSemantics(commits, f.Path, result)
	}
}

func (v *Validator) validateCommitSequence(commits []Commit, file string, result *ValidationResult) {
	seenNodes := make(map[uint64]bool)
	var lastSeq uint64

	for _, c := range commits {
		if c.SeqNo > 0 && c.SeqNo <= lastSeq {
			result.SequenceErrors++
			result.Issues = append(result.Issues, ValidationIssue{
				Severity: "WARNING",
				File:     file,
				SeqNo:    c.SeqNo,
				Message:  fmt.Sprintf("out of order: seq %d after %d", c.SeqNo, lastSeq),
			})
		}
		lastSeq = c.SeqNo

		switch c.Type {
		case CommitAddNode, CommitAddNodeLevel:
			if seenNodes[c.NodeID] {
				result.Issues = append(result.Issues, ValidationIssue{
					Severity: "INFO",
					File:     file,
					NodeID:   c.NodeID,
					Message:  "duplicate AddNode",
				})
			}
			seenNodes[c.NodeID] = true

		case CommitAddLink, CommitReplaceLinks, CommitClearLinks:
			if !seenNodes[c.NodeID] {
				result.Issues = append(result.Issues, ValidationIssue{
					Severity: "WARNING",
					File:     file,
					NodeID:   c.NodeID,
					Message:  fmt.Sprintf("%s before AddNode", c.Type),
				})
			}

		case CommitDeleteNode:
			if !seenNodes[c.NodeID] {
				result.Issues = append(result.Issues, ValidationIssue{
					Severity: "INFO",
					File:     file,
					NodeID:   c.NodeID,
					Message:  "delete of unknown node",
				})
			}
		}
	}
}

func (v *Validator) validateCommitSemantics(commits []Commit, file string, result *ValidationResult) {
	nodeLevel := make(map[uint64]int)
	nodeLinks := make(map[uint64]map[int]int)

	for _, c := range commits {
		switch c.Type {
		case CommitAddNodeLevel:
			nodeLevel[c.NodeID] = c.Level
			nodeLinks[c.NodeID] = make(map[int]int)

		case CommitAddLink:
			if maxLevel, ok := nodeLevel[c.NodeID]; ok {
				if c.Level > maxLevel {
					result.LinkErrors++
					result.Issues = append(result.Issues, ValidationIssue{
						Severity: "ERROR",
						File:     file,
						NodeID:   c.NodeID,
						Message:  fmt.Sprintf("link at level %d exceeds node level %d", c.Level, maxLevel),
					})
				}
			}
			if c.Target == c.NodeID {
				result.LinkErrors++
				result.Issues = append(result.Issues, ValidationIssue{
					Severity: "ERROR",
					File:     file,
					NodeID:   c.NodeID,
					Message:  "self-link",
				})
			}

		case CommitReplaceLinks:
			if maxLevel, ok := nodeLevel[c.NodeID]; ok {
				if c.Level > maxLevel {
					result.LinkErrors++
					result.Issues = append(result.Issues, ValidationIssue{
						Severity: "ERROR",
						File:     file,
						NodeID:   c.NodeID,
						Message:  fmt.Sprintf("replace links at level %d exceeds node level %d", c.Level, maxLevel),
					})
				}
			}
			seen := make(map[uint64]bool)
			for _, l := range c.Links {
				if l == c.NodeID {
					result.LinkErrors++
					result.Issues = append(result.Issues, ValidationIssue{
						Severity: "ERROR",
						File:     file,
						NodeID:   c.NodeID,
						Message:  "self-link in replace",
					})
				}
				if seen[l] {
					result.LinkErrors++
					result.Issues = append(result.Issues, ValidationIssue{
						Severity: "WARNING",
						File:     file,
						NodeID:   c.NodeID,
						Message:  fmt.Sprintf("duplicate link to %d", l),
					})
				}
				seen[l] = true
			}
		}
	}
}

func (v *Validator) validateOverlaps(state *DirectoryState, result *ValidationResult) {
	for _, o := range state.Overlaps {
		result.Issues = append(result.Issues, ValidationIssue{
			Severity: "WARNING",
			Message: fmt.Sprintf("file overlap: %s [%d-%d] ↔ %s [%d-%d]",
				o.FileA.Path, o.FileA.StartTS, o.FileA.EndTS,
				o.FileB.Path, o.FileB.StartTS, o.FileB.EndTS),
		})
	}
}

func (v *Validator) validateConsistency(state *DirectoryState, result *ValidationResult) {
	loader := NewLoader(LoaderConfig{Dir: v.dir})
	loadResult, err := loader.Load()
	if err != nil {
		result.Issues = append(result.Issues, ValidationIssue{
			Severity: "ERROR",
			Message:  fmt.Sprintf("consistency check failed: %v", err),
		})
		return
	}

	for id := range loadResult.Tombstones {
		if nc, ok := loadResult.NodeCommits[id]; ok {
			hasDelete := false
			for _, c := range nc.Commits {
				if c.Type == CommitDeleteNode {
					hasDelete = true
				}
			}
			if !hasDelete {
				result.NodeErrors++
				result.Issues = append(result.Issues, ValidationIssue{
					Severity: "WARNING",
					NodeID:   id,
					Message:  "tombstoned but not deleted",
				})
			}
		}
	}

	var entryPoint uint64
	for _, c := range loadResult.GlobalCommits {
		if c.Type == CommitSetEntryPoint {
			entryPoint = c.NodeID
		}
	}
	if entryPoint > 0 {
		if _, ok := loadResult.NodeCommits[entryPoint]; !ok {
			if !loadResult.Tombstones[entryPoint] {
				result.NodeErrors++
				result.Issues = append(result.Issues, ValidationIssue{
					Severity: "ERROR",
					NodeID:   entryPoint,
					Message:  "entry point references non-existent node",
				})
			}
		}
	}
}

func (r *ValidationResult) Summary() string {
	var sb strings.Builder
	status := "VALID"
	if !r.Valid {
		status = "INVALID"
	}
	sb.WriteString(fmt.Sprintf("Validation [%s] level=%d:\n", status, r.Level))
	sb.WriteString(fmt.Sprintf("  Files: %d checked\n", r.FilesChecked))
	sb.WriteString(fmt.Sprintf("  Commits: %d checked\n", r.CommitsChecked))
	if r.CRCErrors > 0 {
		sb.WriteString(fmt.Sprintf("  CRC errors: %d\n", r.CRCErrors))
	}
	if r.SequenceErrors > 0 {
		sb.WriteString(fmt.Sprintf("  Sequence errors: %d\n", r.SequenceErrors))
	}
	if r.NodeErrors > 0 {
		sb.WriteString(fmt.Sprintf("  Node errors: %d\n", r.NodeErrors))
	}
	if r.LinkErrors > 0 {
		sb.WriteString(fmt.Sprintf("  Link errors: %d\n", r.LinkErrors))
	}
	if len(r.Issues) > 0 {
		sb.WriteString(fmt.Sprintf("  Issues (%d):\n", len(r.Issues)))
		limit := 20
		for i, issue := range r.Issues {
			if i >= limit {
				sb.WriteString(fmt.Sprintf("    ... and %d more\n", len(r.Issues)-limit))
				break
			}
			sb.WriteString(fmt.Sprintf("    %s\n", issue))
		}
	}
	return sb.String()
}
