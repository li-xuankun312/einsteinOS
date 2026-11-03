package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestMirrorPrimeDeltaAndSkips(t *testing.T) {
	tip, mirror := t.TempDir(), t.TempDir()
	write(t, tip, "src/a.cu", "A")
	write(t, tip, "src/b.cu", "B")
	write(t, tip, ".git/HEAD", "ref")
	write(t, mirror, "stale_from_last_unit.cu", "STALE") // must be wiped by priming

	base, err := primeMirror(tip, mirror)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base[".git/HEAD"]; ok {
		t.Fatal(".git must never be in the mirror: it would make devRepoLocalDir take the mirror for the kernel clone")
	}
	if _, err := os.Stat(filepath.Join(mirror, "stale_from_last_unit.cu")); err == nil {
		t.Fatal("priming must wipe the mirror")
	}
	if len(base) != 2 {
		t.Fatalf("manifest %v", base)
	}

	// the child edits a file, adds one, removes one, and its build leaves outputs
	write(t, mirror, "src/a.cu", "A2")
	write(t, mirror, "src/new.cu", "N")
	os.Remove(filepath.Join(mirror, "src/b.cu"))
	write(t, mirror, "build/x.o", "obj")
	write(t, mirror, "src/kernel.o", "obj")
	write(t, mirror, "big.bin", strings.Repeat("z", devMaxFileBytes+1))

	changed, removed, err := treeDelta(mirror, base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(changed, ",") != "src/a.cu,src/new.cu" {
		t.Fatalf("changed=%v (object files and the 2MB+ blob must not count)", changed)
	}
	if strings.Join(removed, ",") != "src/b.cu" {
		t.Fatalf("removed=%v", removed)
	}

	// delta applied onto a tip yields the child's tree, nothing else
	ws, err := assembleWorkspace(tip, mirror, changed, removed)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ws)
	if read(t, ws, "src/a.cu") != "A2" || read(t, ws, "src/new.cu") != "N" || read(t, ws, "src/b.cu") != "<missing>" {
		t.Fatal("workspace is not tip+delta")
	}
	if read(t, ws, "src/kernel.o") != "<missing>" {
		t.Fatal("build output leaked into the workspace")
	}
}

func withRunner(t *testing.T, fn func(cmd string) (int, string)) *[]string {
	var ran []string
	old := devVerifyRunner
	devVerifyRunner = func(cmd, dir string, to time.Duration) (int, string, error) {
		ran = append(ran, cmd)
		c, o := fn(cmd)
		return c, o, nil
	}
	t.Cleanup(func() { devVerifyRunner = old })
	return &ran
}

func TestRunUnitVerifyUsesExitCodesNotText(t *testing.T) {
	ws := t.TempDir()
	build, test := devVerifyScripts("uabc")
	write(t, ws, build, "make")
	write(t, ws, test, "./t")

	// build output says "error:" but exits 0 → pass (a return code, not a word search)
	ran := withRunner(t, func(cmd string) (int, string) {
		if strings.Contains(cmd, "build.sh") {
			return 0, "warning: error: this is only text"
		}
		return 0, "all ok"
	})
	r := runUnitVerify(ws, "uabc", "0,2,3", time.Minute, time.Minute)
	if !r.Passed() {
		t.Fatalf("exit 0/0 must pass: %+v", r)
	}
	if len(*ran) != 2 || !strings.Contains((*ran)[0], "/usr/local/corex/bin") || !strings.Contains((*ran)[0], "CUDA_VISIBLE_DEVICES=\"0,2,3\"") {
		t.Fatalf("scripts must run with the corex PATH and the usable devices: %v", *ran)
	}

	// build fails → test.sh never runs, smoke skipped, the error tail is carried
	ran = withRunner(t, func(cmd string) (int, string) { return 2, "nvcc: fatal: no such file kernel.cu" })
	r = runUnitVerify(ws, "uabc", "", time.Minute, time.Minute)
	if r.Compile != "fail" || r.Smoke != "skipped" || r.BuildExit != 2 || len(*ran) != 1 {
		t.Fatalf("%+v ran=%d", r, len(*ran))
	}
	if !strings.Contains(r.Status()["compile_error"], "no such file kernel.cu") {
		t.Fatalf("status lost the build output: %v", r.Status())
	}

	// build ok, test fails
	withRunner(t, func(cmd string) (int, string) {
		if strings.Contains(cmd, "test.sh") {
			return 1, "mismatch at index 7"
		}
		return 0, ""
	})
	r = runUnitVerify(ws, "uabc", "", time.Minute, time.Minute)
	if r.Compile != "pass" || r.Smoke != "fail" || r.Passed() {
		t.Fatalf("%+v", r)
	}
}

