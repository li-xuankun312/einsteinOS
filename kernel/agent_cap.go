package kernel

import (
	"sync"

	. "google.golang.org/adk/v2/include"
)

type AgentCap uint64

const (
	CAP_TOOL_USE     AgentCap = 1 << 0
	CAP_FILE_READ    AgentCap = 1 << 1
	CAP_FILE_WRITE   AgentCap = 1 << 2
	CAP_NET_ACCESS   AgentCap = 1 << 3
	CAP_EXEC         AgentCap = 1 << 4
	CAP_SPAWN        AgentCap = 1 << 5
	CAP_KILL         AgentCap = 1 << 6
	CAP_ADMIN        AgentCap = 1 << 7
	CAP_BUDGET_ALLOC AgentCap = 1 << 8
	CAP_PROMPT_WRITE AgentCap = 1 << 9
	CAP_MEMORY_READ  AgentCap = 1 << 10
	CAP_MEMORY_WRITE AgentCap = 1 << 11
	CAP_PIPE_CREATE  AgentCap = 1 << 12
	CAP_CONFIRM_SKIP AgentCap = 1 << 13

	CAP_ALL AgentCap = (1 << 14) - 1
	CAP_DEFAULT = CAP_TOOL_USE | CAP_FILE_READ | CAP_SPAWN | CAP_MEMORY_READ | CAP_PIPE_CREATE
	CAP_RESTRICTED = CAP_TOOL_USE | CAP_FILE_READ | CAP_MEMORY_READ
)

var (
	agentCaps  = make(map[int64]AgentCap)
	agentCapMu sync.RWMutex
)

func SetAgentCaps(pid int64, caps AgentCap) {
	agentCapMu.Lock()
	defer agentCapMu.Unlock()
	agentCaps[pid] = caps
}

func GetAgentCaps(pid int64) AgentCap {
	agentCapMu.RLock()
	defer agentCapMu.RUnlock()
	caps, ok := agentCaps[pid]
	if !ok {
		return CAP_DEFAULT
	}
	return caps
}

func HasCap(pid int64, cap AgentCap) bool {
	schedMu.Lock()
	var t *TaskStruct
	for i := 0; i < NR_TASKS; i++ {
		if Task[i] != nil && int64(Task[i].Pid) == pid {
			t = Task[i]
			break
		}
	}
	schedMu.Unlock()

	if t != nil && t.Euid == 0 {
		return true
	}

	return GetAgentCaps(pid)&cap != 0
}

func GrantCap(pid int64, cap AgentCap) {
	agentCapMu.Lock()
	defer agentCapMu.Unlock()
	agentCaps[pid] |= cap
}

func RevokeCap(pid int64, cap AgentCap) {
	agentCapMu.Lock()
	defer agentCapMu.Unlock()
	agentCaps[pid] &^= cap
}

func CapName(cap AgentCap) string {
	names := map[AgentCap]string{
		CAP_TOOL_USE:     "tool_use",
		CAP_FILE_READ:    "file_read",
		CAP_FILE_WRITE:   "file_write",
		CAP_NET_ACCESS:   "net_access",
		CAP_EXEC:         "exec",
		CAP_SPAWN:        "spawn",
		CAP_KILL:         "kill",
		CAP_ADMIN:        "admin",
		CAP_BUDGET_ALLOC: "budget_alloc",
		CAP_PROMPT_WRITE: "prompt_write",
		CAP_MEMORY_READ:  "memory_read",
		CAP_MEMORY_WRITE: "memory_write",
		CAP_PIPE_CREATE:  "pipe_create",
		CAP_CONFIRM_SKIP: "confirm_skip",
	}
	if n, ok := names[cap]; ok {
		return n
	}
	return "unknown"
}

func ListCaps(pid int64) []string {
	caps := GetAgentCaps(pid)
	var out []string
	for i := 0; i < 14; i++ {
		c := AgentCap(1 << i)
		if caps&c != 0 {
			out = append(out, CapName(c))
		}
	}
	return out
}

func InheritCaps(parentPid, childPid int64) {
	parentCaps := GetAgentCaps(parentPid)
	inherited := parentCaps &^ (CAP_ADMIN | CAP_BUDGET_ALLOC)
	SetAgentCaps(childPid, inherited)
}
