package fs

import (
	"sync"
	"time"
)

type ArtifactType int

const (
	ARTIFACT_FILE   ArtifactType = 0
	ARTIFACT_MEMORY ArtifactType = 1
	ARTIFACT_RESULT ArtifactType = 2
	ARTIFACT_LOG    ArtifactType = 3
)

type ArtifactInode struct {
	Inum      uint32
	Type      ArtifactType
	OwnerPid  int64
	Content   []byte
	NLinks    int
	Size      int64
	CreatedAt time.Time
	ModifiedAt time.Time
	AccessedAt time.Time
	Refs      map[int64]bool
	Dirty     bool
	mu        sync.RWMutex
}

type ArtifactFS struct {
	mu       sync.RWMutex
	inodes   map[uint32]*ArtifactInode
	names    map[string]uint32
	nextInum uint32
}

var GlobalArtifactFS = &ArtifactFS{
	inodes:   make(map[uint32]*ArtifactInode),
	names:    make(map[string]uint32),
	nextInum: 1,
}

func (afs *ArtifactFS) Create(name string, atype ArtifactType, ownerPid int64, content []byte) uint32 {
	afs.mu.Lock()
	defer afs.mu.Unlock()

	inum := afs.nextInum
	afs.nextInum++

	now := time.Now()
	inode := &ArtifactInode{
		Inum:       inum,
		Type:       atype,
		OwnerPid:   ownerPid,
		Content:    content,
		NLinks:     1,
		Size:       int64(len(content)),
		CreatedAt:  now,
		ModifiedAt: now,
		AccessedAt: now,
		Refs:       map[int64]bool{ownerPid: true},
		Dirty:      true,
	}
	afs.inodes[inum] = inode
	afs.names[name] = inum
	return inum
}

func (afs *ArtifactFS) Open(name string) *ArtifactInode {
	afs.mu.RLock()
	inum, ok := afs.names[name]
	if !ok {
		afs.mu.RUnlock()
		return nil
	}
	inode := afs.inodes[inum]
	afs.mu.RUnlock()

	if inode != nil {
		inode.mu.Lock()
		inode.AccessedAt = time.Now()
		inode.mu.Unlock()
	}
	return inode
}

func (afs *ArtifactFS) Link(oldname, newname string) int {
	afs.mu.Lock()
	defer afs.mu.Unlock()

	inum, ok := afs.names[oldname]
	if !ok {
		return -1
	}
	inode := afs.inodes[inum]
	if inode == nil {
		return -1
	}
	afs.names[newname] = inum
	inode.mu.Lock()
	inode.NLinks++
	inode.mu.Unlock()
	return 0
}

func (afs *ArtifactFS) Unlink(name string) int {
	afs.mu.Lock()
	defer afs.mu.Unlock()

	inum, ok := afs.names[name]
	if !ok {
		return -1
	}
	delete(afs.names, name)

	inode := afs.inodes[inum]
	if inode == nil {
		return 0
	}
	inode.mu.Lock()
	inode.NLinks--
	shouldFree := inode.NLinks <= 0
	inode.mu.Unlock()

	if shouldFree {
		delete(afs.inodes, inum)
	}
	return 0
}

func (afs *ArtifactFS) AddRef(name string, pid int64) {
	inode := afs.Open(name)
	if inode == nil {
		return
	}
	inode.mu.Lock()
	defer inode.mu.Unlock()
	if !inode.Refs[pid] {
		inode.Refs[pid] = true
		inode.NLinks++
	}
}

func (afs *ArtifactFS) RemoveRef(name string, pid int64) {
	inode := afs.Open(name)
	if inode == nil {
		return
	}
	inode.mu.Lock()
	defer inode.mu.Unlock()
	if inode.Refs[pid] {
		delete(inode.Refs, pid)
		inode.NLinks--
	}
}

func (ai *ArtifactInode) Read() []byte {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	out := make([]byte, len(ai.Content))
	copy(out, ai.Content)
	return out
}

func (ai *ArtifactInode) Write(data []byte) {
	ai.mu.Lock()
	defer ai.mu.Unlock()
	ai.Content = data
	ai.Size = int64(len(data))
	ai.ModifiedAt = time.Now()
	ai.Dirty = true
}

func (ai *ArtifactInode) Append(data []byte) {
	ai.mu.Lock()
	defer ai.mu.Unlock()
	ai.Content = append(ai.Content, data...)
	ai.Size = int64(len(ai.Content))
	ai.ModifiedAt = time.Now()
	ai.Dirty = true
}

func (ai *ArtifactInode) Truncate(newSize int64) {
	ai.mu.Lock()
	defer ai.mu.Unlock()
	if newSize < int64(len(ai.Content)) {
		ai.Content = ai.Content[:newSize]
	}
	ai.Size = newSize
	ai.ModifiedAt = time.Now()
	ai.Dirty = true
}

func (afs *ArtifactFS) Sync() []*ArtifactInode {
	afs.mu.RLock()
	defer afs.mu.RUnlock()
	var dirty []*ArtifactInode
	for _, inode := range afs.inodes {
		inode.mu.RLock()
		if inode.Dirty {
			dirty = append(dirty, inode)
		}
		inode.mu.RUnlock()
	}
	return dirty
}

func (afs *ArtifactFS) MarkClean(inum uint32) {
	afs.mu.RLock()
	inode := afs.inodes[inum]
	afs.mu.RUnlock()
	if inode != nil {
		inode.mu.Lock()
		inode.Dirty = false
		inode.mu.Unlock()
	}
}

func (afs *ArtifactFS) List() map[string]uint32 {
	afs.mu.RLock()
	defer afs.mu.RUnlock()
	out := make(map[string]uint32, len(afs.names))
	for k, v := range afs.names {
		out[k] = v
	}
	return out
}

func (afs *ArtifactFS) Stat(name string) (ArtifactType, int64, int, bool) {
	inode := afs.Open(name)
	if inode == nil {
		return 0, 0, 0, false
	}
	inode.mu.RLock()
	defer inode.mu.RUnlock()
	return inode.Type, inode.Size, inode.NLinks, true
}

func (afs *ArtifactFS) ReleaseByPid(pid int64) int {
	afs.mu.Lock()
	defer afs.mu.Unlock()
	freed := 0
	for name, inum := range afs.names {
		inode := afs.inodes[inum]
		if inode == nil {
			continue
		}
		inode.mu.Lock()
		if inode.Refs[pid] {
			delete(inode.Refs, pid)
			inode.NLinks--
			if inode.NLinks <= 0 {
				delete(afs.inodes, inum)
				delete(afs.names, name)
				freed++
			}
		}
		inode.mu.Unlock()
	}
	return freed
}
