package claudeweb

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

// 11/21 06:37:42 the evaluation's search a07300a2 had the dev repository cloned here with the
// kernel's credentials (git_twin), and from 06:44 ran benchmarks here. A conversation whose
// answer is text or the web's results has nothing run on this machine; a dev conversation does.
func TestASearchConversationHasNothingRunHere(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	m := &Model{Shadow: &ShadowExecutor{WorkDir: t.TempDir(), Enabled: true}}
	dir := t.TempDir()
	for role, runs := range map[string]bool{"eval_discover": false, "ref_discover": false, "detect_dev": false, "dev_fix_1_u1": true} {
		conv := "c-" + role
		convRoles.Store(conv, role)
		mark := filepath.Join(dir, role)
		m.shadowExecute(conv, "bash_tool", jsonCommand("touch "+mark))
		convRoles.Delete(conv)
		_, err := os.Stat(mark)
		if (err == nil) != runs {
			t.Fatalf("role %s: ran here = %v, want %v", role, err == nil, runs)
		}
	}
}
