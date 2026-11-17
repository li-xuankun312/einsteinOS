package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A passed unit whose port map does not cover what the reference requires as measured now is
// reopened when the run starts; one that does, and one that has not passed, are left alone.
// 11/15 08:59 the run restarted on a kernel that measures __launch_bounds__ kernels by their
// names: ued2beb8a35 owned 162 definitions instead of 158, and was not looked at again.
func TestPassedUnitsAreHeldToTheReferenceAgain(t *testing.T) {
	tree := t.TempDir()
	put := func(f, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(tree, f)), 0o755)
		os.WriteFile(filepath.Join(tree, f), []byte(body), 0o644)
	}
	put(PortMapPath("ued2beb8a35"), "src/kernels/cuda/fused_gdn.cu\tgdn_step_kernel\tsrc/kernels/gpu/gdn.cu\tgdn_step_kernel\n")
	put("src/kernels/gpu/gdn.cu", "__global__ void gdn_step_kernel(float* s) { s[0] = 1; }\n")
	put(PortMapPath("u739c93b0c3"), "src/kernels/cuda/rope.cu\trope_kernel\tsrc/kernels/gpu/rope.cu\trope_kernel\n")
	put("src/kernels/gpu/rope.cu", "__global__ void rope_kernel(float* x) { x[0] = 2; }\n")
	plan := &DevPlan{PortPass: map[string]bool{"ued2beb8a35": true, "u739c93b0c3": true},
		PortUnits: []DevPlanNode{
			{ID: "ued2beb8a35", Sources: []string{"src/kernels/cuda/fused_gdn.cu"}},
			{ID: "u739c93b0c3", Sources: []string{"src/kernels/cuda/rope.cu"}},
			{ID: "p4da61fbf21", Sources: []string{"src/kernels/cuda/native_rope.cu"}},
		}}
	req := map[string][]RefSymbol{
		// measured now: a second kernel the unit's file declares with __launch_bounds__
		"ued2beb8a35": {{File: "src/kernels/cuda/fused_gdn.cu", Kind: "kernel", Name: "gdn_step_kernel"},
			{File: "src/kernels/cuda/fused_gdn.cu", Kind: "kernel", Name: "gdn_chunk_kernel"}},
		"u739c93b0c3": {{File: "src/kernels/cuda/rope.cu", Kind: "kernel", Name: "rope_kernel"}},
		"p4da61fbf21": {{File: "src/kernels/cuda/native_rope.cu", Kind: "kernel", Name: "native_rope_kernel"}},
	}
	got := portRecheck(plan, tree, func(n DevPlanNode) UnitRequirements { return UnitRequirements{Symbols: req[n.ID]} })
	if strings.Join(got, " ") != "ued2beb8a35" {
		t.Fatalf("reopened: %v", got)
	}
	f := plan.Reopen["ued2beb8a35"]
	if f.Kind != "port_incomplete" || !strings.Contains(f.Text, "gdn_chunk_kernel") || plan.UnitPass(plan.PortUnits[0]) {
		t.Fatalf("reopen: %+v", f)
	}
}