func TestRunUnitVerifyMissingScriptsAreNotPass(t *testing.T) {
	ws := t.TempDir()
	withRunner(t, func(string) (int, string) { t.Fatal("nothing must run without a script"); return 0, "" })
	r := runUnitVerify(ws, "uabc", "", time.Minute, time.Minute)
	if r.Compile != "missing" || r.Passed() {
		t.Fatalf("%+v", r)
	}
	build, _ := devVerifyScripts("uabc")
	write(t, ws, build, "true")
	withRunner(t, func(string) (int, string) { return 0, "" })
	r = runUnitVerify(ws, "uabc", "", time.Minute, time.Minute)
	if r.Compile != "pass" || r.Smoke != "missing" || r.Passed() {
		t.Fatalf("a unit with a build but no tests must not pass: %+v", r)
	}
}

// ---- end to end against a local bare repository ---------------------------------------

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func setupRemote(t *testing.T) (bare, clone string) {
	bare = filepath.Join(t.TempDir(), "origin.git")
	git(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	seed := t.TempDir()
	git(t, seed, "init", "-q", "-b", "main")
	write(t, seed, "src/base.cu", "base")
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-q", "-m", "seed")
	git(t, seed, "remote", "add", "origin", bare)
	git(t, seed, "push", "-q", "origin", "main")
	git(t, seed, "push", "-q", "origin", "main:einsteinOS/dev-7")
	clone = filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(clone), "clone", "-q", bare, clone)
	old := devGitPush
	devGitPush = func(dir, repo, branch string, n int) error {
		c := exec.Command("git", "push", "-q", "origin", branch)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Logf("push: %s", out)
			return err
		}
		return nil
	}
	t.Cleanup(func() { devGitPush = old })
	oldDel := devGitDeleteBranch
	devGitDeleteBranch = func(dir, repo, branch string) error {
		c := exec.Command("git", "push", "-q", "origin", "--delete", branch)
		c.Dir = dir
		_, err := c.CombinedOutput()
		return err
	}
	t.Cleanup(func() { devGitDeleteBranch = oldDel })
	return bare, clone
}

func remoteHas(t *testing.T, bare, branch, file string) bool {
	c := exec.Command("git", "--git-dir", bare, "cat-file", "-e", branch+":"+file)
	return c.Run() == nil
}

func remoteHasBranch(bare, branch string) bool {
	return exec.Command("git", "--git-dir", bare, "rev-parse", "--verify", "-q", "refs/heads/"+branch).Run() == nil
}

