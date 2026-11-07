package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps the package's tests off this machine's real state files: the cursor
// (ref_cursor_store.go) is written under a temporary directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kernel-test-")
	if err == nil {
		cursorPath = func() string { return filepath.Join(dir, "cursor.json") }
	}
	code := m.Run()
	if dir != "" {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}
