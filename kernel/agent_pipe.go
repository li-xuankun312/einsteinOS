package kernel

import (
	"encoding/json"
	"sync"
)

type AgentMessage struct {
	From    int32  `json:"from"`
	To      int32  `json:"to"`
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

type AgentPipe struct {
	mu     sync.Mutex
	buf    []AgentMessage
	closed bool
	ready  chan struct{}
}

var (
	agentPipes   = make(map[int64]*AgentPipe)
	agentPipeMu  sync.Mutex
	nextPipeID   int64
)

func CreateAgentPipe() int64 {
	agentPipeMu.Lock()
	defer agentPipeMu.Unlock()
	nextPipeID++
	id := nextPipeID
	agentPipes[id] = &AgentPipe{
		ready: make(chan struct{}, 1),
	}
	return id
}

func AgentPipeWrite(pipeID int64, msg AgentMessage) int {
	agentPipeMu.Lock()
	p, ok := agentPipes[pipeID]
	agentPipeMu.Unlock()
	if !ok {
		return -1
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return -1
	}
	p.buf = append(p.buf, msg)
	p.mu.Unlock()

	select {
	case p.ready <- struct{}{}:
	default:
	}
	return 0
}

func AgentPipeRead(pipeID int64) (AgentMessage, bool) {
	agentPipeMu.Lock()
	p, ok := agentPipes[pipeID]
	agentPipeMu.Unlock()
	if !ok {
		return AgentMessage{}, false
	}

	for {
		p.mu.Lock()
		if len(p.buf) > 0 {
			msg := p.buf[0]
			p.buf = p.buf[1:]
			p.mu.Unlock()
			return msg, true
		}
		if p.closed {
			p.mu.Unlock()
			return AgentMessage{}, false
		}
		p.mu.Unlock()
		<-p.ready
	}
}

func AgentPipeClose(pipeID int64) {
	agentPipeMu.Lock()
	p, ok := agentPipes[pipeID]
	agentPipeMu.Unlock()
	if !ok {
		return
	}
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	select {
	case p.ready <- struct{}{}:
	default:
	}
}

func AgentPipeWriteJSON(pipeID int64, from, to int32, msgType string, v interface{}) int {
	data, err := json.Marshal(v)
	if err != nil {
		return -1
	}
	return AgentPipeWrite(pipeID, AgentMessage{
		From:    from,
		To:      to,
		Type:    msgType,
		Payload: string(data),
	})
}
