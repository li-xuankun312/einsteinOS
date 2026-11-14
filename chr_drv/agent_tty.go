package chr_drv

import (
	"strings"
	"sync"
)

const (
	AGENT_TTY_RAW    = 0
	AGENT_TTY_COOKED = 1
)

type FilterFunc func(input string) (output string, blocked bool)

type AgentTty struct {
	mu          sync.Mutex
	Mode        int
	Pid         int64
	Filters     []FilterFunc
	Confirmable bool
	PendingIn   string
	Confirmed   chan bool
	History     []string
}

var (
	agentTtys   = make(map[int64]*AgentTty)
	agentTtyMu  sync.Mutex
)

func GetAgentTty(pid int64) *AgentTty {
	agentTtyMu.Lock()
	defer agentTtyMu.Unlock()
	t, ok := agentTtys[pid]
	if !ok {
		t = &AgentTty{
			Mode:      AGENT_TTY_COOKED,
			Pid:       pid,
			Confirmed: make(chan bool, 1),
		}
		agentTtys[pid] = t
	}
	return t
}

func SetAgentTtyMode(pid int64, mode int) {
	t := GetAgentTty(pid)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Mode = mode
}

func AddFilter(pid int64, f FilterFunc) {
	t := GetAgentTty(pid)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Filters = append(t.Filters, f)
}

func AgentTtyProcess(pid int64, input string) (string, bool) {
	t := GetAgentTty(pid)
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.Mode == AGENT_TTY_RAW {
		t.History = append(t.History, input)
		return input, true
	}

	processed := input
	for _, f := range t.Filters {
		out, blocked := f(processed)
		if blocked {
			return out, false
		}
		processed = out
	}

	processed = strings.TrimSpace(processed)
	t.History = append(t.History, processed)
	return processed, true
}

func SetConfirmable(pid int64, on bool) {
	t := GetAgentTty(pid)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Confirmable = on
}

func AgentTtyNeedConfirm(pid int64, prompt string) bool {
	t := GetAgentTty(pid)
	t.mu.Lock()
	if !t.Confirmable {
		t.mu.Unlock()
		return false
	}
	t.PendingIn = prompt
	t.mu.Unlock()
	return true
}

func AgentTtyConfirm(pid int64, yes bool) {
	t := GetAgentTty(pid)
	select {
	case t.Confirmed <- yes:
	default:
	}
}

func AgentTtyWaitConfirm(pid int64) bool {
	t := GetAgentTty(pid)
	return <-t.Confirmed
}