func TestVerifyAndPush_PassGoesToDevBranch_FailGoesToWip_FixPromotes(t *testing.T) {
	bare, clone := setupRemote(t)
	mirror := t.TempDir()
	const branch = "einsteinOS/dev-7"
	unit := "u123"
	build, test := devVerifyScripts(unit)

	// --- attempt 1: build.sh fails on the real machine ---
	git(t, clone, "checkout", "-q", "-B", branch, "origin/"+branch)
	base, err := primeMirror(clone, mirror)
	if err != nil {
		t.Fatal(err)
	}
	write(t, mirror, "src/impl.cu", "broken")
	write(t, mirror, build, "x")
	write(t, mirror, test, "x")
	withRunner(t, func(cmd string) (int, string) { return 1, "impl.cu(3): error: identifier undefined" })

	o := devVerifyAndPush(clone, mirror, "o/r", branch, branch, unit, "ActivationQuantLayout", "0", base, UnitRequirements{})
	if o.Err != nil {
		t.Fatal(o.Err)
	}
	if o.Result.Compile != "fail" || o.Pushed != devWipBranch(branch, unit) {
		t.Fatalf("outcome %+v", o)
	}
	if remoteHas(t, bare, branch, "src/impl.cu") {
		t.Fatal("UNVERIFIED code reached the dev branch")
	}
	if !remoteHas(t, bare, devWipBranch(branch, unit), "src/impl.cu") {
		t.Fatal("failing code must be on the wip branch so the fix conversation can clone it")
	}

	// --- attempt 2 (a fix): mirror primed from the WIP tip; build and test now pass ---
	wip := devWipBranch(branch, unit)
	git(t, clone, "fetch", "-q", "origin", wip)
	git(t, clone, "checkout", "-q", "-B", wip, "origin/"+wip)
	base2, err := primeMirror(clone, mirror)
	if err != nil {
		t.Fatal(err)
	}
	write(t, mirror, "src/impl.cu", "fixed")
	withRunner(t, func(cmd string) (int, string) { return 0, "ok" })
	o = devVerifyAndPush(clone, mirror, "o/r", branch, wip, unit, "ActivationQuantLayout", "0", base2, UnitRequirements{})
	if o.Err != nil {
		t.Fatal(o.Err)
	}
	if !o.Result.Passed() || o.Pushed != branch {
		t.Fatalf("outcome %+v", o)
	}
	if !remoteHas(t, bare, branch, "src/impl.cu") || !remoteHas(t, bare, branch, "src/base.cu") {
		t.Fatal("verified fix must be on the dev branch together with the earlier tree")
	}
	if remoteHasBranch(bare, wip) {
		t.Fatal("the wip branch was merged into the dev branch and must be deleted (11/03 left two behind)")
	}
	if got := git(t, clone, "--no-pager", "show", "origin/"+branch+":src/impl.cu"); !strings.Contains(got, "fixed") {
		// origin ref may be stale in this clone; read the bare repo directly
		out, _ := exec.Command("git", "--git-dir", bare, "show", branch+":src/impl.cu").Output()
		if !strings.Contains(string(out), "fixed") {
			t.Fatalf("dev branch has %q, want the fixed version", out)
		}
	}
}

func TestVerifyAndPush_FirstTryPassIsOnePushToDevAndNoWip(t *testing.T) {
	bare, clone := setupRemote(t)
	mirror := t.TempDir()
	const branch = "einsteinOS/dev-7"
	unit := "u9"
	build, test := devVerifyScripts(unit)
	git(t, clone, "checkout", "-q", "-B", branch, "origin/"+branch)
	base, _ := primeMirror(clone, mirror)
	write(t, mirror, "src/impl.cu", "good")
	write(t, mirror, build, "x")
	write(t, mirror, test, "x")
	withRunner(t, func(string) (int, string) { return 0, "ok" })
	o := devVerifyAndPush(clone, mirror, "o/r", branch, branch, unit, "m", "", base, UnitRequirements{})
	if o.Err != nil || !o.Result.Passed() || o.Pushed != branch {
		t.Fatalf("%+v", o)
	}
	if !remoteHas(t, bare, branch, "src/impl.cu") {
		t.Fatal("verified code not on the dev branch")
	}
	if remoteHasBranch(bare, devWipBranch(branch, unit)) {
		t.Fatal("a unit that passed first time needs no wip branch")
	}
}

func TestVerifyAndPush_NothingWrittenIsNotAPass(t *testing.T) {
	_, clone := setupRemote(t)
	mirror := t.TempDir()
	const branch = "einsteinOS/dev-7"
	git(t, clone, "checkout", "-q", "-B", branch, "origin/"+branch)
	base, _ := primeMirror(clone, mirror)
	withRunner(t, func(string) (int, string) { t.Fatal("must not run anything"); return 0, "" })
	o := devVerifyAndPush(clone, mirror, "o/r", branch, branch, "u0", "m", "", base, UnitRequirements{})
	if o.Err != nil || o.Result.Passed() || o.Pushed != "" {
		t.Fatalf("%+v", o)
	}
	if !strings.Contains(o.Status["compile_error"], "wrote no file") {
		t.Fatalf("%v", o.Status)
	}
}
