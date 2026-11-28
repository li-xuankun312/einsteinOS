package compact

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strings"
)

type WALValidationResult struct {
	Path          string
	HeaderValid   bool
	TotalRecords  int
	ValidRecords  int
	CorruptRecords int
	TruncatedAt   int64
	FileSize      int64
	Issues        []string
	RecordTypes   map[CommitType]int
}

func (r *WALValidationResult) IsHealthy() bool {
	return r.HeaderValid && r.CorruptRecords == 0
}

func (r *WALValidationResult) Summary() string {
	var sb strings.Builder
	status := "HEALTHY"
	if !r.IsHealthy() {
		status = "CORRUPT"
	}
	sb.WriteString(fmt.Sprintf("WAL %s [%s]:\n", r.Path, status))
	sb.WriteString(fmt.Sprintf("  Size: %s\n", formatBytes(r.FileSize)))
	sb.WriteString(fmt.Sprintf("  Records: %d valid, %d corrupt, %d total\n",
		r.ValidRecords, r.CorruptRecords, r.TotalRecords))
	if r.TruncatedAt > 0 {
		sb.WriteString(fmt.Sprintf("  Truncated at byte: %d\n", r.TruncatedAt))
	}
	if len(r.RecordTypes) > 0 {
		sb.WriteString("  Record types:\n")
		for t, count := range r.RecordTypes {
			sb.WriteString(fmt.Sprintf("    %s: %d\n", t, count))
		}
	}
	for _, issue := range r.Issues {
		sb.WriteString(fmt.Sprintf("  ISSUE: %s\n", issue))
	}
	return sb.String()
}

type WALValidator struct {
	level ValidationLevel
}

func NewWALValidator(level ValidationLevel) *WALValidator {
	return &WALValidator{level: level}
}

func (v *WALValidator) ValidateFile(path string) (*WALValidationResult, error) {
	result := &WALValidationResult{
		Path:        path,
		RecordTypes: make(map[CommitType]int),
	}

	info, err := os.Stat(path)
	if err != nil {
		return result, err
	}
	result.FileSize = info.Size()

	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer f.Close()

	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		result.Issues = append(result.Issues, "cannot read magic bytes")
		return result, nil
	}
	if string(magic) != walMagic {
		result.Issues = append(result.Issues, fmt.Sprintf("invalid magic: %q", string(magic)))
		return result, nil
	}

	ver := make([]byte, 1)
	if _, err := io.ReadFull(f, ver); err != nil {
		result.Issues = append(result.Issues, "cannot read version")
		return result, nil
	}
	if ver[0] > walVersion {
		result.Issues = append(result.Issues, fmt.Sprintf("unsupported version: %d", ver[0]))
		return result, nil
	}
	result.HeaderValid = true

	offset := int64(5)
	for {
		recordStart := offset
		typ, crc, bytesRead, err := v.readRecord(f)
		if err == io.EOF {
			break
		}

		offset += int64(bytesRead)
		result.TotalRecords++

		if err != nil {
			result.CorruptRecords++
			result.TruncatedAt = recordStart
			result.Issues = append(result.Issues,
				fmt.Sprintf("corrupt record at offset %d: %v", recordStart, err))

			if v.level == ValidateBasic {
				break
			}
			continue
		}

		_ = crc
		result.ValidRecords++
		result.RecordTypes[typ]++
	}

	if v.level >= ValidateStandard {
		v.validateSequence(result)
	}

	return result, nil
}

