package kernel

// ref_cursor_store.go — the reference cursor and the next dev branch's base survive a restart.
//
// Both lived only in the process (refBound, devBases): on 11/04 a restart on another machine
// began again at commit 0 of 2438. They are now written to a file on this machine whenever
// they move, and read back when a run starts at the beginning.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

type cursorState struct {
	Next  map[string]int    `json:"next"`  // reference repo → index of the next commit to work on
	Bases map[string]string `json:"bases"` // dev repo → branch of its last completed pipeline
}

var (
	cursorMu   sync.Mutex
	cursorPath = func() string {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.TempDir()
		}
		return filepath.Join(home, ".einsteinOS", "cursor.json")
	}
)

func loadCursor() cursorState {
	st := cursorState{Next: map[string]int{}, Bases: map[string]string{}}
	if b, err := os.ReadFile(cursorPath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Next == nil {
		st.Next = map[string]int{}
	}
	if st.Bases == nil {
		st.Bases = map[string]string{}
	}
	return st
}

func saveCursor(update func(*cursorState)) {
	cursorMu.Lock()
	defer cursorMu.Unlock()
	st := loadCursor()
	update(&st)
	b, _ := json.MarshalIndent(st, "", "  ")
	p := cursorPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		log.Printf("ref_cursor: could not store the cursor at %s: %v", p, err)
	}
}

// storedNextIndex is where a run that starts at the beginning of repo resumes.
func storedNextIndex(repo string) int {
	cursorMu.Lock()
	defer cursorMu.Unlock()
	return loadCursor().Next[repo]
}

// storedBase is the stored base for a new dev branch of devRepo.
func storedBase(devRepo string) string {
	cursorMu.Lock()
	defer cursorMu.Unlock()
	return loadCursor().Bases[devRepo]
}
