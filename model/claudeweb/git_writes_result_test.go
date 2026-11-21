package claudeweb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

// (a URL the kernel makes no twin of, so nothing clones it here in the background)
// 11/18 01:51–02:47 dev_fix_1 of uf7cdc0dcca (7ad91890) and spawn_work ea75c5d2 ran their
// clone-and-commit commands here (gitShim) and were never told what happened: the round's
// report listed only the other commands, and both took git for "never mirrored". A command
// with git that fails here comes back with this machine's exit and output; one that exits 0
// here leaves the sandbox's output (with git's) standing.
func TestAGitCommandThatFailsHereComesBackWithThisMachinesResult(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	dir := t.TempDir()
	s := &ShadowExecutor{WorkDir: dir, Enabled: true}
	missing := filepath.Join(dir, "baloon")
	out := s.Execute("bash_tool", jsonCommand("git clone -q /nonexistent/baloon.git "+missing+
		" && cd "+missing+" && printf 'x\\n' >> .einsteinOS/port/u.tsv"))
	if !strings.Contains(out, "exit=1") || !strings.Contains(out, "No such file or directory") {
		t.Fatalf("this machine's failure did not come back: %q", out)
	}
	// exit 0 here: the sandbox's output stands
	os.MkdirAll(filepath.Join(dir, "r"), 0o755)
	out = s.Execute("bash_tool", jsonCommand("cd "+dir+"/r && printf 'y\\n' > f && git push origin main"))
	if out != "" {
		t.Fatalf("a command that exited 0 here replaced the sandbox's output: %q", out)
	}
}
