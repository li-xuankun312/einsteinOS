package compact

import (
	"fmt"
	"io"
	"os"
)

type IteratorLike interface {
	ID() int
	GlobalCommits() []Commit
	Current() *NodeCommits
	Exhausted() bool
	Next() (bool, error)
}

type Iterator struct {
	id         int
	reader     *WALReader
	closer     io.Closer
	globals    []Commit
	current    *NodeCommits
	exhausted  bool
	pending    []Commit
	err        error
}

func NewIteratorFromFile(path string, id int) (*Iterator, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	reader := NewWALReader(f)
	if err := reader.ReadHeader(); err != nil {
		f.Close()
		return nil, fmt.Errorf("read header %s: %w", path, err)
	}

	it := &Iterator{
		id:     id,
		reader: reader,
		closer: f,
	}

	if err := it.readGlobals(); err != nil {
		f.Close()
		return nil, err
	}

	return it, nil
}

func (it *Iterator) readGlobals() error {
	for {
		c, err := it.reader.ReadCommit()
		if err == io.EOF {
			it.exhausted = true
			return nil
		}
		if err != nil {
			it.pending = append(it.pending, c)
			continue
		}

		if c.Type.IsGlobal() {
			it.globals = append(it.globals, c)
		} else {
			it.pending = append(it.pending, c)
			return it.buildCurrentFromPending()
		}
	}
}

func (it *Iterator) buildCurrentFromPending() error {
	if len(it.pending) == 0 {
		return nil
	}

	nodeID := it.pending[0].NodeID
	nc := &NodeCommits{NodeID: nodeID}

	i := 0
	for i < len(it.pending) && it.pending[i].NodeID == nodeID {
		nc.Commits = append(nc.Commits, it.pending[i])
		i++
	}

	it.pending = it.pending[i:]
	it.current = nc
	return nil
}

func (it *Iterator) ID() int {
	return it.id
}

func (it *Iterator) GlobalCommits() []Commit {
	return it.globals
}

func (it *Iterator) Current() *NodeCommits {
	return it.current
}

func (it *Iterator) Exhausted() bool {
	return it.exhausted
}

func (it *Iterator) Next() (bool, error) {
	if it.exhausted {
		return false, nil
	}

	if len(it.pending) > 0 {
		return true, it.buildCurrentFromPending()
	}

	for {
		c, err := it.reader.ReadCommit()
		if err == io.EOF {
			it.exhausted = true
			it.current = nil
			return false, nil
		}
		if err != nil {
			continue
		}

		if c.Type.IsGlobal() {
			it.globals = append(it.globals, c)
			continue
		}

		it.pending = append(it.pending, c)

		for {
			c2, err := it.reader.ReadCommit()
			if err == io.EOF {
				break
			}
			if err != nil {
				continue
			}
			if c2.Type.IsGlobal() {
				it.globals = append(it.globals, c2)
				continue
			}
			it.pending = append(it.pending, c2)
			if c2.NodeID != c.NodeID {
				break
			}
		}

		if err := it.buildCurrentFromPending(); err != nil {
			return false, err
		}
		return true, nil
	}
}

func (it *Iterator) Close() error {
	if it.closer != nil {
		return it.closer.Close()
	}
	return nil
}

type MemoryIterator struct {
	id         int
	globals    []Commit
	nodes      []*NodeCommits
	pos        int
}

func NewMemoryIterator(id int, globals []Commit, nodes []*NodeCommits) *MemoryIterator {
	return &MemoryIterator{
		id:      id,
		globals: globals,
		nodes:   nodes,
		pos:     -1,
	}
}

func (m *MemoryIterator) ID() int               { return m.id }
func (m *MemoryIterator) GlobalCommits() []Commit { return m.globals }

func (m *MemoryIterator) Current() *NodeCommits {
	if m.pos < 0 || m.pos >= len(m.nodes) {
		return nil
	}
	return m.nodes[m.pos]
}

func (m *MemoryIterator) Exhausted() bool {
	return m.pos >= len(m.nodes)
}

func (m *MemoryIterator) Next() (bool, error) {
	m.pos++
	return m.pos < len(m.nodes), nil
}
