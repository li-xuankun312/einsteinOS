package kernel

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 11/04 commit 0 was theorized twice and its modules renamed; a complete stored plan is read
// back instead, an incomplete one is not used.
func TestACompleteStoredTheoryIsReusedAnIncompleteOneIsNot(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	put := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	sha := "01529aaa82b9ffffffffffffffffffffffffffff"
	run("init", "-q", "-b", "einsteinOS/theory")
	plan := func(id string, chunks int) string {
		b, _ := json.Marshal(theoryPlanRef{Ref: sha, Plan: id, Chunks: make([][]string, chunks)})
		d := ".einsteinOS/theory/01529aaa82b9/" + id
		put(d+"/plan.json", string(b))
		return d
	}
	full := plan("64014d8d29", 2)
	put(full+"/01-aaaa/GgufReader.lean", "theorem a : True := trivial")
	put(full+"/02-bbbb/MtpDrafter.lean", "theorem b : True := trivial")
	part := plan("7da0eac920", 2)
	put(part+"/01-cccc/Other.lean", "theorem c : True := trivial")
	run("add", "-A")
	run("commit", "-qm", "store")

	text, used, ok := storedTheoryIn(dir, "einsteinOS/theory", sha, nil)
	if !ok || used != full {
		t.Fatalf("the complete plan: ok=%v used=%s", ok, used)
	}
	for _, want := range []string{"## FILE: " + TheoryOutputsDir + "/GgufReader.lean", "## FILE: " + TheoryOutputsDir + "/MtpDrafter.lean", "theorem b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "Other.lean") {
		t.Fatal("the incomplete plan's files must not be used")
	}
	if _, _, ok := storedTheoryIn(dir, "einsteinOS/theory", "ffffffffffff"+sha[12:], nil); ok {
		t.Fatal("another commit has no stored theory")
	}
}

// Of two complete stored theories of one commit, the one the dev branch's plan was built from
// is used, not the newer one. 11/09 13:49 commit 2's newer theory (3a7c20346e, made by a
// second run) was taken, and its plan lost the units that had passed.
func TestTheStoredTheoryThePlanWasBuiltFromIsUsed(t *testing.T) {
	dir := t.TempDir()
	run := func(date string, args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	put := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	sha := "e5509707730d31f9e6d29ea3ac5f3c98f9d34b54"
	plan := func(id string) string {
		b, _ := json.Marshal(theoryPlanRef{Ref: sha, Plan: id, Chunks: make([][]string, 1)})
		d := ".einsteinOS/theory/e5509707730d/" + id
		put(d+"/plan.json", string(b))
		return d
	}
	run("2026-11-09T08:38:00Z", "init", "-q", "-b", "einsteinOS/theory")
	first := plan("61aa998c60")
	put(first+"/01-595e/BlockDequantizers.lean", "theorem a : True := trivial")
	put(first+"/01-595e/CpuExpertPool.lean", "theorem b : True := trivial")
	run("2026-11-09T08:38:00Z", "add", "-A")
	run("2026-11-09T08:38:00Z", "commit", "-qm", "first")
	second := plan("3a7c20346e")
	put(second+"/01-1e6e/ExpertPoolRuntime.lean", "theorem c : True := trivial")
	run("2026-11-09T10:55:00Z", "add", "-A")
	run("2026-11-09T10:55:00Z", "commit", "-qm", "second")

	if _, used, _ := storedTheoryIn(dir, "einsteinOS/theory", sha, nil); used != second {
		t.Fatalf("without a plan the newest: %s", used)
	}
	planMods := map[string]bool{"BlockDequantizers.lean": true, "CpuExpertPool.lean": true}
	text, used, ok := storedTheoryIn(dir, "einsteinOS/theory", sha, planMods)
	if !ok || used != first || !strings.Contains(text, "BlockDequantizers.lean") {
		t.Fatalf("the plan's theory: ok=%v used=%s", ok, used)
	}
}
