package kernel

import (
	"encoding/json"
	"sync"

	. "google.golang.org/adk/v2/include"
)

const (
	EXIT_SUCCESS       = 0
	EXIT_NEED_HUMAN    = 1
	EXIT_BUDGET_EXCEED = 2
	EXIT_CONTEXT_FULL  = 3
	EXIT_TIMEOUT       = 4
	EXIT_UPSTREAM_DEAD = 5
	EXIT_FILTER_BLOCK  = 6
	EXIT_ERROR         = 127
)

type AgentExitInfo struct {
	Code    int
	Reason  string
	Result  string
	Tokens  int64
	Context int64
}

var (
	exitInfos  = make(map[int64]*AgentExitInfo)
	exitInfoMu sync.Mutex
)

func AgentExit(pid int64, info *AgentExitInfo) {
	exitInfoMu.Lock()
	exitInfos[pid] = info
	exitInfoMu.Unlock()

	code := int32(info.Code) << 8
	schedMu.Lock()
	saved := Current
	for i := 1; i < NR_TASKS; i++ {
		if Task[i] != nil && int64(Task[i].Pid) == pid {
			Current = Task[i]
			schedMu.Unlock()

			metaMu.Lock()
			if m, ok := taskMeta[pid]; ok {
				m.Result = info.Result
				if m.Done != nil {
					select {
					case <-m.Done:
					default:
						close(m.Done)
					}
				}
			}
			metaMu.Unlock()

			DoExit(code)

			schedMu.Lock()
			Current = saved
			schedMu.Unlock()
			return
		}
	}
	schedMu.Unlock()
}

func GetAgentExitInfo(pid int64) *AgentExitInfo {
	exitInfoMu.Lock()
	defer exitInfoMu.Unlock()
	return exitInfos[pid]
}

func AgentWait(pid int64) (int64, *AgentExitInfo, error) {
	childPid, _, err := Wait(pid)
	if err != nil {
		return 0, nil, err
	}

	exitInfoMu.Lock()
	info := exitInfos[childPid]
	exitInfoMu.Unlock()

	if info == nil {
		info = &AgentExitInfo{Code: EXIT_SUCCESS}
	}
	return childPid, info, nil
}

func ExitCodeName(code int) string {
	switch code {
	case EXIT_SUCCESS:
		return "success"
	case EXIT_NEED_HUMAN:
		return "need_human"
	case EXIT_BUDGET_EXCEED:
		return "budget_exceeded"
	case EXIT_CONTEXT_FULL:
		return "context_full"
	case EXIT_TIMEOUT:
		return "timeout"
	case EXIT_UPSTREAM_DEAD:
		return "upstream_dead"
	case EXIT_FILTER_BLOCK:
		return "filter_blocked"
	case EXIT_ERROR:
		return "error"
	default:
		return "unknown"
	}
}

func AgentExitInfoJSON(info *AgentExitInfo) string {
	data, _ := json.Marshal(info)
	return string(data)
}
