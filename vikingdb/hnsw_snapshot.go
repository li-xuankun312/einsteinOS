package vikingdb

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
)

const (
	snapshotMagic   = "HSNP"
	snapshotVersion = 1
)

type SnapshotWriter struct {
	w   io.Writer
	err error
}

func NewSnapshotWriter(w io.Writer) *SnapshotWriter {
	return &SnapshotWriter{w: w}
}

func (sw *SnapshotWriter) Write(h *HNSW) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sw.writeBytes([]byte(snapshotMagic))
	sw.writeByte(snapshotVersion)
	sw.writeUint64(h.nodeCount)
	sw.writeUint64(h.entryPoint)
	sw.writeInt32(int32(h.maxLevel))
	sw.writeInt32(int32(h.cfg.M))
	sw.writeInt32(int32(h.cfg.MMax))
	sw.writeInt32(int32(h.cfg.MMax0))
	sw.writeInt32(int32(h.cfg.EfConstruction))
	sw.writeInt32(int32(h.cfg.EfSearch))
	sw.writeFloat64(h.cfg.ML)

	if sw.err != nil {
		return sw.err
	}

	idMapping := make(map[uint64]uint64)
	seqID := uint64(0)
	for id := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		idMapping[id] = seqID
		seqID++
	}

	sw.writeUint64(seqID)

	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		extID := h.reverseMap[id]

		sw.writeUint64(idMapping[id])
		sw.writeString(extID)
		sw.writeInt32(int32(node.level))
		sw.writeInt32(int32(len(node.vector)))
		for _, v := range node.vector {
			sw.writeFloat32(v)
		}

		for level := 0; level <= node.level; level++ {
			conns := node.neighborsAtLevel(level)
			validConns := make([]uint64, 0, len(conns))
			for _, c := range conns {
				if mappedID, ok := idMapping[c]; ok {
					validConns = append(validConns, mappedID)
				}
			}
			sw.writeUint32(uint32(len(validConns)))
			for _, c := range validConns {
				sw.writeUint64(c)
			}
		}

		if meta, ok := h.meta[id]; ok {
			sw.writeUint32(uint32(len(meta)))
			for k, v := range meta {
				sw.writeString(k)
				sw.writeMetaValue(v)
			}
		} else {
			sw.writeUint32(0)
		}

		if sw.err != nil {
			return sw.err
		}
	}

	tombstoneList := h.tombstones.List()
	sw.writeUint32(uint32(len(tombstoneList)))
	for _, id := range tombstoneList {
		sw.writeUint64(id)
	}

	return sw.err
}

func (sw *SnapshotWriter) writeBytes(b []byte) {
	if sw.err != nil {
		return
	}
	_, sw.err = sw.w.Write(b)
}

func (sw *SnapshotWriter) writeByte(b byte) {
	if sw.err != nil {
		return
	}
	_, sw.err = sw.w.Write([]byte{b})
}

func (sw *SnapshotWriter) writeUint64(v uint64) {
	if sw.err != nil {
		return
	}
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	_, sw.err = sw.w.Write(buf[:])
}

