package compact

import (
	"fmt"
	"io"
	"os"
)

type SnapshotIterator struct {
	reader    *SnapshotFileReader
	file      *os.File
	header    SnapshotHeader
	globals   []Commit
	current   *NodeCommits
	nodeIdx   int
	nodeCount int
	section   uint8
	exhausted bool
	err       error
}

func NewSnapshotIterator(path string, id int) (*SnapshotIterator, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	reader := NewSnapshotFileReader(f)
	if err := reader.ReadHeader(); err != nil {
		f.Close()
		return nil, fmt.Errorf("read header: %w", err)
	}

	it := &SnapshotIterator{
		reader:    reader,
		file:      f,
		header:    reader.Header,
		nodeCount: int(reader.Header.NodeCount),
	}

	if err := it.readSections(); err != nil {
		f.Close()
		return nil, err
	}

	return it, nil
}

func (it *SnapshotIterator) readSections() error {
	for {
		section, err := it.reader.ReadSection()
		if err != nil {
			return err
		}
		it.section = section

		switch section {
		case snapSectionNodes:
			return it.advanceToFirstNode()
		case snapSectionEnd:
			it.exhausted = true
			return nil
		default:
			continue
		}
	}
}

func (it *SnapshotIterator) advanceToFirstNode() error {
	if it.nodeIdx >= it.nodeCount {
		it.exhausted = true
		return nil
	}
	return it.readNextNode()
}

func (it *SnapshotIterator) readNextNode() error {
	if it.nodeIdx >= it.nodeCount {
		it.exhausted = true
		it.current = nil
		return nil
	}

	node, err := it.reader.ReadNode()
	if err != nil {
		it.err = err
		it.exhausted = true
		return err
	}

	nc := &NodeCommits{NodeID: node.ID}
	nc.Commits = append(nc.Commits, Commit{
		Type:   CommitAddNodeLevel,
		NodeID: node.ID,
		Level:  node.Level,
	})

	it.current = nc
	it.nodeIdx++
	return nil
}

func (it *SnapshotIterator) ID() int {
	return 0
}

func (it *SnapshotIterator) GlobalCommits() []Commit {
	globals := make([]Commit, 0, 3)
	globals = append(globals, Commit{
		Type:   CommitSetMaxLevel,
		Level:  int(it.header.MaxLevel),
	})
	globals = append(globals, Commit{
		Type:   CommitSetEntryPoint,
		NodeID: it.header.EntryPoint,
	})
	if it.header.EfSearch > 0 {
		globals = append(globals, Commit{
			Type:  CommitSetEf,
			Value: uint64(it.header.EfSearch),
		})
	}
	return globals
}

func (it *SnapshotIterator) Current() *NodeCommits {
	return it.current
}

func (it *SnapshotIterator) Exhausted() bool {
	return it.exhausted
}

func (it *SnapshotIterator) Next() (bool, error) {
	if it.exhausted {
		return false, nil
	}
	if err := it.readNextNode(); err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, err
	}
	return !it.exhausted, nil
}

func (it *SnapshotIterator) Close() error {
	if it.file != nil {
		return it.file.Close()
	}
	return nil
}

func (it *SnapshotIterator) Header() SnapshotHeader {
	return it.header
}

func (it *SnapshotIterator) Progress() float64 {
	if it.nodeCount == 0 {
		return 1.0
	}
	return float64(it.nodeIdx) / float64(it.nodeCount)
}

func (it *SnapshotIterator) Remaining() int {
	return it.nodeCount - it.nodeIdx
}

type MultiFileIterator struct {
	iterators []IteratorLike
	current   int
	globals   []Commit
	exhausted bool
}

func NewMultiFileIterator(paths []string) (*MultiFileIterator, error) {
	mfi := &MultiFileIterator{}

	for i, path := range paths {
		it, err := NewIteratorFromFile(path, i)
		if err != nil {
			for _, prev := range mfi.iterators {
				if closer, ok := prev.(*Iterator); ok {
					closer.Close()
				}
			}
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		mfi.iterators = append(mfi.iterators, it)
		mfi.globals = append(mfi.globals, it.GlobalCommits()...)
	}

	if len(mfi.iterators) > 0 {
		mfi.current = 0
		for mfi.current < len(mfi.iterators) && mfi.iterators[mfi.current].Exhausted() {
			mfi.current++
		}
		if mfi.current >= len(mfi.iterators) {
			mfi.exhausted = true
		}
	} else {
		mfi.exhausted = true
	}

	return mfi, nil
}

func (mfi *MultiFileIterator) ID() int {
	return mfi.current
}

func (mfi *MultiFileIterator) GlobalCommits() []Commit {
	return mfi.globals
}

func (mfi *MultiFileIterator) Current() *NodeCommits {
	if mfi.exhausted || mfi.current >= len(mfi.iterators) {
		return nil
	}
	return mfi.iterators[mfi.current].Current()
}

func (mfi *MultiFileIterator) Exhausted() bool {
	return mfi.exhausted
}

func (mfi *MultiFileIterator) Next() (bool, error) {
	if mfi.exhausted {
		return false, nil
	}

	has, err := mfi.iterators[mfi.current].Next()
	if err != nil {
		return false, err
	}
	if has {
		return true, nil
	}

	mfi.current++
	for mfi.current < len(mfi.iterators) {
		if !mfi.iterators[mfi.current].Exhausted() {
			return true, nil
		}
		mfi.current++
	}

	mfi.exhausted = true
	return false, nil
}

func (mfi *MultiFileIterator) Close() error {
	for _, it := range mfi.iterators {
		if closer, ok := it.(*Iterator); ok {
			closer.Close()
		}
	}
	return nil
}
