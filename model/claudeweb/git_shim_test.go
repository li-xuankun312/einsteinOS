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

// 11/13 00:22:35: unit 7 wrote /tmp/guard.sh and /tmp/step.sh in a command that also ran git.
// The write went to an absolute path, the old pattern did not see it, the whole command was
// skipped here, and the ten commands that called the scripts exited 127 on this machine while
// the sandbox reported 0. Whatever follows git after ; && || or a new line runs here.
func TestWhatFollowsGitRunsHere(t *testing.T) {
	for _, cmd := range []string{
		"git diff > /tmp/x.patch; cat > /tmp/guard.sh <<'EOF'\nexit 0\nEOF\nchmod +x /tmp/guard.sh",
		"git status; cat > /home/claude/u7tools/step.sh <<'EOF'\necho\nEOF",
		"cd /home/claude/baloon && git add -A && chmod +x /tmp/step.sh",
		"git log -1 || rm -f /tmp/stale",
	} {
		if !isGitCommand(cmd) || !gitCommandWritesFiles(cmd) {
			t.Errorf("must run here: %q", cmd)
		}
	}
	for _, pure := range []string{"git status", "cd /home/claude/baloon && git log --oneline -5",
		"git clone https://github.com/o/r.git /tmp/r 2>&1 | tail -2", "git fetch -q origin; git log -1"} {
		if gitCommandWritesFiles(pure) {
			t.Errorf("only git: %q", pure)
		}
	}
	dir := t.TempDir()
	abs := filepath.Join(dir, "guard.sh")
	c := exec.Command("bash", "-c", gitShim+"git status; cat > "+abs+" <<'EOF'\nexit 0\nEOF\nchmod +x "+abs)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if st, err := os.Stat(abs); err != nil || st.Mode()&0o100 == 0 {
		t.Fatalf("%s not written executable here (%v)", abs, err)
	}
}
