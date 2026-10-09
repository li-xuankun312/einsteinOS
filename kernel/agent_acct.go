package kernel

import (
	"fmt"
	"sync"
	"time"
)

type AccountRecord struct {
	Pid       int64
	Ppid      int64
	StartTime time.Time
	EndTime   time.Time
	Tokens    int64
	Context   int64
	ExitCode  int
	Command   string
	Model     string
	Effort    uint8
}

type AccountingLog struct {
	mu      sync.Mutex
	records []AccountRecord
	enabled bool
}

var GlobalAccounting = &AccountingLog{enabled: true}

func (al *AccountingLog) Enable()  { al.mu.Lock(); al.enabled = true; al.mu.Unlock() }
func (al *AccountingLog) Disable() { al.mu.Lock(); al.enabled = false; al.mu.Unlock() }

func (al *AccountingLog) Record(r AccountRecord) {
	al.mu.Lock()
	defer al.mu.Unlock()
	if !al.enabled {
		return
	}
	al.records = append(al.records, r)
}

func (al *AccountingLog) All() []AccountRecord {
	al.mu.Lock()
	defer al.mu.Unlock()
	out := make([]AccountRecord, len(al.records))
	copy(out, al.records)
	return out
}

func (al *AccountingLog) ByPid(pid int64) []AccountRecord {
	al.mu.Lock()
	defer al.mu.Unlock()
	var out []AccountRecord
	for _, r := range al.records {
		if r.Pid == pid {
			out = append(out, r)
		}
	}
	return out
}

func (al *AccountingLog) Summary() string {
	al.mu.Lock()
	defer al.mu.Unlock()
	var totalTokens int64
	var totalProcs int
	exitCounts := make(map[int]int)
	for _, r := range al.records {
		totalTokens += r.Tokens
		totalProcs++
		exitCounts[r.ExitCode]++
	}
	out := fmt.Sprintf("  processes: %d, total tokens: %d\n", totalProcs, totalTokens)
	for code, count := range exitCounts {
		out += fmt.Sprintf("  exit %s: %d\n", ExitCodeName(code), count)
	}
	return out
}

func (al *AccountingLog) Last(n int) []AccountRecord {
	al.mu.Lock()
	defer al.mu.Unlock()
	if n > len(al.records) {
		n = len(al.records)
	}
	out := make([]AccountRecord, n)
	copy(out, al.records[len(al.records)-n:])
	return out
}

func (al *AccountingLog) TokensByModel() map[string]int64 {
	al.mu.Lock()
	defer al.mu.Unlock()
	m := make(map[string]int64)
	for _, r := range al.records {
		if r.Model != "" {
			m[r.Model] += r.Tokens
		}
	}
	return m
}

func (al *AccountingLog) Clear() {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.records = nil
}
