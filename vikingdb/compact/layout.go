package compact

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
)

const (
	layoutMagic   = "HLAY"
	layoutVersion = 1

	sectionIndex     uint8 = 1
	sectionData      uint8 = 2
	sectionNodeIndex uint8 = 3
	sectionGlobals   uint8 = 4
)

type LayoutHeader struct {
	Magic       [4]byte
	Version     uint8
	NodeCount   uint64
	GlobalCount uint32
	IndexOffset uint64
	DataOffset  uint64
	TotalSize   uint64
}

type NodeIndexEntry struct {
	NodeID     uint64
	DataOffset uint64
	DataLength uint32
	CommitCount uint16
	Level       uint16
}

type CompactedLayout struct {
	header  LayoutHeader
	index   []NodeIndexEntry
	globals []Commit
}

func NewCompactedLayout() *CompactedLayout {
	return &CompactedLayout{}
}

type LayoutWriter struct {
	w             io.Writer
	nodeOffsets   []NodeIndexEntry
	globalBuf     []byte
	dataBuf       []byte
	dataOffset    uint64
	commitCount   int
}

func NewLayoutWriter(w io.Writer) *LayoutWriter {
	return &LayoutWriter{w: w}
}

func (lw *LayoutWriter) AddGlobals(commits []Commit) {
	for _, c := range commits {
		lw.globalBuf = appendCommitBytes(lw.globalBuf, c)
	}
}

func (lw *LayoutWriter) AddNode(nc *NodeCommits) {
	startOffset := uint64(len(lw.dataBuf))
	for _, c := range nc.Commits {
		lw.dataBuf = appendCommitBytes(lw.dataBuf, c)
	}
	endOffset := uint64(len(lw.dataBuf))

	level := uint16(0)
	for _, c := range nc.Commits {
		if c.Type == CommitAddNodeLevel && uint16(c.Level) > level {
			level = uint16(c.Level)
		}
	}

	lw.nodeOffsets = append(lw.nodeOffsets, NodeIndexEntry{
		NodeID:      nc.NodeID,
		DataOffset:  startOffset,
		DataLength:  uint32(endOffset - startOffset),
		CommitCount: uint16(len(nc.Commits)),
		Level:       level,
	})
	lw.commitCount += len(nc.Commits)
}

func (lw *LayoutWriter) Finalize() error {
	sort.Slice(lw.nodeOffsets, func(i, j int) bool {
		return lw.nodeOffsets[i].NodeID < lw.nodeOffsets[j].NodeID
	})

	header := LayoutHeader{
		Version:     layoutVersion,
		NodeCount:   uint64(len(lw.nodeOffsets)),
		GlobalCount: uint32(len(lw.globalBuf)),
	}
	copy(header.Magic[:], layoutMagic)

	headerSize := uint64(37)
	globalSize := uint64(len(lw.globalBuf))
	indexEntrySize := uint64(22)
	indexSize := uint64(len(lw.nodeOffsets)) * indexEntrySize
	dataSize := uint64(len(lw.dataBuf))

	header.IndexOffset = headerSize + 1 + globalSize + 1
	header.DataOffset = header.IndexOffset + 1 + indexSize
	header.TotalSize = header.DataOffset + 1 + dataSize

	if err := writeLayoutHeader(lw.w, header); err != nil {
		return err
	}

	lw.w.Write([]byte{sectionGlobals})
	lw.w.Write(lw.globalBuf)

	lw.w.Write([]byte{sectionNodeIndex})
	for _, entry := range lw.nodeOffsets {
		writeNodeIndexEntry(lw.w, entry)
	}

	lw.w.Write([]byte{sectionData})
	lw.w.Write(lw.dataBuf)

	return nil
}

func writeLayoutHeader(w io.Writer, h LayoutHeader) error {
	w.Write(h.Magic[:])
	w.Write([]byte{h.Version})
	writeU64(w, h.NodeCount)
	writeU32(w, h.GlobalCount)
	writeU64(w, h.IndexOffset)
	writeU64(w, h.DataOffset)
	writeU64(w, h.TotalSize)
	return nil
}

func writeNodeIndexEntry(w io.Writer, e NodeIndexEntry) {
	writeU64(w, e.NodeID)
	writeU64(w, e.DataOffset)
	writeU32(w, e.DataLength)
	writeU16(w, e.CommitCount)
	writeU16(w, e.Level)
}

func writeU64(w io.Writer, v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	w.Write(buf[:])
}

func writeU32(w io.Writer, v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	w.Write(buf[:])
}

func writeU16(w io.Writer, v uint16) {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], v)
	w.Write(buf[:])
}

type LayoutReader struct {
	r       io.ReadSeeker
	header  LayoutHeader
	index   []NodeIndexEntry
	globals []byte
}

func NewLayoutReader(r io.ReadSeeker) *LayoutReader {
	return &LayoutReader{r: r}
}

func (lr *LayoutReader) ReadHeader() error {
	var magic [4]byte
	if _, err := io.ReadFull(lr.r, magic[:]); err != nil {
		return err
	}
	if string(magic[:]) != layoutMagic {
		return fmt.Errorf("invalid layout magic: %q", string(magic[:]))
	}

	ver := make([]byte, 1)
	io.ReadFull(lr.r, ver)
	lr.header.Version = ver[0]

	lr.header.NodeCount = readU64(lr.r)
	lr.header.GlobalCount = readU32(lr.r)
	lr.header.IndexOffset = readU64(lr.r)
	lr.header.DataOffset = readU64(lr.r)
	lr.header.TotalSize = readU64(lr.r)

	return nil
}

