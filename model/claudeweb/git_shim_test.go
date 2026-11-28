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

// A git command that changes the files of the tree (git rm, mv, checkout -- f, restore, apply,
// reset --hard, clean, stash) writes files as much as a redirect does. 11/19 12:36:33 the dev
// conversation of p3798e66f3c removed include/ref/ngram/ with a command of git alone: skipped
// here, the file stayed in the mirror, and 12:41:32 and 12:50:21 the kernel told it twice that
// its files were those of the rejected verification (ref_path_written) — the third time ends the
// conversation and judges the tree; pb0569948de too from 12:36:52.
func TestAGitCommandThatChangesTheTreeRunsHere(t *testing.T) {
	for _, cmd := range []string{
		"cd /home/claude/baloon-p3798e66f3c && git rm -r -q include/ref/ngram && git commit -qm 'drop the reference header'",
		"git -C /home/claude/baloon-pb0569948de rm -q include/ref/prefill/gemm.hpp",
		"git mv include/ref/plan/plan.hpp include/baloon/plan.hpp",
		"git checkout -- include/baloon/plan.hpp",
		"git restore --source=HEAD~1 include/baloon/logits_selection.hpp",
		"git apply /tmp/fix.patch",
		"git reset --hard HEAD~1",
		"git clean -fdq include/ref",
		"git stash",
	} {
		if !isGitCommand(cmd) || !gitCommandWritesFiles(cmd) {
			t.Errorf("must run here: %q", cmd)
		}
	}
	for _, pure := range []string{"git status", "git diff --stat", "git log --oneline -5", "git show HEAD:include/baloon/plan.hpp",
		"git -C /home/claude/baloon-p3798e66f3c ls-files include/ref", "git checkout -b wip", "git reset --soft HEAD~1", "git stash list"} {
		if gitCommandWritesFiles(pure) {
			t.Errorf("changes no file of the tree: %q", pure)
		}
	}
	// and it does remove the file here
	dir := t.TempDir()
	run := func(s string) {
		c := exec.Command("bash", "-c", s)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", s, err, out)
		}
	}
	run("git init -q && git config user.email a@b && git config user.name a && mkdir -p include/ref/ngram && echo x > include/ref/ngram/ple_reader.hpp && git add -A && git commit -qm init")
	run(gitShim + "git rm -r -q include/ref/ngram && git commit -qm drop && git push origin main")
	if _, err := os.Stat(filepath.Join(dir, "include/ref/ngram/ple_reader.hpp")); err == nil {
		t.Fatal("git rm run here left the file")
	}
}
