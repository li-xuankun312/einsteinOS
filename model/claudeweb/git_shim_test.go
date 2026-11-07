package claudeweb

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 11/03 21:37:38: build.sh and test.sh written in a command that also ran git never reached
// this machine. Such a command runs here with the git network commands as no-ops.
func TestAGitCommandThatWritesFilesStillWritesThemHere(t *testing.T) {
	cmd := "mkdir -p v/u1 && cat > v/u1/build.sh <<'EOF'\necho build\nEOF\ngit add -A && git commit -qm x && git push origin main && echo ok > v/u1/test.sh"
	if !isGitCommand(cmd) || !gitCommandWritesFiles(cmd) {
		t.Fatal("must be recognised as git + writes")
	}
	for _, pure := range []string{"git status", "cd /home/claude/baloon && git log --oneline -5", "git clone https://github.com/o/r.git /tmp/r 2>&1 | tail -2"} {
		if gitCommandWritesFiles(pure) {
			t.Errorf("not a file-writing command: %q", pure)
		}
	}
	dir := t.TempDir()
	c := exec.Command("bash", "-c", gitShim+cmd)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, f := range []string{"v/u1/build.sh", "v/u1/test.sh"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s not written: the chain must continue past git", f)
		}
	}
}
