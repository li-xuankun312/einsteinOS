package kernel

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const PR_STATE_DIR = "/tmp/einstein_pr"

type pr_saved_step struct {
	Kind     StepKind `json:"kind"`
	Owner    string   `json:"owner"`
	Repo     string   `json:"repo"`
	SHA      string   `json:"sha"`
	Path     string   `json:"path"`
	Command  string   `json:"cmd"`
	Dir      string   `json:"dir"`
	Result   string   `json:"result"`
	Files    []string `json:"files"`
	Done     bool     `json:"done"`
	Err      string   `json:"err"`
	Duration int64    `json:"dur_ms"`
}

type pr_saved struct {
	Pid      int64           `json:"pid"`
	Repo     string          `json:"repo"`
	Branch   string          `json:"branch"`
	Title    string          `json:"title"`
	Refs     []RefRepo       `json:"refs"`
	Steps    []pr_saved_step `json:"steps"`
	Cursor   int             `json:"cursor"`
	Created  time.Time       `json:"created"`
	Theorems []theorem_saved `json:"theorems"`
}

type theorem_saved struct {
	Repo   string `json:"repo"`
	SHA    string `json:"sha"`
	Theory string `json:"theory"`
}

var pr_persist_mu sync.Mutex

func pr_state_path(pid int64) string {
	return filepath.Join(PR_STATE_DIR, fmt.Sprintf("pr_%d.json", pid))
}

func PRSave(pid int64) {
	pr_persist_mu.Lock()
	defer pr_persist_mu.Unlock()

	pr := GetPR(pid)
	if pr == nil {
		return
	}

	prStepsMu.Lock()
	steps := prSteps[pid]
	cursor := prCursor[pid]
	prStepsMu.Unlock()

	var savedSteps []pr_saved_step
	for i, s := range steps {
		errStr := ""
		if s.Err != nil {
			errStr = s.Err.Error()
		}
		dur := int64(0)
		if !s.Finished.IsZero() {
			dur = s.Finished.Sub(s.Started).Milliseconds()
		}
		savedSteps = append(savedSteps, pr_saved_step{
			Kind:     s.Kind,
			Owner:    s.Owner,
			Repo:     s.Repo,
			SHA:      s.SHA,
			Path:     s.Path,
			Command:  s.Command,
			Dir:      s.Dir,
			Result:   s.Result,
			Files:    s.Files,
			Done:     i < cursor,
			Err:      errStr,
			Duration: dur,
		})
	}

	theorem_mu.Lock()
	var savedTheorems []theorem_saved
	for i := 0; i < nr_theorems; i++ {
		t := theorem_table[i]
		savedTheorems = append(savedTheorems, theorem_saved{
			Repo: t.repo, SHA: t.sha, Theory: t.theory,
		})
	}
	theorem_mu.Unlock()

	state := pr_saved{
		Pid:      pid,
		Repo:     pr.Repo,
		Branch:   pr.Branch,
		Title:    pr.Title,
		Refs:     pr.RefRepos,
		Steps:    savedSteps,
		Cursor:   cursor,
		Created:  pr.CreateTime,
		Theorems: savedTheorems,
	}

	os.MkdirAll(PR_STATE_DIR, 0755)
	data, _ := json.MarshalIndent(state, "", "  ")
	path := pr_state_path(pid)
	os.WriteFile(path, data, 0644)
	log.Printf("pr_persist: saved pid %d -> %s (%d steps, cursor=%d, %d theorems)",
		pid, path, len(savedSteps), cursor, len(savedTheorems))
}

func PRLoad(pid int64) bool {
	pr_persist_mu.Lock()
	defer pr_persist_mu.Unlock()

	path := pr_state_path(pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}

	var state pr_saved
	if json.Unmarshal(data, &state) != nil {
		return false
	}

	pr := &PRProcess{
		Repo:       state.Repo,
		Branch:     state.Branch,
		Title:      state.Title,
		RefRepos:   state.Refs,
		CreateTime: state.Created,
	}
	prMu.Lock()
	prTable[pid] = pr
	prMu.Unlock()

	prStepsMu.Lock()
	prSteps[pid] = nil
	for _, ss := range state.Steps {
		s := &Step{
			Kind:    ss.Kind,
			Owner:   ss.Owner,
			Repo:    ss.Repo,
			SHA:     ss.SHA,
			Path:    ss.Path,
			Command: ss.Command,
			Dir:     ss.Dir,
			Result:  ss.Result,
			Files:   ss.Files,
		}
		if ss.Err != "" {
			s.Err = fmt.Errorf("%s", ss.Err)
		}
		prSteps[pid] = append(prSteps[pid], s)
	}
	prCursor[pid] = state.Cursor
	prStepsMu.Unlock()

	theorem_mu.Lock()
	for _, t := range state.Theorems {
		if nr_theorems < NR_THEOREMS {
			theorem_table[nr_theorems] = theorem_entry{
				repo: t.Repo, sha: t.SHA, theory: t.Theory,
			}
			nr_theorems++
		}
	}
	theorem_mu.Unlock()

	log.Printf("pr_persist: loaded pid %d from %s (%d steps, cursor=%d, %d theorems)",
		pid, path, len(state.Steps), state.Cursor, len(state.Theorems))
	return true
}

func PRListSaved() []int64 {
	entries, err := os.ReadDir(PR_STATE_DIR)
	if err != nil {
		return nil
	}
	var pids []int64
	for _, e := range entries {
		var pid int64
		if _, err := fmt.Sscanf(e.Name(), "pr_%d.json", &pid); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}
