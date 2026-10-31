package compact

import (
	"container/heap"
	"fmt"
)

type NWayMerger struct {
	iterators []IteratorLike
	globals   []Commit
	h         *iterHeap
	exhausted bool
}

func NewNWayMerger(iterators []IteratorLike) (*NWayMerger, error) {
	if len(iterators) == 0 {
		return nil, fmt.Errorf("no iterators provided")
	}

	m := &NWayMerger{
		iterators: iterators,
	}

	for _, it := range iterators {
		m.globals = append(m.globals, it.GlobalCommits()...)
	}

	m.h = &iterHeap{}
	heap.Init(m.h)

	for i, it := range iterators {
		if !it.Exhausted() && it.Current() != nil {
			heap.Push(m.h, &heapEntry{
				nodeID:   it.Current().NodeID,
				iterIdx:  i,
				iterator: it,
			})
		}
	}

	return m, nil
}

func (m *NWayMerger) GlobalCommits() []Commit {
	return m.globals
}

func (m *NWayMerger) Next() (*NodeCommits, error) {
	if m.h.Len() == 0 {
		m.exhausted = true
		return nil, nil
	}

	entry := heap.Pop(m.h).(*heapEntry)
	nodeID := entry.nodeID
	merged := &NodeCommits{NodeID: nodeID}

	entriesForNode := []*heapEntry{entry}

	for m.h.Len() > 0 {
		peek := (*m.h)[0]
		if peek.nodeID != nodeID {
			break
		}
		entriesForNode = append(entriesForNode, heap.Pop(m.h).(*heapEntry))
	}

	for _, e := range entriesForNode {
		if e.iterator.Current() != nil {
			merged.Commits = append(merged.Commits, e.iterator.Current().Commits...)
		}

		has, err := e.iterator.Next()
		if err != nil {
			return nil, fmt.Errorf("iterator %d advance: %w", e.iterIdx, err)
		}
		if has && e.iterator.Current() != nil {
			heap.Push(m.h, &heapEntry{
				nodeID:   e.iterator.Current().NodeID,
				iterIdx:  e.iterIdx,
				iterator: e.iterator,
			})
		}
	}

	merged.Commits = deduplicateCommits(merged.Commits)
	return merged, nil
}

func (m *NWayMerger) Exhausted() bool {
	return m.exhausted
}

func (m *NWayMerger) MergeAll() ([]Commit, []*NodeCommits, error) {
	globals := m.GlobalCommits()
	var nodes []*NodeCommits

	for {
		nc, err := m.Next()
		if err != nil {
			return globals, nodes, err
		}
		if nc == nil {
			break
		}
		nodes = append(nodes, nc)
	}

	return globals, nodes, nil
}

type heapEntry struct {
	nodeID   uint64
	iterIdx  int
	iterator IteratorLike
}

type iterHeap []*heapEntry

func (h iterHeap) Len() int { return len(h) }

func (h iterHeap) Less(i, j int) bool {
	if h[i].nodeID == h[j].nodeID {
		return h[i].iterIdx < h[j].iterIdx
	}
	return h[i].nodeID < h[j].nodeID
}

func (h iterHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *iterHeap) Push(x interface{}) {
	*h = append(*h, x.(*heapEntry))
}

func (h *iterHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return item
}
