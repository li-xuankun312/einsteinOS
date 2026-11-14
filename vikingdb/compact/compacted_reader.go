package compact

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
)

type CompactedFileReader struct {
	f         *os.File
	header    LayoutHeader
	index     []NodeIndexEntry
	indexMap  map[uint64]int
	globals   []byte
	dataStart int64
}

func OpenCompactedFile(path string) (*CompactedFileReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	cr := &CompactedFileReader{f: f}
	if err := cr.readHeader(); err != nil {
		f.Close()
		return nil, err
	}
	if err := cr.readGlobals(); err != nil {
		f.Close()
		return nil, err
	}
	if err := cr.readIndex(); err != nil {
		f.Close()
		return nil, err
	}

	return cr, nil
}

func (cr *CompactedFileReader) readHeader() error {
	var magic [4]byte
	if _, err := io.ReadFull(cr.f, magic[:]); err != nil {
		return err
	}
	if string(magic[:]) != layoutMagic {
		return fmt.Errorf("invalid magic: %q", string(magic[:]))
	}

	ver := make([]byte, 1)
	io.ReadFull(cr.f, ver)
	cr.header.Version = ver[0]
	cr.header.NodeCount = cr.readU64()
	cr.header.GlobalCount = cr.readU32()
	cr.header.IndexOffset = cr.readU64()
	cr.header.DataOffset = cr.readU64()
	cr.header.TotalSize = cr.readU64()
	return nil
}

func (cr *CompactedFileReader) readGlobals() error {
	section := make([]byte, 1)
	io.ReadFull(cr.f, section)
	if section[0] != sectionGlobals {
		return fmt.Errorf("expected globals, got %d", section[0])
	}

	cr.globals = make([]byte, cr.header.GlobalCount)
	_, err := io.ReadFull(cr.f, cr.globals)
	return err
}

func (cr *CompactedFileReader) readIndex() error {
	section := make([]byte, 1)
	io.ReadFull(cr.f, section)
	if section[0] != sectionNodeIndex {
		return fmt.Errorf("expected node index, got %d", section[0])
	}

	cr.index = make([]NodeIndexEntry, cr.header.NodeCount)
	cr.indexMap = make(map[uint64]int, cr.header.NodeCount)

	for i := uint64(0); i < cr.header.NodeCount; i++ {
		cr.index[i] = NodeIndexEntry{
			NodeID:      cr.readU64(),
			DataOffset:  cr.readU64(),
			DataLength:  cr.readU32(),
			CommitCount: cr.readU16(),
			Level:       cr.readU16(),
		}
		cr.indexMap[cr.index[i].NodeID] = int(i)
	}

	section2 := make([]byte, 1)
	io.ReadFull(cr.f, section2)
	if section2[0] == sectionData {
		pos, _ := cr.f.Seek(0, io.SeekCurrent)
		cr.dataStart = pos
	}

	return nil
}

func (cr *CompactedFileReader) LookupNode(nodeID uint64) *NodeIndexEntry {
	idx := sort.Search(len(cr.index), func(i int) bool {
		return cr.index[i].NodeID >= nodeID
	})
	if idx < len(cr.index) && cr.index[idx].NodeID == nodeID {
		return &cr.index[idx]
	}
	return nil
}

func (cr *CompactedFileReader) ReadNodeData(entry *NodeIndexEntry) ([]byte, error) {
	offset := cr.dataStart + int64(entry.DataOffset)
	if _, err := cr.f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data := make([]byte, entry.DataLength)
	if _, err := io.ReadFull(cr.f, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (cr *CompactedFileReader) ReadNodeByID(nodeID uint64) ([]byte, error) {
	entry := cr.LookupNode(nodeID)
	if entry == nil {
		return nil, fmt.Errorf("node %d not found", nodeID)
	}
	return cr.ReadNodeData(entry)
}

func (cr *CompactedFileReader) NodeCount() int {
	return len(cr.index)
}

func (cr *CompactedFileReader) NodeIDs() []uint64 {
	ids := make([]uint64, len(cr.index))
	for i, entry := range cr.index {
		ids[i] = entry.NodeID
	}
	return ids
}

func (cr *CompactedFileReader) Header() LayoutHeader {
	return cr.header
}

func (cr *CompactedFileReader) GlobalsData() []byte {
	return cr.globals
}

func (cr *CompactedFileReader) Index() []NodeIndexEntry {
	return cr.index
}

func (cr *CompactedFileReader) NodeLevel(nodeID uint64) (int, bool) {
	entry := cr.LookupNode(nodeID)
	if entry == nil {
		return -1, false
	}
	return int(entry.Level), true
}

func (cr *CompactedFileReader) NodeCommitCount(nodeID uint64) (int, bool) {
	entry := cr.LookupNode(nodeID)
	if entry == nil {
		return 0, false
	}
	return int(entry.CommitCount), true
}

func (cr *CompactedFileReader) HasNode(nodeID uint64) bool {
	return cr.LookupNode(nodeID) != nil
}

func (cr *CompactedFileReader) IterateNodes(fn func(entry NodeIndexEntry, data []byte) error) error {
	for _, entry := range cr.index {
		data, err := cr.ReadNodeData(&entry)
		if err != nil {
			return fmt.Errorf("node %d: %w", entry.NodeID, err)
		}
		if err := fn(entry, data); err != nil {
			return err
		}
	}
	return nil
}

func (cr *CompactedFileReader) Summary() string {
	totalDataSize := int64(0)
	maxLevel := uint16(0)
	maxCommits := uint16(0)
	for _, e := range cr.index {
		totalDataSize += int64(e.DataLength)
		if e.Level > maxLevel {
			maxLevel = e.Level
		}
		if e.CommitCount > maxCommits {
			maxCommits = e.CommitCount
		}
	}

	return fmt.Sprintf("CompactedFile{nodes=%d maxLevel=%d maxCommits=%d globals=%dB data=%s total=%s}",
		len(cr.index), maxLevel, maxCommits,
		len(cr.globals), formatBytes(totalDataSize), formatBytes(int64(cr.header.TotalSize)))
}

func (cr *CompactedFileReader) Close() error {
	if cr.f != nil {
		return cr.f.Close()
	}
	return nil
}

func (cr *CompactedFileReader) readU64() uint64 {
	var buf [8]byte
	io.ReadFull(cr.f, buf[:])
	return binary.LittleEndian.Uint64(buf[:])
}

func (cr *CompactedFileReader) readU32() uint32 {
	var buf [4]byte
	io.ReadFull(cr.f, buf[:])
	return binary.LittleEndian.Uint32(buf[:])
}

func (cr *CompactedFileReader) readU16() uint16 {
	var buf [2]byte
	io.ReadFull(cr.f, buf[:])
	return binary.LittleEndian.Uint16(buf[:])
}
