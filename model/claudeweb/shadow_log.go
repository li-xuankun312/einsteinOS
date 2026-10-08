package claudeweb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type ExecRecord struct {
	Time     string `json:"t"`
	ConvID   string `json:"conv"`
	Pid      int    `json:"pid"`
	Tool     string `json:"tool"`
	Command  string `json:"cmd"`
	ExitCode int    `json:"exit"`
	Stdout   string `json:"out,omitempty"`
	Stderr   string `json:"err,omitempty"`
	Files    string `json:"files,omitempty"`
}

type ShadowLog struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	records  int
	convID   string
	taskDesc string
}

func NewShadowLog(workDir string) *ShadowLog {
	logDir := filepath.Join(workDir, ".einstein")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "shadow.jsonl")
	f, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	return &ShadowLog{
		path: logPath,
		file: f,
	}
}

func (sl *ShadowLog) SetConv(convID string) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	sl.convID = convID
}

func (sl *ShadowLog) SetTask(desc string) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	sl.taskDesc = desc
}

func (sl *ShadowLog) Append(rec ExecRecord) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if rec.ConvID == "" {
		rec.ConvID = sl.convID
	}
	if rec.Time == "" {
		rec.Time = time.Now().Format("2006-01-02T15:04:05")
	}
	if rec.Stdout != "" && len(rec.Stdout) > 500 {
		rec.Stdout = rec.Stdout[:500]
	}
	if rec.Stderr != "" && len(rec.Stderr) > 300 {
		rec.Stderr = rec.Stderr[:300]
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if sl.file != nil {
		sl.file.Write(data)
		sl.file.Write([]byte("\n"))
	}
	sl.records++
}

func (sl *ShadowLog) Summary(maxLines int) string {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	data, err := os.ReadFile(sl.path)
	if err != nil || len(data) == 0 {
		return ""
	}

	lines := splitLines(string(data))
	if len(lines) == 0 {
		return ""
	}

	start := 0
	if len(lines) > maxLines {
		start = len(lines) - maxLines
	}

	var summary string
	var totalExec, totalFail int
	files := map[string]bool{}

	for _, line := range lines[start:] {
		var rec ExecRecord
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		totalExec++
		if rec.ExitCode != 0 {
			totalFail++
		}
		if rec.Files != "" {
			files[rec.Files] = true
		}
	}

	summary = fmt.Sprintf("[Local State] %d commands executed (%d failed), %d files touched",
		totalExec, totalFail, len(files))

	if sl.taskDesc != "" {
		summary += "\nLast task: " + sl.taskDesc
	}

	summary += "\nRecent commands:\n"
	recentStart := len(lines) - 10
	if recentStart < start {
		recentStart = start
	}
	for _, line := range lines[recentStart:] {
		var rec ExecRecord
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		status := "ok"
		if rec.ExitCode != 0 {
			status = fmt.Sprintf("exit=%d", rec.ExitCode)
		}
		cmd := rec.Command
		if len(cmd) > 80 {
			cmd = cmd[:80] + "..."
		}
		summary += fmt.Sprintf("  [%s] %s → %s\n", rec.Time, cmd, status)
	}

	return summary
}

func (sl *ShadowLog) Close() {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if sl.file != nil {
		sl.file.Close()
	}
}

func (sl *ShadowLog) Count() int {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	return sl.records
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
