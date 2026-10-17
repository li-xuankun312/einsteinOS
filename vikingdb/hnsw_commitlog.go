package vikingdb

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	opAddNode          uint8 = 1
	opSetEntryPoint    uint8 = 2
	opAddLink          uint8 = 3
	opReplaceLinks     uint8 = 4
	opDeleteNode       uint8 = 5
	opClearLinks       uint8 = 6
	opSetMaxLevel      uint8 = 7
	opAddTombstone     uint8 = 8
	opRemoveTombstone  uint8 = 9
	opResetIndex       uint8 = 10
	opSetEf            uint8 = 11
	opAddNodeWithLevel uint8 = 12

	commitLogVersion uint8 = 2
	commitLogMagic         = "HNSW"
)

type CommitLogger struct {
	mu       sync.Mutex
	file     *os.File
	writer   *bufio.Writer
	dir      string
	id       string
	seqNo    uint64
	maxSize  int64
	curSize  int64
	closed   bool
}

func NewCommitLogger(dir, id string, maxSize int64) (*CommitLogger, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create commitlog dir: %w", err)
	}

	if maxSize <= 0 {
		maxSize = 64 * 1024 * 1024
	}

	cl := &CommitLogger{
		dir:     dir,
		id:      id,
		maxSize: maxSize,
	}

	if err := cl.openNewFile(); err != nil {
		return nil, err
	}

	return cl, nil
}

func (cl *CommitLogger) openNewFile() error {
	cl.seqNo++
	name := fmt.Sprintf("%s_%d_%d.wal", cl.id, time.Now().UnixNano(), cl.seqNo)
	path := filepath.Join(cl.dir, name)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open commitlog: %w", err)
	}

	if cl.file != nil {
		cl.writer.Flush()
		cl.file.Close()
	}

	cl.file = f
	cl.writer = bufio.NewWriterSize(f, 32*1024)
	cl.curSize = 0

	cl.writer.Write([]byte(commitLogMagic))
	cl.writer.WriteByte(commitLogVersion)
	cl.curSize += 5

	return nil
}

func (cl *CommitLogger) maybeRotate() error {
	if cl.curSize >= cl.maxSize {
		return cl.openNewFile()
	}
	return nil
}

func (cl *CommitLogger) AddNode(id uint64, level int) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return fmt.Errorf("commitlog closed")
	}
	cl.writer.WriteByte(opAddNodeWithLevel)
	writeUint64(cl.writer, id)
	writeUint16(cl.writer, uint16(level))
	cl.curSize += 11
	return cl.maybeRotate()
}

func (cl *CommitLogger) SetEntryPoint(id uint64) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return fmt.Errorf("commitlog closed")
	}
	cl.writer.WriteByte(opSetEntryPoint)
	writeUint64(cl.writer, id)
	cl.curSize += 9
	return nil
}

func (cl *CommitLogger) AddLink(from, to uint64, level int) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return fmt.Errorf("commitlog closed")
	}
	cl.writer.WriteByte(opAddLink)
	writeUint64(cl.writer, from)
	writeUint64(cl.writer, to)
	writeUint16(cl.writer, uint16(level))
	cl.curSize += 19
	return nil
}

func (cl *CommitLogger) ReplaceLinksAtLevel(id uint64, level int, links []uint64) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return fmt.Errorf("commitlog closed")
	}
	cl.writer.WriteByte(opReplaceLinks)
	writeUint64(cl.writer, id)
	writeUint16(cl.writer, uint16(level))
	writeUint16(cl.writer, uint16(len(links)))
	for _, link := range links {
		writeUint64(cl.writer, link)
	}
	cl.curSize += int64(13 + 8*len(links))
	return cl.maybeRotate()
}

func (cl *CommitLogger) DeleteNode(id uint64) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return fmt.Errorf("commitlog closed")
	}
	cl.writer.WriteByte(opDeleteNode)
	writeUint64(cl.writer, id)
	cl.curSize += 9
	return nil
}

func (cl *CommitLogger) AddTombstone(id uint64) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return fmt.Errorf("commitlog closed")
	}
	cl.writer.WriteByte(opAddTombstone)
	writeUint64(cl.writer, id)
	cl.curSize += 9
	return nil
}

func (cl *CommitLogger) RemoveTombstone(id uint64) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return fmt.Errorf("commitlog closed")
	}
	cl.writer.WriteByte(opRemoveTombstone)
	writeUint64(cl.writer, id)
	cl.curSize += 9
	return nil
}

func (cl *CommitLogger) SetMaxLevel(level int) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return fmt.Errorf("commitlog closed")
	}
	cl.writer.WriteByte(opSetMaxLevel)
	writeUint16(cl.writer, uint16(level))
	cl.curSize += 3
	return nil
}

func (cl *CommitLogger) SetEf(ef int) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return fmt.Errorf("commitlog closed")
	}
	cl.writer.WriteByte(opSetEf)
	writeUint64(cl.writer, uint64(ef))
	cl.curSize += 9
	return nil
}

func (cl *CommitLogger) ResetIndex() error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return fmt.Errorf("commitlog closed")
	}
	cl.writer.WriteByte(opResetIndex)
	cl.curSize += 1
	return nil
}

