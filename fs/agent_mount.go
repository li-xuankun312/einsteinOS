package fs

import (
	"sync"
	"time"
)

type MountType int

const (
	MOUNT_MCP    MountType = 0
	MOUNT_MEMORY MountType = 1
	MOUNT_SKILL  MountType = 2
	MOUNT_TOOL   MountType = 3
	MOUNT_FS     MountType = 4
)

type AgentMountPoint struct {
	Path      string
	Type      MountType
	Source    string
	Pid       int64
	MountedAt time.Time
	ReadOnly  bool
	Options   map[string]string
}

type AgentMountTable struct {
	mu     sync.RWMutex
	mounts map[string]*AgentMountPoint
}

var GlobalMountTable = &AgentMountTable{
	mounts: make(map[string]*AgentMountPoint),
}

func (mt *AgentMountTable) Mount(path string, mtype MountType, source string, pid int64, readOnly bool) int {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	if _, exists := mt.mounts[path]; exists {
		return -1
	}

	mt.mounts[path] = &AgentMountPoint{
		Path:      path,
		Type:      mtype,
		Source:    source,
		Pid:       pid,
		MountedAt: time.Now(),
		ReadOnly:  readOnly,
		Options:   make(map[string]string),
	}
	return 0
}

func (mt *AgentMountTable) Umount(path string) int {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	if _, exists := mt.mounts[path]; !exists {
		return -1
	}
	delete(mt.mounts, path)
	return 0
}

func (mt *AgentMountTable) Lookup(path string) *AgentMountPoint {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	best := ""
	for mountPath := range mt.mounts {
		if len(path) >= len(mountPath) && path[:len(mountPath)] == mountPath {
			if len(mountPath) > len(best) {
				best = mountPath
			}
		}
	}
	if best == "" {
		return nil
	}
	return mt.mounts[best]
}

func (mt *AgentMountTable) List() []*AgentMountPoint {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	out := make([]*AgentMountPoint, 0, len(mt.mounts))
	for _, m := range mt.mounts {
		out = append(out, m)
	}
	return out
}

func (mt *AgentMountTable) MountsByPid(pid int64) []*AgentMountPoint {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	var out []*AgentMountPoint
	for _, m := range mt.mounts {
		if m.Pid == pid {
			out = append(out, m)
		}
	}
	return out
}

func (mt *AgentMountTable) UmountAll(pid int64) int {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	n := 0
	for path, m := range mt.mounts {
		if m.Pid == pid {
			delete(mt.mounts, path)
			n++
		}
	}
	return n
}

func (mt *AgentMountTable) SetOption(path, key, value string) {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	if m, ok := mt.mounts[path]; ok {
		m.Options[key] = value
	}
}

func MountTypeName(t MountType) string {
	switch t {
	case MOUNT_MCP:
		return "mcp"
	case MOUNT_MEMORY:
		return "memory"
	case MOUNT_SKILL:
		return "skill"
	case MOUNT_TOOL:
		return "tool"
	case MOUNT_FS:
		return "fs"
	default:
		return "unknown"
	}
}