// 11/17 02:36 (and 11/16 08:42): every passed unit that owns definitions was reopened at the
// restart — "no line in .einsteinOS/port/<id>.tsv" — while the dev branch on GitHub held those
// port maps with as many lines as the reference required (25, 18, 5, 9). devRepoLocalDir syncs
// the kernel's clone with GitCloneOrReuse, which ends in `git reset --hard origin/HEAD`; with
// the dev branch checked out (dev_store_theory leaves it so) that moves the LOCAL dev branch
// to main's commit, and branchTree read the local branch: a tree without the port maps.
func TestBranchTreeReadsTheRemoteBranchNotTheLocalOne(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	g := func(dir string, args ...string) string {
		return diagGitOut(t, dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	g(root, "init", "-q", "--bare", remote)
	work := filepath.Join(root, "work")
	g(root, "clone", "-q", remote, work)
	g(work, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(work, "README"), []byte("base\n"), 0o644)
	g(work, "add", ".")
	g(work, "commit", "-qm", "main")
	g(work, "push", "-q", "origin", "main")
	g(work, "checkout", "-q", "-b", "dev")
	os.MkdirAll(filepath.Join(work, ".einsteinOS", "port"), 0o755)
	os.WriteFile(filepath.Join(work, ".einsteinOS", "port", "u1.tsv"), []byte("a.hpp\tFOO\tb.hpp\tFOO\n"), 0o644)
	g(work, "add", ".")
	g(work, "commit", "-qm", "unit 1 passed")
	g(work, "push", "-q", "origin", "dev")
	g(work, "remote", "set-head", "origin", "main")
	g(work, "fetch", "-q", "origin", "+refs/heads/*:refs/remotes/origin/*")
	g(work, "checkout", "-q", "-B", "dev", "origin/dev") // what gitUseBranchFromRemote leaves
	// what GitCloneOrReuse does every time devRepoLocalDir is called
	g(work, "fetch", "-q", "origin", "+refs/heads/*:refs/remotes/origin/*")
	g(work, "reset", "-q", "--hard", "origin/HEAD")
	if strings.Contains(g(work, "ls-tree", "-r", "--name-only", "dev"), "u1.tsv") {
		t.Fatal("premise: the local dev branch lost the port map in the reset")
	}
	tree, err := branchTree(work, "dev")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tree)
	if _, err := os.Stat(filepath.Join(tree, ".einsteinOS", "port", "u1.tsv")); err != nil {
		t.Fatalf("the tree of the dev branch has no port map although the branch on the remote has it: %v", err)
	}
}

// a branch that was never pushed has no remote-tracking branch: the local one is read
func TestBranchTreeReadsALocalBranchThatWasNeverPushed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	g := func(dir string, args ...string) string {
		return diagGitOut(t, dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	}
	dir := t.TempDir()
	g(dir, "init", "-q")
	g(dir, "checkout", "-q", "-b", "local-only")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644)
	g(dir, "add", ".")
	g(dir, "commit", "-qm", "c")
	tree, err := branchTree(dir, "local-only")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tree)
	if _, err := os.Stat(filepath.Join(tree, "f.txt")); err != nil {
		t.Fatal(err)
	}
}

// 11/17 10:12:54 uf7cdc0dcca and 12:05 u89be81de0b: the port maps on the branch name the
// reference's files as the conversations were shown them (include/ref/…); the recheck looked
// for include/strata/… and reopened both. It holds a unit to what the verification holds it to.
func TestTheRecheckReadsThePortMapAsTheBranchWritesIt(t *testing.T) {
	tree := t.TempDir()
	put := func(f, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(tree, f)), 0o755)
		os.WriteFile(filepath.Join(tree, f), []byte(body), 0o644)
	}
	put(PortMapPath("u89be81de0b"), "include/ref/kernels/ngram.hpp\tHEADS_PER_NGRAM\tinclude/baloon/ple_ngram_kernels.hpp\tHEADS_PER_NGRAM\n"+
		"include/ref/kernels/ngram.hpp\tNGRAM_SIZE\tinclude/baloon/ple_ngram_kernels.hpp\tNGRAM_SIZE\n")
	put("include/baloon/ple_ngram_kernels.hpp", "constexpr int HEADS_PER_NGRAM = 4;\nconstexpr int NGRAM_SIZE = 3;\n")
	unit := DevPlanNode{ID: "u89be81de0b", Sources: []string{"include/strata/kernels/ngram.hpp"}}
	syms := []RefSymbol{{File: "include/strata/kernels/ngram.hpp", Kind: "const", Name: "HEADS_PER_NGRAM"},
		{File: "include/strata/kernels/ngram.hpp", Kind: "const", Name: "NGRAM_SIZE"}}
	r := &devRun{repo: "Niko1221/Strata",
		plan: &DevPlan{PortPass: map[string]bool{unit.ID: true}, PortUnits: []DevPlanNode{unit}},
		raw:  map[string]UnitRequirements{unit.ID: {Symbols: syms}},
		reqs: map[string]UnitRequirements{unit.ID: {Symbols: syms}}}
	r.owners, r.ownersFor = map[string]string{
		"include/strata/kernels/ngram.hpp\tHEADS_PER_NGRAM": unit.ID,
		"include/strata/kernels/ngram.hpp\tNGRAM_SIZE":      unit.ID}, unit.ID
	if got := r.recheckOn(tree); len(got) != 0 {
		t.Fatalf("reopened %v: %+v", got, r.plan.Reopen[unit.ID])
	}
	// a definition truly without a line is still found
	put(PortMapPath("u89be81de0b"), "include/ref/kernels/ngram.hpp\tHEADS_PER_NGRAM\tinclude/baloon/ple_ngram_kernels.hpp\tHEADS_PER_NGRAM\n")
	if got := r.recheckOn(tree); len(got) != 1 || !strings.Contains(r.plan.Reopen[unit.ID].Text, "NGRAM_SIZE") {
		t.Fatalf("a missing line was not found: %v %+v", got, r.plan.Reopen[unit.ID])
	}
}