func (cl *CommitLogger) Flush() error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return nil
	}
	return cl.writer.Flush()
}

func (cl *CommitLogger) Close() error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return nil
	}
	cl.closed = true
	cl.writer.Flush()
	return cl.file.Close()
}

func (cl *CommitLogger) Size() int64 {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.curSize
}

func writeUint64(w *bufio.Writer, v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	w.Write(buf[:])
}

func writeUint16(w *bufio.Writer, v uint16) {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], v)
	w.Write(buf[:])
}

type CommitLogEntry struct {
	Op     uint8
	NodeID uint64
	Level  int
	Links  []uint64
	To     uint64
	Ef     int
}

type CommitLogReader struct {
	reader *bufio.Reader
	err    error
}

func NewCommitLogReader(r io.Reader) *CommitLogReader {
	return &CommitLogReader{reader: bufio.NewReader(r)}
}

func ReadCommitLog(path string) ([]CommitLogEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := NewCommitLogReader(f)
	return reader.ReadAll()
}

func (r *CommitLogReader) ReadAll() ([]CommitLogEntry, error) {
	magic := make([]byte, 4)
	if _, err := io.ReadFull(r.reader, magic); err != nil {
		return nil, fmt.Errorf("read magic: %w", err)
	}
	if string(magic) != commitLogMagic {
		return nil, fmt.Errorf("invalid commitlog magic: %s", string(magic))
	}

	version, err := r.reader.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}
	if version > commitLogVersion {
		return nil, fmt.Errorf("unsupported commitlog version: %d", version)
	}

	var entries []CommitLogEntry
	for {
		entry, err := r.readEntry()
		if err == io.EOF {
			break
		}
		if err != nil {
			return entries, fmt.Errorf("read entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (r *CommitLogReader) readEntry() (CommitLogEntry, error) {
	op, err := r.reader.ReadByte()
	if err != nil {
		return CommitLogEntry{}, err
	}

	entry := CommitLogEntry{Op: op}

	switch op {
	case opAddNode:
		entry.NodeID, err = readUint64(r.reader)
	case opAddNodeWithLevel:
		entry.NodeID, err = readUint64(r.reader)
		if err == nil {
			var lvl uint16
			lvl, err = readUint16(r.reader)
			entry.Level = int(lvl)
		}
	case opSetEntryPoint:
		entry.NodeID, err = readUint64(r.reader)
	case opAddLink:
		entry.NodeID, err = readUint64(r.reader)
		if err == nil {
			entry.To, err = readUint64(r.reader)
		}
		if err == nil {
			var lvl uint16
			lvl, err = readUint16(r.reader)
			entry.Level = int(lvl)
		}
	case opReplaceLinks:
		entry.NodeID, err = readUint64(r.reader)
		if err == nil {
			var lvl uint16
			lvl, err = readUint16(r.reader)
			entry.Level = int(lvl)
		}
		if err == nil {
			var count uint16
			count, err = readUint16(r.reader)
			if err == nil {
				entry.Links = make([]uint64, count)
				for i := 0; i < int(count); i++ {
					entry.Links[i], err = readUint64(r.reader)
					if err != nil {
						break
					}
				}
			}
		}
	case opDeleteNode:
		entry.NodeID, err = readUint64(r.reader)
	case opSetMaxLevel:
		var lvl uint16
		lvl, err = readUint16(r.reader)
		entry.Level = int(lvl)
	case opAddTombstone:
		entry.NodeID, err = readUint64(r.reader)
	case opRemoveTombstone:
		entry.NodeID, err = readUint64(r.reader)
	case opResetIndex:
	case opSetEf:
		var ef uint64
		ef, err = readUint64(r.reader)
		entry.Ef = int(ef)
	default:
		err = fmt.Errorf("unknown op: %d", op)
	}

	return entry, err
}

func readUint64(r *bufio.Reader) (uint64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

func readUint16(r *bufio.Reader) (uint16, error) {
	var buf [2]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf[:]), nil
}

func ListCommitLogs(dir, id string) ([]string, error) {
	pattern := filepath.Join(dir, id+"_*.wal")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	return matches, nil
}

type NoopCommitLogger struct{}

func (n *NoopCommitLogger) AddNode(id uint64, level int) error            { return nil }
func (n *NoopCommitLogger) SetEntryPoint(id uint64) error                  { return nil }
func (n *NoopCommitLogger) AddLink(from, to uint64, level int) error       { return nil }
func (n *NoopCommitLogger) ReplaceLinksAtLevel(id uint64, level int, links []uint64) error {
	return nil
}
func (n *NoopCommitLogger) DeleteNode(id uint64) error       { return nil }
func (n *NoopCommitLogger) AddTombstone(id uint64) error     { return nil }
func (n *NoopCommitLogger) RemoveTombstone(id uint64) error  { return nil }
func (n *NoopCommitLogger) SetMaxLevel(level int) error      { return nil }
func (n *NoopCommitLogger) SetEf(ef int) error               { return nil }
func (n *NoopCommitLogger) ResetIndex() error                { return nil }
func (n *NoopCommitLogger) Flush() error                     { return nil }
func (n *NoopCommitLogger) Close() error                     { return nil }