func (sw *SnapshotWriter) writeUint32(v uint32) {
	if sw.err != nil {
		return
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	_, sw.err = sw.w.Write(buf[:])
}

func (sw *SnapshotWriter) writeInt32(v int32) {
	sw.writeUint32(uint32(v))
}

func (sw *SnapshotWriter) writeFloat32(v float32) {
	sw.writeUint32(math.Float32bits(v))
}

func (sw *SnapshotWriter) writeFloat64(v float64) {
	sw.writeUint64(math.Float64bits(v))
}

func (sw *SnapshotWriter) writeString(s string) {
	sw.writeUint32(uint32(len(s)))
	sw.writeBytes([]byte(s))
}

func (sw *SnapshotWriter) writeMetaValue(v interface{}) {
	switch val := v.(type) {
	case string:
		sw.writeByte(1)
		sw.writeString(val)
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
		sw.writeString(fmt.Sprintf("%v", val))
	}
}

type SnapshotReader struct {
	r   io.Reader
	err error
}

func NewSnapshotReader(r io.Reader) *SnapshotReader {
	return &SnapshotReader{r: r}
}

func (sr *SnapshotReader) Read() (*HNSW, error) {
	magic := make([]byte, 4)
	sr.readBytes(magic)
	if sr.err != nil {
		return nil, sr.err
	}
	if string(magic) != snapshotMagic {
		return nil, fmt.Errorf("invalid snapshot magic: %s", string(magic))
	}

	version := sr.readByte()
	if version > snapshotVersion {
		return nil, fmt.Errorf("unsupported snapshot version: %d", version)
	}

	nodeCount := sr.readUint64()
	entryPoint := sr.readUint64()
	maxLevel := sr.readInt32()
	m := sr.readInt32()
	mMax := sr.readInt32()
	mMax0 := sr.readInt32()
	efConstruction := sr.readInt32()
	efSearch := sr.readInt32()
	ml := sr.readFloat64()

	if sr.err != nil {
		return nil, sr.err
	}

	cfg := HNSWConfig{
		M:              int(m),
		MMax:           int(mMax),
		MMax0:          int(mMax0),
		EfConstruction: int(efConstruction),
		EfSearch:       int(efSearch),
		ML:             ml,
		Dist:           L2Distance,
	}

	h := NewHNSW(cfg)
	h.entryPoint = entryPoint
	h.maxLevel = int(maxLevel)
	h.nodeCount = nodeCount

	actualCount := sr.readUint64()
	if sr.err != nil {
		return nil, sr.err
	}

	for i := uint64(0); i < actualCount; i++ {
		seqID := sr.readUint64()
		extID := sr.readString()
		level := int(sr.readInt32())
		vecLen := int(sr.readInt32())

		vec := make(Vector, vecLen)
		for j := 0; j < vecLen; j++ {
			vec[j] = sr.readFloat32()
		}

		node := &hnswNode{
			id:          seqID,
			level:       level,
			connections: make([][]uint64, level+1),
			vector:      vec,
		}

		for lv := 0; lv <= level; lv++ {
			connCount := int(sr.readUint32())
			conns := make([]uint64, connCount)
			for c := 0; c < connCount; c++ {
				conns[c] = sr.readUint64()
			}
			node.connections[lv] = conns
		}

		metaCount := int(sr.readUint32())
		var meta map[string]interface{}
		if metaCount > 0 {
			meta = make(map[string]interface{}, metaCount)
			for m := 0; m < metaCount; m++ {
				key := sr.readString()
				val := sr.readMetaValue()
				meta[key] = val
			}
		}

		if sr.err != nil {
			return nil, sr.err
		}

		h.nodes[seqID] = node
		h.idMap[extID] = seqID
		h.reverseMap[seqID] = extID
		if meta != nil {
			h.meta[seqID] = meta
		}
		h.nextID.Store(seqID + 1)
	}

	tombstoneCount := int(sr.readUint32())
	for t := 0; t < tombstoneCount; t++ {
		id := sr.readUint64()
		h.tombstones.Add(id)
	}

	return h, sr.err
}

func (sr *SnapshotReader) readBytes(buf []byte) {
	if sr.err != nil {
		return
	}
	_, sr.err = io.ReadFull(sr.r, buf)
}

func (sr *SnapshotReader) readByte() byte {
	buf := make([]byte, 1)
	sr.readBytes(buf)
	return buf[0]
}

func (sr *SnapshotReader) readUint64() uint64 {
	var buf [8]byte
	sr.readBytes(buf[:])
	if sr.err != nil {
		return 0
	}
	return binary.LittleEndian.Uint64(buf[:])
}

func (sr *SnapshotReader) readUint32() uint32 {
	var buf [4]byte
	sr.readBytes(buf[:])
	if sr.err != nil {
		return 0
	}
	return binary.LittleEndian.Uint32(buf[:])
}

func (sr *SnapshotReader) readInt32() int32 {
	return int32(sr.readUint32())
}

func (sr *SnapshotReader) readFloat32() float32 {
	return math.Float32frombits(sr.readUint32())
}

func (sr *SnapshotReader) readFloat64() float64 {
	return math.Float64frombits(sr.readUint64())
}

func (sr *SnapshotReader) readString() string {
	length := sr.readUint32()
	if sr.err != nil || length == 0 {
		return ""
	}
	buf := make([]byte, length)
	sr.readBytes(buf)
	return string(buf)
}

func (sr *SnapshotReader) readMetaValue() interface{} {
	typ := sr.readByte()
	switch typ {
	case 1:
		return sr.readString()
	case 2:
		return int64(sr.readUint64())
	case 3:
		return sr.readFloat64()
	case 4:
		b := sr.readByte()
		return b == 1
	default:
		return nil
	}
}

func SaveSnapshot(h *HNSW, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sw := NewSnapshotWriter(f)
	return sw.Write(h)
}

func LoadSnapshot(path string, distFunc DistFunc) (*HNSW, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sr := NewSnapshotReader(f)
	h, err := sr.Read()
	if err != nil {
		return nil, err
	}
	if distFunc != nil {
		h.cfg.Dist = distFunc
	}
	return h, nil
}

func (h *HNSW) SaveTo(path string) error {
	return SaveSnapshot(h, path)
}

func LoadFrom(path string) (*HNSW, error) {
	return LoadSnapshot(path, nil)
}

func snapshotPath(dir, id string) string {
	return filepath.Join(dir, id+".hnsw.snap")
}

var _ atomic.Uint64 // ensure import
