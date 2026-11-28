package kernel

import (
	"os"
	"testing"
)

// A .git in the unit's mirror is not the kernel's clone: 11/13 04:21:46 a dev conversation
// ran git init in /home/claude/baloon and the kernel took it for its clone from then on.
func TestTheMirrorIsNeverTheKernelsClone(t *testing.T) {
	repo := "o/einsteinos_probe_mirror_clone"
	m := devMirrorDir(repo)
	if err := os.MkdirAll(m+"/.git", 0o755); err != nil {
		t.Skip("no /home/claude here:", err)
	}
	t.Cleanup(func() { os.RemoveAll(m) })
	if got := legacyDevClone(repo); got != "" {
		t.Fatalf("the mirror %s is taken for the kernel's clone: %s", m, got)
	}
}
