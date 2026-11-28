package compact

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"time"
)

const (
	snapMagic   = "SNAP"
	snapVersion = 1

	snapSectionHeader     uint8 = 1
	snapSectionNodes      uint8 = 2
	snapSectionLinks      uint8 = 3
	snapSectionTombstones uint8 = 4
	snapSectionMeta       uint8 = 5
	snapSectionEnd        uint8 = 255
)

type SnapshotHeader struct {
	Version      uint8
	NodeCount    uint64
	EntryPoint   uint64
	MaxLevel     int32
	M            int32
	MMax         int32
	MMax0        int32
	EfConstruct  int32
	EfSearch     int32
	ML           float64
	CreatedAt    int64
	Dimensions   int32
}

type SnapshotNode struct {
	ID     uint64
	ExtID  string
	Level  int
	Vector []float32
}

type SnapshotLink struct {
	NodeID      uint64
	Level       int
	Connections []uint64
}

type SnapshotMeta struct {
	NodeID uint64
	Key    string
	Value  interface{}
}

type SnapshotFileWriter struct {
	w         io.Writer
	header    SnapshotHeader
	nodeCount int
	linkCount int
	err       error
}

func NewSnapshotFileWriter(w io.Writer) *SnapshotFileWriter {
	return &SnapshotFileWriter{w: w}
}

func (sw *SnapshotFileWriter) WriteHeader(h SnapshotHeader) error {
	sw.header = h
	sw.write([]byte(snapMagic))
	sw.writeByte(snapVersion)
	sw.writeByte(snapSectionHeader)
	sw.writeUint64(h.NodeCount)
	sw.writeUint64(h.EntryPoint)
	sw.writeInt32(h.MaxLevel)
	sw.writeInt32(h.M)
	sw.writeInt32(h.MMax)
	sw.writeInt32(h.MMax0)
	sw.writeInt32(h.EfConstruct)
	sw.writeInt32(h.EfSearch)
	sw.writeFloat64(h.ML)
	sw.writeInt64(h.CreatedAt)
	sw.writeInt32(h.Dimensions)
	return sw.err
}

func (sw *SnapshotFileWriter) BeginNodes() error {
	sw.writeByte(snapSectionNodes)
	return sw.err
}

func (sw *SnapshotFileWriter) WriteNode(n SnapshotNode) error {
	sw.writeUint64(n.ID)
	sw.writeUint32(uint32(len(n.ExtID)))
	sw.write([]byte(n.ExtID))
	sw.writeInt32(int32(n.Level))
	sw.writeInt32(int32(len(n.Vector)))
	for _, v := range n.Vector {
		sw.writeFloat32(v)
	}
	sw.nodeCount++
	return sw.err
}

func (sw *SnapshotFileWriter) BeginLinks() error {
	sw.writeByte(snapSectionLinks)
	return sw.err
}

func (sw *SnapshotFileWriter) WriteLink(l SnapshotLink) error {
	sw.writeUint64(l.NodeID)
	sw.writeInt32(int32(l.Level))
	sw.writeUint32(uint32(len(l.Connections)))
	for _, c := range l.Connections {
		sw.writeUint64(c)
	}
	sw.linkCount++
	return sw.err
}

func (sw *SnapshotFileWriter) BeginTombstones() error {
	sw.writeByte(snapSectionTombstones)
	return sw.err
}

func (sw *SnapshotFileWriter) WriteTombstones(ids []uint64) error {
	sw.writeUint32(uint32(len(ids)))
	for _, id := range ids {
		sw.writeUint64(id)
	}
	return sw.err
}

func (sw *SnapshotFileWriter) BeginMeta() error {
	sw.writeByte(snapSectionMeta)
	return sw.err
}

func (sw *SnapshotFileWriter) WriteMeta(nodeID uint64, meta map[string]interface{}) error {
	sw.writeUint64(nodeID)
	sw.writeUint32(uint32(len(meta)))
	for k, v := range meta {
		sw.writeUint32(uint32(len(k)))
		sw.write([]byte(k))
		sw.writeMetaValue(v)
	}
	return sw.err
}

func (sw *SnapshotFileWriter) End() error {
	sw.writeByte(snapSectionEnd)
	return sw.err
}

func (sw *SnapshotFileWriter) write(b []byte) {
	if sw.err != nil {
		return
	}
	_, sw.err = sw.w.Write(b)
}

func (sw *SnapshotFileWriter) writeByte(b byte) {
	sw.write([]byte{b})
}

func (sw *SnapshotFileWriter) writeUint64(v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	sw.write(buf[:])
}

func (sw *SnapshotFileWriter) writeUint32(v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	sw.write(buf[:])
}