func (lr *LayoutReader) ReadGlobals() ([]byte, error) {
	section := make([]byte, 1)
	io.ReadFull(lr.r, section)
	if section[0] != sectionGlobals {
		return nil, fmt.Errorf("expected globals section, got %d", section[0])
	}

	data := make([]byte, lr.header.GlobalCount)
	if _, err := io.ReadFull(lr.r, data); err != nil {
		return nil, err
	}
	lr.globals = data
	return data, nil
}

func (lr *LayoutReader) ReadIndex() ([]NodeIndexEntry, error) {
	section := make([]byte, 1)
	io.ReadFull(lr.r, section)
	if section[0] != sectionNodeIndex {
		return nil, fmt.Errorf("expected index section, got %d", section[0])
	}

	entries := make([]NodeIndexEntry, lr.header.NodeCount)
	for i := uint64(0); i < lr.header.NodeCount; i++ {
		entries[i] = readNodeIndexEntry(lr.r)
	}
	lr.index = entries
	return entries, nil
}

func readNodeIndexEntry(r io.Reader) NodeIndexEntry {
	return NodeIndexEntry{
		NodeID:      readU64(r),
		DataOffset:  readU64(r),
		DataLength:  readU32(r),
		CommitCount: readU16(r),
		Level:       readU16(r),
	}
}

func (lr *LayoutReader) LookupNode(nodeID uint64) *NodeIndexEntry {
	idx := sort.Search(len(lr.index), func(i int) bool {
		return lr.index[i].NodeID >= nodeID
	})
	if idx < len(lr.index) && lr.index[idx].NodeID == nodeID {
		return &lr.index[idx]
	}
	return nil
}

func (lr *LayoutReader) ReadNodeData(entry *NodeIndexEntry) ([]byte, error) {
	offset := int64(lr.header.DataOffset) + 1 + int64(entry.DataOffset)
	if _, err := lr.r.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data := make([]byte, entry.DataLength)
	if _, err := io.ReadFull(lr.r, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (lr *LayoutReader) Header() LayoutHeader {
	return lr.header
}

func readU64(r io.Reader) uint64 {
	var buf [8]byte
	io.ReadFull(r, buf[:])
	return binary.LittleEndian.Uint64(buf[:])
}

func readU32(r io.Reader) uint32 {
	var buf [4]byte
	io.ReadFull(r, buf[:])
	return binary.LittleEndian.Uint32(buf[:])
}

func readU16(r io.Reader) uint16 {
	var buf [2]byte
	io.ReadFull(r, buf[:])
	return binary.LittleEndian.Uint16(buf[:])
}

func appendCommitBytes(buf []byte, c Commit) []byte {
	buf = append(buf, byte(c.Type))
	switch c.Type {
	case CommitAddNode:
		buf = appendU64(buf, c.NodeID)
	case CommitAddNodeLevel:
		buf = appendU64(buf, c.NodeID)
		buf = appendU16(buf, uint16(c.Level))
	case CommitSetEntryPoint:
		buf = appendU64(buf, c.NodeID)
	case CommitAddLink:
		buf = appendU64(buf, c.NodeID)
		buf = appendU64(buf, c.Target)
		buf = appendU16(buf, uint16(c.Level))
	case CommitReplaceLinks:
		buf = appendU64(buf, c.NodeID)
		buf = appendU16(buf, uint16(c.Level))
		buf = appendU16(buf, uint16(len(c.Links)))
		for _, l := range c.Links {
			buf = appendU64(buf, l)
		}
	case CommitDeleteNode:
		buf = appendU64(buf, c.NodeID)
	case CommitClearLinks:
		buf = appendU64(buf, c.NodeID)
		buf = appendU16(buf, uint16(c.Level))
	case CommitSetMaxLevel:
		buf = appendU16(buf, uint16(c.Level))
	case CommitAddTombstone:
		buf = appendU64(buf, c.NodeID)
	case CommitRemoveTombstone:
		buf = appendU64(buf, c.NodeID)
	case CommitSetEf:
		buf = appendU64(buf, c.Value)
	}
	return buf
}

func appendU64(buf []byte, v uint64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	return append(buf, b[:]...)
}

func appendU16(buf []byte, v uint16) []byte {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	return append(buf, b[:]...)
}

func WriteCompactedFile(path string, globals []Commit, nodes []*NodeCommits) error {
	w, err := NewSafeFileWriter(path, 256*1024)
	if err != nil {
		return err
	}

	lw := NewLayoutWriter(w)
	lw.AddGlobals(globals)
	for _, nc := range nodes {
		lw.AddNode(nc)
	}

	if err := lw.Finalize(); err != nil {
		w.Abort()
		return err
	}

	return w.Commit()
}

func ReadCompactedFile(path string) (*LayoutReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	lr := NewLayoutReader(f)
	if err := lr.ReadHeader(); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := lr.ReadGlobals(); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := lr.ReadIndex(); err != nil {
		f.Close()
		return nil, err
	}

	return lr, nil
}
