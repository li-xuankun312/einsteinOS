package claudeweb

import (
	"fmt"
	"sync"
	"time"
)

type ExecRecord struct {
	ID       int
	Time     time.Time
	ConvID   string
	Pid      int
	Tool     string
	Command  string
	ExitCode int
	Stdout   string
	Stderr   string
	File     string
}

type ShadowDB struct {
	mu      sync.RWMutex
	records []ExecRecord
	nextID  int

	byConv map[string][]int
	byFile map[string][]int
	byTool map[string][]int

	convID   string
	taskDesc string
}

func NewShadowDB() *ShadowDB {
	return &ShadowDB{
		byConv: make(map[string][]int),
		byFile: make(map[string][]int),
		byTool: make(map[string][]int),
	}
}

func (db *ShadowDB) SetConv(convID string) {
	db.mu.Lock()
	db.convID = convID
	db.mu.Unlock()
}

func (db *ShadowDB) SetTask(desc string) {
	db.mu.Lock()
	db.taskDesc = desc
	db.mu.Unlock()
}

func (db *ShadowDB) Insert(rec ExecRecord) int {
	db.mu.Lock()
	defer db.mu.Unlock()

	db.nextID++
	rec.ID = db.nextID
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	if rec.ConvID == "" {
		rec.ConvID = db.convID
	}
	if len(rec.Stdout) > 1000 {
		rec.Stdout = rec.Stdout[:1000]
	}
	if len(rec.Stderr) > 500 {
		rec.Stderr = rec.Stderr[:500]
	}

	idx := len(db.records)
	db.records = append(db.records, rec)

	if rec.ConvID != "" {
		db.byConv[rec.ConvID] = append(db.byConv[rec.ConvID], idx)
	}
	if rec.File != "" {
		db.byFile[rec.File] = append(db.byFile[rec.File], idx)
	}
	if rec.Tool != "" {
		db.byTool[rec.Tool] = append(db.byTool[rec.Tool], idx)
	}

	return rec.ID
}

func (db *ShadowDB) Recent(n int) []ExecRecord {
	db.mu.RLock()
	defer db.mu.RUnlock()

	total := len(db.records)
	if total == 0 {
		return nil
	}
	start := total - n
	if start < 0 {
		start = 0
	}
	out := make([]ExecRecord, total-start)
	copy(out, db.records[start:])
	return out
}

func (db *ShadowDB) ByConv(convID string) []ExecRecord {
	db.mu.RLock()
	defer db.mu.RUnlock()

	idxs := db.byConv[convID]
	out := make([]ExecRecord, len(idxs))
	for i, idx := range idxs {
		out[i] = db.records[idx]
	}
	return out
}

func (db *ShadowDB) ByFile(path string) []ExecRecord {
	db.mu.RLock()
	defer db.mu.RUnlock()

	idxs := db.byFile[path]
	out := make([]ExecRecord, len(idxs))
	for i, idx := range idxs {
		out[i] = db.records[idx]
	}
	return out
}

func (db *ShadowDB) FilesTouched() []string {
	db.mu.RLock()
	defer db.mu.RUnlock()

	files := make([]string, 0, len(db.byFile))
	for f := range db.byFile {
		files = append(files, f)
	}
	return files
}

func (db *ShadowDB) Stats() (total, failed, files, convs int) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	total = len(db.records)
	for _, r := range db.records {
		if r.ExitCode != 0 {
			failed++
		}
	}
	files = len(db.byFile)
	convs = len(db.byConv)
	return
}

func (db *ShadowDB) Count() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return len(db.records)
}

func (db *ShadowDB) Summary(maxRecent int) string {
	db.mu.RLock()
	defer db.mu.RUnlock()

	total := len(db.records)
	if total == 0 {
		return ""
	}

	var failed int
	for _, r := range db.records {
		if r.ExitCode != 0 {
			failed++
		}
	}

	s := fmt.Sprintf("[Local State] %d commands executed (%d failed), %d files touched, %d conversations\n",
		total, failed, len(db.byFile), len(db.byConv))

	if db.taskDesc != "" {
		s += "Last task: " + db.taskDesc + "\n"
	}

	if len(db.byFile) > 0 {
		s += "Files modified:"
		count := 0
		for f := range db.byFile {
			if count >= 15 {
				s += fmt.Sprintf(" (+%d more)", len(db.byFile)-15)
				break
			}
			s += " " + f
			count++
		}
		s += "\n"
	}

	s += "Recent:\n"
	start := total - maxRecent
	if start < 0 {
		start = 0
	}
	for _, r := range db.records[start:] {
		status := "ok"
		if r.ExitCode != 0 {
			status = fmt.Sprintf("exit=%d", r.ExitCode)
		}
		cmd := r.Command
		if len(cmd) > 80 {
			cmd = cmd[:80] + "..."
		}
		s += fmt.Sprintf("  [%s] %s → %s\n", r.Time.Format("15:04:05"), cmd, status)
	}

	return s
}
