package claudeweb

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

// 11/18 01:47:03 dev_fix_1 of uf7cdc0dcca (7ad91890) refreshed its repo the way its prompt's
// "[DEV REPO] … git clone --branch <dev branch> <url> /home/claude/baloon" suggests:
// "mkdir -p /home/claude && rm -rf /home/claude/baloon && git clone -q --branch … /home/claude/baloon".
// The sandbox got the branch tip again; here rm ran and the clone was a no-op, and the mirror
// was gone until 03:50. Here the mirror is the tip again, as in the sandbox — the conversation's
// earlier writes gone on both machines.
// (the URL is one the kernel makes no twin of, so nothing clones it here in the background)
func TestACloneIntoTheMirrorGivesItTheBranchTipHereToo(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	clone := t.TempDir() // the kernel's clone of the dev repo
	git := func(args ...string) string {
		c := exec.Command("git", append([]string{"-C", clone}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	os.MkdirAll(filepath.Join(clone, ".einsteinOS/port"), 0o755)
	os.WriteFile(filepath.Join(clone, ".einsteinOS/port/uf7cdc0dcca.tsv"), []byte("src/core/layer.cpp\tblock_layer\tsrc/core/layer.cpp\tblock_layer\n"), 0o644)
	git("add", "-A")
	git("commit", "-qm", "tip")
	sha := git("rev-parse", "HEAD")

	home := t.TempDir()
	mirror := filepath.Join(home, "baloon")
	os.MkdirAll(filepath.Join(mirror, ".einsteinOS/port"), 0o755)
	os.WriteFile(filepath.Join(mirror, ".einsteinOS/port/uf7cdc0dcca.tsv"), []byte("src/core/layer.cpp\tblock_layer\tsrc/core/layer.cpp\tblock_layer\n"), 0o644)
	os.WriteFile(filepath.Join(mirror, "written_before.txt"), []byte("x"), 0o644)
	kernel.RegisterMirrorTip(mirror, clone, sha)
	defer kernel.UnregisterMirrorTip(mirror)

	s := &ShadowExecutor{WorkDir: t.TempDir(), Enabled: true}
	cmd := "mkdir -p " + home + " && rm -rf " + mirror + " && git clone -q --branch einsteinOS/dev-c880662efd /nonexistent/baloon.git " + mirror +
		" && cd " + mirror + " && git config user.email \"zeng-bin3123@users.noreply.github.com\"" +
		" && printf 'src/core/mtp.cpp\\tBump\\tsrc/core/mtp.cpp\\tBump\\n' >> .einsteinOS/port/uf7cdc0dcca.tsv && git add -A && git commit -qm 'port Bump'"
	s.Execute("bash_tool", jsonCommand(cmd))

	b, err := os.ReadFile(filepath.Join(mirror, ".einsteinOS/port/uf7cdc0dcca.tsv"))
	if err != nil || !strings.Contains(string(b), "block_layer") {
		t.Fatalf("the mirror after the conversation's clone: %v %q", err, b)
	}
	// what the command did next in the clone happened here too
	if !strings.Contains(string(b), "Bump") || strings.Count(string(b), "\n") != 2 {
		t.Fatalf("the line the command appended after its clone: %q", b)
	}
	if _, err := os.Stat(filepath.Join(mirror, "written_before.txt")); err == nil {
		t.Fatalf("a file the clone replaced is still in the mirror")
	}
	if _, err := os.Stat(filepath.Join(mirror, ".git")); err == nil {
		t.Fatalf("the mirror has a .git")
	}

	// a clone into the mirror that still holds files fails in the sandbox and changes nothing here
	os.WriteFile(filepath.Join(mirror, "new.txt"), []byte("y"), 0o644)
	s.Execute("bash_tool", jsonCommand("git clone -q /nonexistent/baloon.git "+mirror))
	if _, err := os.Stat(filepath.Join(mirror, "new.txt")); err != nil {
		t.Fatalf("a failed clone changed the mirror: %v", err)
	}
}