func (sw *SnapshotFileWriter) writeInt32(v int32) {
	sw.writeUint32(uint32(v))
}

func (sw *SnapshotFileWriter) writeInt64(v int64) {
	sw.writeUint64(uint64(v))
}

func (sw *SnapshotFileWriter) writeFloat32(v float32) {
	sw.writeUint32(math.Float32bits(v))
}

func (sw *SnapshotFileWriter) writeFloat64(v float64) {
	sw.writeUint64(math.Float64bits(v))
}

func (sw *SnapshotFileWriter) writeMetaValue(v interface{}) {
	switch val := v.(type) {
	case string:
		sw.writeByte(1)
		sw.writeUint32(uint32(len(val)))
		sw.write([]byte(val))
	case int:
		sw.writeByte(2)
		sw.writeUint64(uint64(val))
	case int64:
		sw.writeByte(2)
		sw.writeUint64(uint64(val))
	case float64:
		sw.writeByte(3)
		sw.writeFloat64(val)
	case bool:
		sw.writeByte(4)
		if val {
			sw.writeByte(1)
		} else {
			sw.writeByte(0)
		}
	default:
		sw.writeByte(1)
		s := fmt.Sprintf("%v", val)
		sw.writeUint32(uint32(len(s)))
		sw.write([]byte(s))
	}
}

type SnapshotFileReader struct {
	r      io.Reader
	Header SnapshotHeader
	err    error
}

func NewSnapshotFileReader(r io.Reader) *SnapshotFileReader {
	return &SnapshotFileReader{r: r}
}

func (sr *SnapshotFileReader) ReadHeader() error {
	magic := make([]byte, 4)
	sr.read(magic)
	if sr.err != nil {
		return sr.err
	}
	if string(magic) != snapMagic {
		return fmt.Errorf("invalid snapshot magic: %q", string(magic))
	}

	version := sr.readByte()
	if version > snapVersion {
		return fmt.Errorf("unsupported version: %d", version)
	}

	section := sr.readByte()
	if section != snapSectionHeader {
		return fmt.Errorf("expected header section, got %d", section)
	}

	sr.Header.Version = version
	sr.Header.NodeCount = sr.readUint64()
	sr.Header.EntryPoint = sr.readUint64()
	sr.Header.MaxLevel = sr.readInt32()
	sr.Header.M = sr.readInt32()
	sr.Header.MMax = sr.readInt32()
	sr.Header.MMax0 = sr.readInt32()
	sr.Header.EfConstruct = sr.readInt32()
	sr.Header.EfSearch = sr.readInt32()
	sr.Header.ML = sr.readFloat64()
	sr.Header.CreatedAt = int64(sr.readUint64())
	sr.Header.Dimensions = sr.readInt32()

	return sr.err
}

func (sr *SnapshotFileReader) ReadSection() (uint8, error) {
	section := sr.readByte()
	return section, sr.err
}

func (sr *SnapshotFileReader) ReadNode() (SnapshotNode, error) {
	n := SnapshotNode{}
	n.ID = sr.readUint64()
	extIDLen := sr.readUint32()
	extIDBuf := make([]byte, extIDLen)
	sr.read(extIDBuf)
	n.ExtID = string(extIDBuf)
	n.Level = int(sr.readInt32())
	vecLen := int(sr.readInt32())
	n.Vector = make([]float32, vecLen)
	for i := 0; i < vecLen; i++ {
		n.Vector[i] = sr.readFloat32()
	}
	return n, sr.err
}

func (sr *SnapshotFileReader) ReadLink() (SnapshotLink, error) {
	l := SnapshotLink{}
	l.NodeID = sr.readUint64()
	l.Level = int(sr.readInt32())
	connCount := int(sr.readUint32())
	l.Connections = make([]uint64, connCount)
	for i := 0; i < connCount; i++ {
		l.Connections[i] = sr.readUint64()
	}
	return l, sr.err
}

func (sr *SnapshotFileReader) ReadTombstones() ([]uint64, error) {
	count := int(sr.readUint32())
	ids := make([]uint64, count)
	for i := 0; i < count; i++ {
		ids[i] = sr.readUint64()
	}
	return ids, sr.err
}

func (sr *SnapshotFileReader) ReadMeta() (uint64, map[string]interface{}, error) {
	nodeID := sr.readUint64()
	count := int(sr.readUint32())
	meta := make(map[string]interface{}, count)
	for i := 0; i < count; i++ {
		keyLen := sr.readUint32()
		keyBuf := make([]byte, keyLen)
		sr.read(keyBuf)
		key := string(keyBuf)
		val := sr.readMetaValue()
		meta[key] = val
	}
	return nodeID, meta, sr.err
}

