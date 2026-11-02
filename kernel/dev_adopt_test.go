package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyBranch pushes a pid-named dev branch whose plan.json is p.
func legacyBranch(t *testing.T, bare, name string, p *DevPlan) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "w")
	twinGitRun(t, filepath.Dir(work), "clone", "-q", bare, work)
	twinGitRun(t, work, "checkout", "-q", "-b", "tmp", "origin/main")
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(work, devPlanFile, string(b))
	twinGitRun(t, work, "add", "-A")
	twinGitRun(t, work, "commit", "-q", "-m", "legacy plan")
	twinGitRun(t, work, "push", "-q", "origin", "tmp:refs/heads/"+name)
}

func planWith(repo, sha string, passed int) *DevPlan {
	p := newDevPlan(repo, sha, mods("A", "B", "C"), [][]string{{"A"}, {"B"}, {"C"}}, "plan")
	for i, l := range p.Leaves() {
		if i < passed {
			p.MarkUnitPass(l)
		}
	}
	return p
}

func TestAdoptTakesTheLegacyBranchWithTheMostPassedUnitsForTheSameCommit(t *testing.T) {
	env := newDevEnv(t, theory2("Alpha"))
	const stable = "einsteinOS/dev-stable0001"
	legacyBranch(t, env.bare, "einsteinOS/dev-3", planWith("ref/proj", "OTHERSHA", 3)) // another commit: not ours
	legacyBranch(t, env.bare, "einsteinOS/dev-4", planWith("ref/proj", "refsha1", 0))  // ours, nothing passed: nothing to continue
	legacyBranch(t, env.bare, "einsteinOS/dev-5", planWith("ref/proj", "refsha1", 1))
	legacyBranch(t, env.bare, "einsteinOS/dev-6", planWith("ref/proj", "refsha1", 2))   // the one to take
	legacyBranch(t, env.bare, "einsteinOS/dev-7", planWith("other/repo", "refsha1", 3)) // another reference repo

	got := adoptLegacyDevBranch(env.dev, "ref/proj", "refsha1", stable)
	if got != "einsteinOS/dev-6" {
		t.Fatalf("adopted %q, want dev-6 (2 units passed, same repo and commit)", got)
	}
	if !remoteHasBranch(env.bare, stable) {
		t.Fatal("the stable branch was not published")
	}
	if plan := bareShow(env.bare, stable+":"+devPlanFile); !strings.Contains(plan, `"pass": true`) {
		t.Fatalf("the stable branch must carry the legacy plan with its passed units:\n%.300s", plan)
	}
	// the legacy branches are left alone
	if !remoteHasBranch(env.bare, "einsteinOS/dev-6") || !remoteHasBranch(env.bare, "einsteinOS/dev-5") {
		t.Fatal("adoption must not delete anything")
	}
	// and now that the stable branch exists, there is nothing to adopt
	if again := adoptLegacyDevBranch(env.dev, "ref/proj", "refsha1", stable); again != "" {
		t.Fatalf("second call adopted %q", again)
	}
}

func TestAdoptDoesNothingWithoutACommitOrWithoutAMatch(t *testing.T) {
	env := newDevEnv(t, theory2("Alpha"))
	legacyBranch(t, env.bare, "einsteinOS/dev-8", planWith("ref/proj", "somethingelse", 2))
	if got := adoptLegacyDevBranch(env.dev, "ref/proj", "refsha1", "einsteinOS/dev-x"); got != "" {
		t.Fatalf("a different commit must not be adopted: %q", got)
	}
	if got := adoptLegacyDevBranch(env.dev, "ref/proj", "", "einsteinOS/dev-x"); got != "" {
		t.Fatalf("without a commit nothing can match: %q", got)
	}
	if remoteHasBranch(env.bare, "einsteinOS/dev-x") {
		t.Fatal("nothing should have been published")
	}
	_ = os.Stdout
}

// the exact situation after the 2026/11/02 run: units passed on einsteinOS/dev-<pid>, the process restarts on new code
func TestARestartOnNewCodeContinuesFromAPidNamedBranch(t *testing.T) {
	env := newDevEnv(t, theory2("Alpha", "Beta"))
	env.model.unitWork = func(prompt, unit string, n int) {
		mustWrite(env.mirror, "src/"+unit+".cu", "// "+unit+"\n")
		env.model.writeScripts(unit, 0, 0, "built")
	}
	step := &Step{Owner: "ref", Repo: "proj"}
	if _, err := dev_from_theorems(9601, step); err != nil {
		t.Fatal(err)
	}
	stable := devBranchFor("ref/proj", "refsha1", 9601)
	// make it look like the old code produced it: the work is on dev-6, the stable name does not exist
	twinGitRun(t, filepath.Dir(env.bare), "--git-dir", env.bare, "branch", "-m", stable, "einsteinOS/dev-6")
	if remoteHasBranch(env.bare, stable) {
		t.Fatal("test setup: the stable branch should be gone")
	}
	before := len(env.model.prompts)

	devPlanMu.Lock()
	devPlanMem = map[string]*DevPlan{}
	devPlanMu.Unlock()
	if _, err := dev_from_theorems(9602, step); err != nil {
		t.Fatal(err)
	}
	if len(env.model.prompts) != before {
		t.Fatalf("the restarted process redid the finished work (%d new conversations)", len(env.model.prompts)-before)
	}
	if !remoteHasBranch(env.bare, stable) {
		t.Fatal("the work was adopted under the stable name")
	}
}
