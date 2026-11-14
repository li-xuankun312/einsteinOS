package kernel

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestMain keeps the package's tests off this machine's real state files: the cursor
// (ref_cursor_store.go) and the built launch probe (launch_probe.go) are written under a
// temporary directory. Afterwards ~/.einsteinOS must be as it was before the tests: a file
// a test created, changed or removed there fails the run.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kernel-test-")
	if err == nil {
		cursorPath = func() string { return filepath.Join(dir, "cursor.json") }
		// 11/09 the tests built launch_probe_*.so into ~/.einsteinOS/launchprobe
		launchProbeDir = func() string { return filepath.Join(dir, "launchprobe") }
	}
	state := ""
	if home, err := os.UserHomeDir(); err == nil {
		state = filepath.Join(home, ".einsteinOS")
	}
	before := stateSnapshot(state)
	code := m.Run()
	if changed := stateChanges(before, stateSnapshot(state)); len(changed) > 0 {
		fmt.Fprintf(os.Stderr, "the tests changed this machine's %s:\n", state)
		for _, c := range changed {
			fmt.Fprintln(os.Stderr, "  "+c)
		}
		code = 1
	}
	if dir != "" {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}

// stateSnapshot: every file under dir with its size and modification time ("" or a missing
// dir: none).
func stateSnapshot(dir string) map[string]string {
	out := map[string]string{}
	if dir == "" {
		return out
	}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			out[p] = fmt.Sprintf("%d %d", info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return out
}

// stateChanges: the files created, changed or removed between two snapshots, sorted.
func stateChanges(before, after map[string]string) []string {
	var out []string
	for p, a := range after {
		if b, ok := before[p]; !ok {
			out = append(out, "created "+p)
		} else if a != b {
			out = append(out, "changed "+p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, "removed "+p)
		}
	}
	sort.Strings(out)
	return out
}