func (sr *SnapshotFileReader) read(buf []byte) {
	if sr.err != nil {
		return
	}
	_, sr.err = io.ReadFull(sr.r, buf)
}

func (sr *SnapshotFileReader) readByte() byte {
	buf := make([]byte, 1)
	sr.read(buf)
	return buf[0]
}

func (sr *SnapshotFileReader) readUint64() uint64 {
	var buf [8]byte
	sr.read(buf[:])
	if sr.err != nil {
		return 0
	}
	return binary.LittleEndian.Uint64(buf[:])
}

func (sr *SnapshotFileReader) readUint32() uint32 {
	var buf [4]byte
	sr.read(buf[:])
	if sr.err != nil {
		return 0
	}
	return binary.LittleEndian.Uint32(buf[:])
}

func (sr *SnapshotFileReader) readInt32() int32 {
	return int32(sr.readUint32())
}

func (sr *SnapshotFileReader) readFloat32() float32 {
	return math.Float32frombits(sr.readUint32())
}

func (sr *SnapshotFileReader) readFloat64() float64 {
	return math.Float64frombits(sr.readUint64())
}

func (sr *SnapshotFileReader) readMetaValue() interface{} {
	typ := sr.readByte()
	switch typ {
	case 1:
		length := sr.readUint32()
		buf := make([]byte, length)
		sr.read(buf)
		return string(buf)
	case 2:
		return int64(sr.readUint64())
	case 3:
		return sr.readFloat64()
	case 4:
		return sr.readByte() == 1
	default:
		return nil
	}
}

func WriteSnapshotFile(path string, header SnapshotHeader,
	nodes []SnapshotNode, links []SnapshotLink,
	tombstones []uint64, metas map[uint64]map[string]interface{}) error {

	w, err := NewSafeFileWriter(path, 256*1024)
	if err != nil {
		return err
	}

	sw := NewSnapshotFileWriter(w)

	header.CreatedAt = time.Now().UnixNano()
	if err := sw.WriteHeader(header); err != nil {
		w.Abort()
		return err
	}

	if err := sw.BeginNodes(); err != nil {
		w.Abort()
		return err
	}
	for _, n := range nodes {
		if err := sw.WriteNode(n); err != nil {
			w.Abort()
			return err
		}
	}

	if err := sw.BeginLinks(); err != nil {
		w.Abort()
		return err
	}
	for _, l := range links {
		if err := sw.WriteLink(l); err != nil {
			w.Abort()
			return err
		}
	}

	if len(tombstones) > 0 {
		if err := sw.BeginTombstones(); err != nil {
			w.Abort()
			return err
		}
		if err := sw.WriteTombstones(tombstones); err != nil {
			w.Abort()
			return err
		}
	}

	if len(metas) > 0 {
		if err := sw.BeginMeta(); err != nil {
			w.Abort()
			return err
		}
		for nodeID, meta := range metas {
			if err := sw.WriteMeta(nodeID, meta); err != nil {
				w.Abort()
				return err
			}
		}
	}

	if err := sw.End(); err != nil {
		w.Abort()
		return err
	}

	return w.Commit()
}

func ReadSnapshotFile(path string) (*SnapshotHeader, []SnapshotNode, []SnapshotLink,
	[]uint64, map[uint64]map[string]interface{}, error) {

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	defer f.Close()

	sr := NewSnapshotFileReader(f)
	if err := sr.ReadHeader(); err != nil {
		return nil, nil, nil, nil, nil, err
	}

	var nodes []SnapshotNode
	var links []SnapshotLink
	var tombstones []uint64
	metas := make(map[uint64]map[string]interface{})

	for {
		section, err := sr.ReadSection()
		if err != nil {
			return &sr.Header, nodes, links, tombstones, metas, err
		}

		switch section {
		case snapSectionNodes:
			for i := uint64(0); i < sr.Header.NodeCount; i++ {
				n, err := sr.ReadNode()
				if err != nil {
					return &sr.Header, nodes, links, tombstones, metas, err
				}
				nodes = append(nodes, n)
			}

		case snapSectionLinks:
			for range nodes {
				l, err := sr.ReadLink()
				if err != nil {
					break
				}
				links = append(links, l)
			}

		case snapSectionTombstones:
			tombstones, err = sr.ReadTombstones()
			if err != nil {
				return &sr.Header, nodes, links, tombstones, metas, err
			}

		case snapSectionMeta:
			for range nodes {
				nodeID, meta, err := sr.ReadMeta()
				if err != nil {
					break
				}
				metas[nodeID] = meta
			}

		case snapSectionEnd:
			return &sr.Header, nodes, links, tombstones, metas, nil

		default:
			return &sr.Header, nodes, links, tombstones, metas,
				fmt.Errorf("unknown section: %d", section)
		}
	}
}
