package kernel

import (
	"path/filepath"
	"testing"
)

// 11/04: a restart on another machine began again at commit 0
func TestTheCursorAndTheNextBaseSurviveARestart(t *testing.T) {
	old := cursorPath
	p := filepath.Join(t.TempDir(), "cursor.json")
	cursorPath = func() string { return p }
	defer func() { cursorPath = old }()
	if storedNextIndex("o/r") != 0 || storedBase("o/d") != "" {
		t.Fatal("nothing stored yet")
	}
	saveCursor(func(st *cursorState) { st.Next["o/r"] = 1 })
	recordCompletedBranch("o/d", "einsteinOS/dev-abc")
	devBasesMu.Lock()
	delete(devBases, "o/d") // a new process
	devBasesMu.Unlock()
	if storedNextIndex("o/r") != 1 || devNewBranchBase("o/d") != "einsteinOS/dev-abc" {
		t.Fatal("read back after a restart")
	}
}
