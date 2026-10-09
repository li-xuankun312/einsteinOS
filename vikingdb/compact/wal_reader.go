package compact

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

type WALReader struct {
	r         io.Reader
	seqNo     uint64
	err       error
	corrupted int
}

func NewWALReader(r io.Reader) *WALReader {
	return &WALReader{r: r}
}

func OpenWALFile(path string) (*WALReader, io.Closer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	reader := NewWALReader(f)
	return reader, f, nil
}

func (r *WALReader) ReadHeader() error {
	magic := make([]byte, 4)
	if _, err := io.ReadFull(r.r, magic); err != nil {
		return fmt.Errorf("read magic: %w", err)
	}
	if string(magic) != walMagic {
		return fmt.Errorf("invalid WAL magic: %q", string(magic))
	}

	version := make([]byte, 1)
	if _, err := io.ReadFull(r.r, version); err != nil {
		return fmt.Errorf("read version: %w", err)
	}
	if version[0] > walVersion {
		return fmt.Errorf("unsupported WAL version: %d (max %d)", version[0], walVersion)
	}
	return nil
}

func (r *WALReader) ReadAll() ([]Commit, error) {
	if err := r.ReadHeader(); err != nil {
		return nil, err
	}

	var commits []Commit
	for {
		c, err := r.ReadCommit()
		if err == io.EOF {
			break
		}
		if err != nil {
			r.corrupted++
			if r.corrupted > 10 {
				return commits, fmt.Errorf("too many corrupted records (%d): %w", r.corrupted, err)
			}
			continue
		}
		r.seqNo++
		c.SeqNo = r.seqNo
		commits = append(commits, c)
	}
	return commits, nil
}

func (r *WALReader) ReadCommit() (Commit, error) {
	var crcAccum uint32
	c := Commit{}

	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(r.r, typeBuf); err != nil {
		return c, err
	}
	c.Type = CommitType(typeBuf[0])
	crcAccum = crc32.Update(crcAccum, crc32.IEEETable, typeBuf)

	var err error
	switch c.Type {
	case CommitAddNode:
		c.NodeID, crcAccum, err = r.readUint64CRC(crcAccum)

	case CommitAddNodeLevel:
		c.NodeID, crcAccum, err = r.readUint64CRC(crcAccum)
		if err == nil {
			var lvl uint16
			lvl, crcAccum, err = r.readUint16CRC(crcAccum)
			c.Level = int(lvl)
		}

	case CommitSetEntryPoint:
		c.NodeID, crcAccum, err = r.readUint64CRC(crcAccum)

	case CommitAddLink:
		c.NodeID, crcAccum, err = r.readUint64CRC(crcAccum)
		if err == nil {
			c.Target, crcAccum, err = r.readUint64CRC(crcAccum)
		}
		if err == nil {
			var lvl uint16
			lvl, crcAccum, err = r.readUint16CRC(crcAccum)
			c.Level = int(lvl)
		}

	case CommitReplaceLinks:
		c.NodeID, crcAccum, err = r.readUint64CRC(crcAccum)
		if err == nil {
			var lvl uint16
			lvl, crcAccum, err = r.readUint16CRC(crcAccum)
			c.Level = int(lvl)
		}
		if err == nil {
			var count uint16
			count, crcAccum, err = r.readUint16CRC(crcAccum)
			if err == nil {
				c.Links = make([]uint64, count)
				for i := 0; i < int(count); i++ {
					c.Links[i], crcAccum, err = r.readUint64CRC(crcAccum)
					if err != nil {
						break
					}
				}
			}
		}

	case CommitDeleteNode:
		c.NodeID, crcAccum, err = r.readUint64CRC(crcAccum)

	case CommitClearLinks:
		c.NodeID, crcAccum, err = r.readUint64CRC(crcAccum)
		if err == nil {
			var lvl uint16
			lvl, crcAccum, err = r.readUint16CRC(crcAccum)
			c.Level = int(lvl)
		}

	case CommitSetMaxLevel:
		var lvl uint16
		lvl, crcAccum, err = r.readUint16CRC(crcAccum)
		c.Level = int(lvl)

	case CommitAddTombstone:
		c.NodeID, crcAccum, err = r.readUint64CRC(crcAccum)

	case CommitRemoveTombstone:
		c.NodeID, crcAccum, err = r.readUint64CRC(crcAccum)

	case CommitResetIndex:

	case CommitSetEf:
		c.Value, crcAccum, err = r.readUint64CRC(crcAccum)

	default:
		return c, fmt.Errorf("unknown commit type: %d", c.Type)
	}

	if err != nil {
		return c, err
	}

	expectedCRC, err := r.readUint32()
	if err != nil {
		return c, fmt.Errorf("read checksum: %w", err)
	}
	if expectedCRC != crcAccum {
		return c, fmt.Errorf("CRC mismatch at seq %d: expected %08x got %08x", r.seqNo+1, expectedCRC, crcAccum)
	}

	return c, nil
}

func (r *WALReader) Corrupted() int {
	return r.corrupted
}

func (r *WALReader) readUint64CRC(crc uint32) (uint64, uint32, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r.r, buf[:]); err != nil {
		return 0, crc, err
	}
	crc = crc32.Update(crc, crc32.IEEETable, buf[:])
	return binary.LittleEndian.Uint64(buf[:]), crc, nil
}

func (r *WALReader) readUint16CRC(crc uint32) (uint16, uint32, error) {
	var buf [2]byte
	if _, err := io.ReadFull(r.r, buf[:]); err != nil {
		return 0, crc, err
	}
	crc = crc32.Update(crc, crc32.IEEETable, buf[:])
	return binary.LittleEndian.Uint16(buf[:]), crc, nil
}

func (r *WALReader) readUint32() (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r.r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

func CountCommits(path string) (int, int, error) {
	reader, closer, err := OpenWALFile(path)
	if err != nil {
		return 0, 0, err
	}
	defer closer.Close()

	commits, err := reader.ReadAll()
	return len(commits), reader.Corrupted(), err
}

func SplitByNode(commits []Commit) (globals []Commit, byNode map[uint64]*NodeCommits) {
	byNode = make(map[uint64]*NodeCommits)
	for _, c := range commits {
		if c.Type.IsGlobal() {
			globals = append(globals, c)
			continue
		}
		nc, ok := byNode[c.NodeID]
		if !ok {
			nc = &NodeCommits{NodeID: c.NodeID}
			byNode[c.NodeID] = nc
		}
		nc.Commits = append(nc.Commits, c)
	}
	return
}

func ValidateWAL(path string) (valid int, corrupted int, err error) {
	reader, closer, openErr := OpenWALFile(path)
	if openErr != nil {
		return 0, 0, openErr
	}
	defer closer.Close()

	commits, err := reader.ReadAll()
	return len(commits), reader.Corrupted(), err
}
