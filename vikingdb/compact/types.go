package compact

import (
	"fmt"
	"time"
)

type CommitType uint8

const (
	CommitAddNode         CommitType = 1
	CommitSetEntryPoint   CommitType = 2
	CommitAddLink         CommitType = 3
	CommitReplaceLinks    CommitType = 4
	CommitDeleteNode      CommitType = 5
	CommitClearLinks      CommitType = 6
	CommitSetMaxLevel     CommitType = 7
	CommitAddTombstone    CommitType = 8
	CommitRemoveTombstone CommitType = 9
	CommitResetIndex      CommitType = 10
	CommitSetEf           CommitType = 11
	CommitAddNodeLevel    CommitType = 12
)

func (t CommitType) String() string {
	switch t {
	case CommitAddNode:
		return "AddNode"
	case CommitSetEntryPoint:
		return "SetEntryPoint"
	case CommitAddLink:
		return "AddLink"
	case CommitReplaceLinks:
		return "ReplaceLinks"
	case CommitDeleteNode:
		return "DeleteNode"
	case CommitClearLinks:
		return "ClearLinks"
	case CommitSetMaxLevel:
		return "SetMaxLevel"
	case CommitAddTombstone:
		return "AddTombstone"
	case CommitRemoveTombstone:
		return "RemoveTombstone"
	case CommitResetIndex:
		return "ResetIndex"
	case CommitSetEf:
		return "SetEf"
	case CommitAddNodeLevel:
		return "AddNodeLevel"
	default:
		return fmt.Sprintf("Unknown(%d)", t)
	}
}

func (t CommitType) IsGlobal() bool {
	switch t {
	case CommitSetEntryPoint, CommitSetMaxLevel, CommitResetIndex, CommitSetEf:
		return true
	default:
		return false
	}
}

func (t CommitType) IsNodeScoped() bool {
	return !t.IsGlobal()
}

type Commit struct {
	Type    CommitType
	NodeID  uint64
	Level   int
	Links   []uint64
	Target  uint64
	Value   uint64
	SeqNo   uint64
}

type NodeCommits struct {
	NodeID  uint64
	Commits []Commit
}

type FileType int

const (
	FileWAL      FileType = 1
	FileSorted   FileType = 2
	FileSnapshot FileType = 3
)

func (ft FileType) String() string {
	switch ft {
	case FileWAL:
		return "wal"
	case FileSorted:
		return "sorted"
	case FileSnapshot:
		return "snapshot"
	default:
		return "unknown"
	}
}

type FileInfo struct {
	Path      string
	Type      FileType
	StartTS   int64
	EndTS     int64
	Size      int64
	SeqNo     int
	CreatedAt time.Time
}

func (fi FileInfo) String() string {
	return fmt.Sprintf("%s[%d-%d] %s (%d bytes)", fi.Type, fi.StartTS, fi.EndTS, fi.Path, fi.Size)
}

func (fi FileInfo) Contains(ts int64) bool {
	return ts >= fi.StartTS && ts <= fi.EndTS
}

func (fi FileInfo) Overlaps(other FileInfo) bool {
	return fi.StartTS <= other.EndTS && fi.EndTS >= other.StartTS
}

type Overlap struct {
	FileA FileInfo
	FileB FileInfo
}

type DirectoryState struct {
	WALFiles      []FileInfo
	SortedFiles   []FileInfo
	SnapshotFiles []FileInfo
	Overlaps      []Overlap
	ResetTS       int64
}

func (ds *DirectoryState) TotalWALSize() int64 {
	var total int64
	for _, f := range ds.WALFiles {
		total += f.Size
	}
	return total
}

func (ds *DirectoryState) TotalSortedSize() int64 {
	var total int64
	for _, f := range ds.SortedFiles {
		total += f.Size
	}
	return total
}

func (ds *DirectoryState) TotalSnapshotSize() int64 {
	var total int64
	for _, f := range ds.SnapshotFiles {
		total += f.Size
	}
	return total
}

func (ds *DirectoryState) TotalSize() int64 {
	return ds.TotalWALSize() + ds.TotalSortedSize() + ds.TotalSnapshotSize()
}

func (ds *DirectoryState) FileCount() int {
	return len(ds.WALFiles) + len(ds.SortedFiles) + len(ds.SnapshotFiles)
}

func (ds *DirectoryState) LatestSnapshot() *FileInfo {
	if len(ds.SnapshotFiles) == 0 {
		return nil
	}
	latest := &ds.SnapshotFiles[0]
	for i := 1; i < len(ds.SnapshotFiles); i++ {
		if ds.SnapshotFiles[i].EndTS > latest.EndTS {
			latest = &ds.SnapshotFiles[i]
		}
	}
	return latest
}

func (ds *DirectoryState) String() string {
	return fmt.Sprintf("DirState{wal=%d sorted=%d snap=%d overlaps=%d totalSize=%d}",
		len(ds.WALFiles), len(ds.SortedFiles), len(ds.SnapshotFiles),
		len(ds.Overlaps), ds.TotalSize())
}
