package claudeweb

import (
	"os"
	"path/filepath"
	"testing"
)

// 11/17 dev_unit_8's conversation (09:37–10:01) wrote /tmp/FULLFIX.py and /tmp/wip_uc81_work
// here; dev_planner, the next conversation, read them (10:23:14) and ran FULLFIX.py (10:27:13).
// What a conversation leaves in this machine's /tmp goes when it ends: only what its commands
// named, only what was not there before, never the kernel's own temp directories.
func TestWhatAConversationLeavesInTmpGoesWithIt(t *testing.T) {
	root := t.TempDir()
	old := tmpRoot
	tmpRoot = root
	defer func() { tmpRoot = old }()
	mk := func(name string) {
		os.MkdirAll(filepath.Join(root, name), 0o755)
		os.WriteFile(filepath.Join(root, name, "f"), []byte("x"), 0o644)
	}
	mk("before_the_conversation")
	before := tmpEntries()
	mk("wip_uc81_work")
	os.WriteFile(filepath.Join(root, "FULLFIX.py"), []byte("print(1)"), 0o644)
	mk("einsteinos_diag")
	mk("another_process_s")

	const conv = "e2e-dev-unit-8"
	noteConvTmp(conv, `{"command":"cat > /tmp/FULLFIX.py << 'PYEOF'\n…\nPYEOF\nls /tmp/wip_uc81_work/lean/Baloon/"}`)
	noteConvTmp(conv, `{"command":"cat /tmp/before_the_conversation/f; cat /tmp/einsteinos_diag/x"}`)

	got := clearConvTmp(conv, before)
	if len(got) != 2 || got[0] != "FULLFIX.py" || got[1] != "wip_uc81_work" {
		t.Fatalf("removed %v", got)
	}
	for _, keep := range []string{"before_the_conversation", "einsteinos_diag", "another_process_s"} {
		if _, err := os.Stat(filepath.Join(root, keep)); err != nil {
			t.Fatalf("%s was removed", keep)
		}
	}
	if got := clearConvTmp(conv, before); len(got) != 0 {
		t.Fatalf("a second clear removed %v", got)
	}
}
