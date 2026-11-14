package compact

import (
	"fmt"
	"io"
	"sort"
)

type SortedWriter struct {
	w           *WALWriter
	lastNodeID  uint64
	nodeCount   int
	commitCount int
	started     bool
}

func NewSortedWriter(w io.Writer) (*SortedWriter, error) {
	walWriter := NewWALWriter(w)
	if err := walWriter.WriteHeader(); err != nil {
		return nil, fmt.Errorf("write header: %w", err)
	}
	return &SortedWriter{w: walWriter}, nil
}

func (sw *SortedWriter) WriteGlobals(commits []Commit) error {
	for _, c := range commits {
		if !c.Type.IsGlobal() {
			return fmt.Errorf("non-global commit in globals: %s", c.Type)
		}
		if err := sw.w.WriteCommit(c); err != nil {
			return err
		}
		sw.commitCount++
	}
	sw.started = true
	return nil
}

func (sw *SortedWriter) WriteNode(nc *NodeCommits) error {
	if sw.started && nc.NodeID < sw.lastNodeID {
		return fmt.Errorf("out of order: node %d after %d", nc.NodeID, sw.lastNodeID)
	}
	for _, c := range nc.Commits {
		if err := sw.w.WriteCommit(c); err != nil {
			return err
		}
		sw.commitCount++
	}
	sw.lastNodeID = nc.NodeID
	sw.nodeCount++
	sw.started = true
	return nil
}

func (sw *SortedWriter) Stats() (nodes, commits int) {
	return sw.nodeCount, sw.commitCount
}

func WriteSortedFile(path string, globals []Commit, nodes []*NodeCommits) error {
	w, err := NewSafeFileWriter(path, 128*1024)
	if err != nil {
		return err
	}

	sw, err := NewSortedWriter(w)
	if err != nil {
		w.Abort()
		return err
	}

	if err := sw.WriteGlobals(globals); err != nil {
		w.Abort()
		return err
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].NodeID < nodes[j].NodeID
	})

	for _, nc := range nodes {
		if err := sw.WriteNode(nc); err != nil {
			w.Abort()
			return err
		}
	}

	return w.Commit()
}

type SortBuffer struct {
	globals []Commit
	nodes   map[uint64]*NodeCommits
}

func NewSortBuffer() *SortBuffer {
	return &SortBuffer{
		nodes: make(map[uint64]*NodeCommits),
	}
}

func (sb *SortBuffer) Add(c Commit) {
	if c.Type.IsGlobal() {
		sb.globals = append(sb.globals, c)
		return
	}
	nc, ok := sb.nodes[c.NodeID]
	if !ok {
		nc = &NodeCommits{NodeID: c.NodeID}
		sb.nodes[c.NodeID] = nc
	}
	nc.Commits = append(nc.Commits, c)
}

func (sb *SortBuffer) AddAll(commits []Commit) {
	for _, c := range commits {
		sb.Add(c)
	}
}

func (sb *SortBuffer) Globals() []Commit {
	return sb.globals
}

func (sb *SortBuffer) SortedNodes() []*NodeCommits {
	ids := make([]uint64, 0, len(sb.nodes))
	for id := range sb.nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	result := make([]*NodeCommits, len(ids))
	for i, id := range ids {
		result[i] = sb.nodes[id]
	}
	return result
}

func (sb *SortBuffer) NodeCount() int {
	return len(sb.nodes)
}

func (sb *SortBuffer) CommitCount() int {
	total := len(sb.globals)
	for _, nc := range sb.nodes {
		total += len(nc.Commits)
	}
	return total
}

func (sb *SortBuffer) Clear() {
	sb.globals = nil
	sb.nodes = make(map[uint64]*NodeCommits)
}

func (sb *SortBuffer) Deduplicate() {
	for id, nc := range sb.nodes {
		nc.Commits = deduplicateCommits(nc.Commits)
		sb.nodes[id] = nc
	}
}

func (sb *SortBuffer) WriteTo(path string) error {
	sb.Deduplicate()
	return WriteSortedFile(path, sb.globals, sb.SortedNodes())
}
