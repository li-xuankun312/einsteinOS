package kernel

import (
	"sync"
	"time"
)

type AgentSession struct {
	ID        string
	LeaderPid int64
	Pgrp      int32
	ConvID    string
	Model     string
	Effort    uint8
	CreatedAt time.Time
	LastActive time.Time
	TotalTokens int64
	TurnCount   int
	Active     bool
}

type SessionTable struct {
	mu       sync.RWMutex
	sessions map[string]*AgentSession
}

var GlobalSessionTable = &SessionTable{
	sessions: make(map[string]*AgentSession),
}

func (st *SessionTable) Create(id string, leaderPid int64, convID, model string) *AgentSession {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	s := &AgentSession{
		ID:         id,
		LeaderPid:  leaderPid,
		ConvID:     convID,
		Model:      model,
		CreatedAt:  now,
		LastActive: now,
		Active:     true,
	}
	st.sessions[id] = s
	return s
}

func (st *SessionTable) Get(id string) *AgentSession {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.sessions[id]
}

func (st *SessionTable) GetByConvID(convID string) *AgentSession {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, s := range st.sessions {
		if s.ConvID == convID {
			return s
		}
	}
	return nil
}

func (st *SessionTable) GetByPid(pid int64) *AgentSession {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, s := range st.sessions {
		if s.LeaderPid == pid {
			return s
		}
	}
	return nil
}

func (st *SessionTable) Touch(id string, tokens int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if s, ok := st.sessions[id]; ok {
		s.LastActive = time.Now()
		s.TotalTokens += tokens
		s.TurnCount++
	}
}

func (st *SessionTable) Close(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if s, ok := st.sessions[id]; ok {
		s.Active = false
	}
}

func (st *SessionTable) Remove(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, id)
}

func (st *SessionTable) Active() []*AgentSession {
	st.mu.RLock()
	defer st.mu.RUnlock()
	var out []*AgentSession
	for _, s := range st.sessions {
		if s.Active {
			out = append(out, s)
		}
	}
	return out
}

func (st *SessionTable) All() []*AgentSession {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]*AgentSession, 0, len(st.sessions))
	for _, s := range st.sessions {
		out = append(out, s)
	}
	return out
}

func (st *SessionTable) TotalTokens() int64 {
	st.mu.RLock()
	defer st.mu.RUnlock()
	var total int64
	for _, s := range st.sessions {
		total += s.TotalTokens
	}
	return total
}

func (st *SessionTable) Count() (active, total int) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, s := range st.sessions {
		total++
		if s.Active {
			active++
		}
	}
	return
}
