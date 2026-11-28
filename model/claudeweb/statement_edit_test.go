package claudeweb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

var kernelInitOnce sync.Once

// 2026/11/03 06:59:22: Axle showed scratch_640's numbers were wrong, the chunk corrected
// the statement, and the shadow refused the edit on this machine only. The local copy
// must follow the sandbox: the edit is applied.
func TestAStatementCorrectionIsAppliedLikeTheSandboxDid(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	dir := t.TempDir()
	f := filepath.Join(dir, "SharedExpertCombine.lean")
	old := "theorem scratch_640 : scratchBytes 640 = 2 * 2560 + 680 + 584 + 32 := by decide"
	corrected := "theorem scratch_640 : scratchBytes 640 = 2 * 2560 + 688 + 592 + 32 := by decide"
	if err := os.WriteFile(f, []byte("-- header\n"+old+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]string{"path": f, "old_str": old, "new_str": corrected})
	s := &ShadowExecutor{WorkDir: dir, Enabled: true}
	out := s.Execute("str_replace", string(in))
	if strings.Contains(out, "ERROR") || strings.Contains(out, "sorry") {
		t.Fatalf("the edit was refused: %q", out)
	}
	got, _ := os.ReadFile(f)
	if !strings.Contains(string(got), corrected) || strings.Contains(string(got), "680 + 584") {
		t.Fatalf("local copy not equal to the sandbox's:\n%s", got)
	}
}