func (v *WALValidator) readRecord(r io.Reader) (CommitType, uint32, int, error) {
	var crcAccum uint32
	bytesRead := 0

	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(r, typeBuf); err != nil {
		return 0, 0, 0, err
	}
	bytesRead++
	crcAccum = crc32.Update(crcAccum, crc32.IEEETable, typeBuf)
	typ := CommitType(typeBuf[0])

	var err error
	switch typ {
	case CommitAddNode:
		_, crcAccum, err = readBytesWithCRC(r, 8, crcAccum)
		bytesRead += 8

	case CommitAddNodeLevel:
		_, crcAccum, err = readBytesWithCRC(r, 10, crcAccum)
		bytesRead += 10

	case CommitSetEntryPoint:
		_, crcAccum, err = readBytesWithCRC(r, 8, crcAccum)
		bytesRead += 8

	case CommitAddLink:
		_, crcAccum, err = readBytesWithCRC(r, 18, crcAccum)
		bytesRead += 18

	case CommitReplaceLinks:
		buf, newCRC, readErr := readBytesWithCRC(r, 12, crcAccum)
		crcAccum = newCRC
		bytesRead += 12
		if readErr != nil {
			err = readErr
			break
		}
		count := binary.LittleEndian.Uint16(buf[10:12])
		linkBytes := int(count) * 8
		if linkBytes > 0 {
			_, crcAccum, err = readBytesWithCRC(r, linkBytes, crcAccum)
			bytesRead += linkBytes
		}

	case CommitDeleteNode:
		_, crcAccum, err = readBytesWithCRC(r, 8, crcAccum)
		bytesRead += 8

	case CommitClearLinks:
		_, crcAccum, err = readBytesWithCRC(r, 10, crcAccum)
		bytesRead += 10

	case CommitSetMaxLevel:
		_, crcAccum, err = readBytesWithCRC(r, 2, crcAccum)
		bytesRead += 2

	case CommitAddTombstone:
		_, crcAccum, err = readBytesWithCRC(r, 8, crcAccum)
		bytesRead += 8

	case CommitRemoveTombstone:
		_, crcAccum, err = readBytesWithCRC(r, 8, crcAccum)
		bytesRead += 8

	case CommitResetIndex:

	case CommitSetEf:
		_, crcAccum, err = readBytesWithCRC(r, 8, crcAccum)
		bytesRead += 8

	default:
		return typ, 0, bytesRead, fmt.Errorf("unknown type: %d", typ)
	}

	if err != nil {
		return typ, 0, bytesRead, err
	}

	var checksumBuf [4]byte
	if _, err := io.ReadFull(r, checksumBuf[:]); err != nil {
		return typ, 0, bytesRead, fmt.Errorf("read checksum: %w", err)
	}
	bytesRead += 4

	expected := binary.LittleEndian.Uint32(checksumBuf[:])
	if expected != crcAccum {
		return typ, crcAccum, bytesRead, fmt.Errorf("CRC mismatch: got %08x want %08x", crcAccum, expected)
	}

	return typ, crcAccum, bytesRead, nil
}

func readBytesWithCRC(r io.Reader, n int, crc uint32) ([]byte, uint32, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, crc, err
	}
	crc = crc32.Update(crc, crc32.IEEETable, buf)
	return buf, crc, nil
}

func (v *WALValidator) validateSequence(result *WALValidationResult) {
	addNodes := result.RecordTypes[CommitAddNode] + result.RecordTypes[CommitAddNodeLevel]
	deleteNodes := result.RecordTypes[CommitDeleteNode]

	if deleteNodes > addNodes {
		result.Issues = append(result.Issues,
			fmt.Sprintf("more deletes (%d) than adds (%d)", deleteNodes, addNodes))
	}

	tombstoneAdds := result.RecordTypes[CommitAddTombstone]
	tombstoneRemoves := result.RecordTypes[CommitRemoveTombstone]
	if tombstoneRemoves > tombstoneAdds {
		result.Issues = append(result.Issues,
			fmt.Sprintf("more tombstone removes (%d) than adds (%d)", tombstoneRemoves, tombstoneAdds))
	}
}

func ValidateAllWALs(dir string, level ValidationLevel) ([]*WALValidationResult, error) {
	discovery := NewFileDiscovery(dir)
	state, err := discovery.Scan()
	if err != nil {
		return nil, err
	}

	validator := NewWALValidator(level)
	var results []*WALValidationResult

	allFiles := make([]FileInfo, 0)
	allFiles = append(allFiles, state.WALFiles...)
	allFiles = append(allFiles, state.SortedFiles...)
	allFiles = append(allFiles, state.SnapshotFiles...)

	for _, f := range allFiles {
		result, err := validator.ValidateFile(f.Path)
		if err != nil {
			result.Issues = append(result.Issues, err.Error())
		}
		results = append(results, result)
	}

	return results, nil
}

func WALValidationSummary(results []*WALValidationResult) string {
	var sb strings.Builder
	healthy := 0
	corrupt := 0
	totalRecords := 0
	totalCorrupt := 0

	for _, r := range results {
		if r.IsHealthy() {
			healthy++
		} else {
			corrupt++
		}
		totalRecords += r.ValidRecords
		totalCorrupt += r.CorruptRecords
	}

	sb.WriteString(fmt.Sprintf("WAL Validation: %d files (%d healthy, %d corrupt)\n",
		len(results), healthy, corrupt))
	sb.WriteString(fmt.Sprintf("  Records: %d valid, %d corrupt\n", totalRecords, totalCorrupt))

	if corrupt > 0 {
		sb.WriteString("  Corrupt files:\n")
		for _, r := range results {
			if !r.IsHealthy() {
				sb.WriteString(fmt.Sprintf("    %s: %d corrupt records\n", r.Path, r.CorruptRecords))
			}
		}
	}

	return sb.String()
}
