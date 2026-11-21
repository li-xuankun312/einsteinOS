package compact

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

const (
	walMagic   = "HWAL"
	walVersion = 2
)

type WALWriter struct {
	w       io.Writer
	crc     uint32
	written int64
}

func NewWALWriter(w io.Writer) *WALWriter {
	return &WALWriter{w: w}
}

func (w *WALWriter) WriteHeader() error {
	if _, err := w.w.Write([]byte(walMagic)); err != nil {
		return err
	}
	w.written += 4
	return w.writeByte(walVersion)
}

func (w *WALWriter) WriteCommit(c Commit) error {
	w.crc = 0

	if err := w.writeByteWithCRC(byte(c.Type)); err != nil {
		return err
	}

	switch c.Type {
	case CommitAddNode:
		if err := w.writeUint64WithCRC(c.NodeID); err != nil {
			return err
		}

	case CommitAddNodeLevel:
		if err := w.writeUint64WithCRC(c.NodeID); err != nil {
			return err
		}
		if err := w.writeUint16WithCRC(uint16(c.Level)); err != nil {
			return err
		}

	case CommitSetEntryPoint:
		if err := w.writeUint64WithCRC(c.NodeID); err != nil {
			return err
		}

	case CommitAddLink:
		if err := w.writeUint64WithCRC(c.NodeID); err != nil {
			return err
		}
		if err := w.writeUint64WithCRC(c.Target); err != nil {
			return err
		}
		if err := w.writeUint16WithCRC(uint16(c.Level)); err != nil {
			return err
		}

	case CommitReplaceLinks:
		if err := w.writeUint64WithCRC(c.NodeID); err != nil {
			return err
		}
		if err := w.writeUint16WithCRC(uint16(c.Level)); err != nil {
			return err
		}
		if err := w.writeUint16WithCRC(uint16(len(c.Links))); err != nil {
			return err
		}
		for _, link := range c.Links {
			if err := w.writeUint64WithCRC(link); err != nil {
				return err
			}
		}

	case CommitDeleteNode:
		if err := w.writeUint64WithCRC(c.NodeID); err != nil {
			return err
		}

	case CommitClearLinks:
		if err := w.writeUint64WithCRC(c.NodeID); err != nil {
			return err
		}
		if err := w.writeUint16WithCRC(uint16(c.Level)); err != nil {
			return err
		}

	case CommitSetMaxLevel:
		if err := w.writeUint16WithCRC(uint16(c.Level)); err != nil {
			return err
		}

	case CommitAddTombstone:
		if err := w.writeUint64WithCRC(c.NodeID); err != nil {
			return err
		}

	case CommitRemoveTombstone:
		if err := w.writeUint64WithCRC(c.NodeID); err != nil {
			return err
		}

	case CommitResetIndex:

	case CommitSetEf:
		if err := w.writeUint64WithCRC(c.Value); err != nil {
			return err
		}

	default:
		return fmt.Errorf("unknown commit type: %d", c.Type)
	}

	return w.writeChecksum()
}

func (w *WALWriter) WriteNodeCommits(nc *NodeCommits) error {
	for _, c := range nc.Commits {
		if err := w.WriteCommit(c); err != nil {
			return err
		}
	}
	return nil
}

func (w *WALWriter) Written() int64 {
	return w.written
}

func (w *WALWriter) writeByte(b byte) error {
	_, err := w.w.Write([]byte{b})
	if err == nil {
		w.written++
	}
	return err
}

func (w *WALWriter) writeByteWithCRC(b byte) error {
	w.crc = crc32.Update(w.crc, crc32.IEEETable, []byte{b})
	return w.writeByte(b)
}

func (w *WALWriter) writeUint16WithCRC(v uint16) error {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], v)
	w.crc = crc32.Update(w.crc, crc32.IEEETable, buf[:])
	_, err := w.w.Write(buf[:])
	if err == nil {
		w.written += 2
	}
	return err
}

func (w *WALWriter) writeUint64WithCRC(v uint64) error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	w.crc = crc32.Update(w.crc, crc32.IEEETable, buf[:])
	_, err := w.w.Write(buf[:])
	if err == nil {
		w.written += 8
	}
	return err
}

func (w *WALWriter) writeChecksum() error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], w.crc)
	_, err := w.w.Write(buf[:])
	if err == nil {
		w.written += 4
	}
	return err
}
