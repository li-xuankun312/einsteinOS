package kernel

import "testing"

// 11/03: commit 0 of 2438 completed ("2437 remaining") and nothing came next.
func TestTheCursorMovesOnlyAfterACompletePipeline(t *testing.T) {
	const pid = 77001
	if advanceAfterPipeline(pid, "Niko1221", "Strata") {
		t.Fatal("nothing bound: nothing to advance from")
	}
	recordRefBound(pid, "Niko1221/Strata", boundRef{Idx: 0, SHA: "01529aaa82b9f5c5", Total: 2438})
	before, _ := PRStepCount(pid)
	if !advanceAfterPipeline(pid, "Niko1221", "Strata") {
		t.Fatal("2437 commits remain")
	}
	prStepsMu.Lock()
	steps := append([]*Step{}, prSteps[pid][before:]...)
	prStepsMu.Unlock()
	if len(steps) != 2 || steps[0].Kind != STEP_REF_BIND || steps[0].Command != "start=1" || steps[1].Kind != STEP_DEV_FROM_THEOREMS {
		t.Fatalf("queued %+v", steps)
	}
	if got := ref_bind_cursor(pid, "Niko1221", "Strata"); got != 1 {
		t.Fatalf("cursor %d", got)
	}
	// the last commit: nothing more
	recordRefBound(pid, "Niko1221/Strata", boundRef{Idx: 2437, SHA: "ffff", Total: 2438})
	if advanceAfterPipeline(pid, "Niko1221", "Strata") {
		t.Fatal("past the last commit")
	}
}

// commit 1's dev branch starts from commit 0's completed branch, not from main (where
// commit 0's verified code is not: its PR is opened, not merged)
func TestTheNextCommitsDevBranchStartsFromTheLastCompletedOne(t *testing.T) {
	_, clone := fetchFixture(t) // remote: main + einsteinOS/theory
	gitT(t, clone, "checkout", "-q", "-b", "einsteinOS/dev-commit0")
	write(t, clone, "include/baloon/f16_conversion.hpp", "// verified in commit 0's pipeline\n")
	gitT(t, clone, "add", "-A")
	gitT(t, clone, "commit", "-q", "-m", "unit 1 verified")
	gitT(t, clone, "push", "-q", "origin", "einsteinOS/dev-commit0")
	gitT(t, clone, "checkout", "-q", "main")

	const devRepo = "o/next-base"
	if devNewBranchBase(devRepo) != "main" {
		t.Fatal("first pipeline: main")
	}
	recordCompletedBranch(devRepo, "einsteinOS/dev-commit0")
	if err := gitUseBranchFromRemote(clone, "einsteinOS/dev-commit1", devNewBranchBase(devRepo)); err != nil {
		t.Fatal(err)
	}
	if cur := gitT(t, clone, "branch", "--show-current"); cur != "einsteinOS/dev-commit1" {
		t.Fatalf("on %q", cur)
	}
	if out := gitT(t, clone, "show", "HEAD:include/baloon/f16_conversion.hpp"); out != "// verified in commit 0's pipeline" {
		t.Fatalf("commit 0's code missing from commit 1's branch: %q", out)
	}
	// other dev repos are not affected
	if devNewBranchBase("o/other") != "main" {
		t.Fatal("base leaked across dev repos")
	}
}
