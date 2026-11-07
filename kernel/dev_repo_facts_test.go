package kernel

import (
	"strings"
	"testing"
)

func withProbe(t *testing.T, fn func(url, branch string) bool) {
	old := devRepoProbe
	devRepoProbe = fn
	t.Cleanup(func() { devRepoProbe = old })
}

func TestRepoBlockStatesOnlyWhatWasMeasured(t *testing.T) {
	var gotURL, gotBranch string
	withProbe(t, func(url, branch string) bool { gotURL, gotBranch = url, branch; return true })
	f := newDevRepoFacts("zeng-bin3123/baloon", "einsteinOS/dev-abc123")
	if gotURL != "https://github.com/zeng-bin3123/baloon.git" || gotBranch != "einsteinOS/dev-abc123" {
		t.Fatalf("the probe must ask about the anonymous URL and the branch the conversation builds on: %q %q", gotURL, gotBranch)
	}
	b := f.Block()
	for _, want := range []string{
		"[DEV REPO]",
		"zeng-bin3123/baloon → /home/claude/baloon (the machine that runs your commands)",
		"in your own sandbox: git clone --branch einsteinOS/dev-abc123 https://github.com/zeng-bin3123/baloon.git /home/claude/baloon",
	} {
		if !strings.Contains(b, want) {
			t.Fatalf("block lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "@") || strings.Contains(b, "x-access-token") {
		t.Fatalf("the URL in the prompt must carry no credentials:\n%s", b)
	}

	// not readable anonymously (private, or the branch is missing): no clone line, and no claim about why
	withProbe(t, func(string, string) bool { return false })
	b = newDevRepoFacts("zeng-bin3123/baloon", "einsteinOS/dev-abc123").Block()
	if strings.Contains(b, "git clone") || strings.Contains(strings.ToLower(b), "private") {
		t.Fatalf("an unmeasured fact must leave no line and no assertion:\n%s", b)
	}
	if !strings.Contains(b, "→ /home/claude/baloon") {
		t.Fatalf("the directory is still a fact:\n%s", b)
	}
	// no repository at all: an empty block, not a made-up one
	if got := (DevRepoFacts{}).Block(); got != "[DEV REPO]\n" {
		t.Fatalf("%q", got)
	}
}

func TestEveryAttemptNamesItsOwnBranchInTheCloneCommand(t *testing.T) {
	withProbe(t, func(string, string) bool { return true })
	r := &devRun{pid: 1, repo: "o/r", devRepo: "o/d", branch: "einsteinOS/dev-main1", task: "t", gpuAll: "0"}
	plan := newDevPlan("o/r", "s", []DevModule{{Name: "A.lean", Hash: "h", Bytes: 10}}, [][]string{{"A.lean"}}, "p")
	r.plan = plan
	first := r.unitPrompt(plan.Leaves()[0], 0, 1, 1, DevFailure{}, r.branch)
	if !strings.Contains(first, "git clone --branch einsteinOS/dev-main1 ") {
		t.Fatalf("a first attempt clones the dev branch:\n%s", first)
	}
	// a fix attempt continues from the unit's wip branch, where the failing code was parked
	wip := devWipBranch(r.branch, plan.Leaves()[0].ID)
	fix := r.unitPrompt(plan.Leaves()[0], 0, 1, 2, DevFailure{Kind: "compile_fail", Text: "x", BaseRef: wip}, wip)
	if !strings.Contains(fix, "git clone --branch "+wip+" ") || strings.Contains(fix, "git clone --branch einsteinOS/dev-main1 ") {
		t.Fatalf("a fix attempt clones the branch that holds the failing code:\n%s", fix)
	}
}

// the sentences that were false in the conversation's own sandbox must not come back
func TestThePromptAssertsNothingItCannotKnow(t *testing.T) {
	for _, probe := range []bool{true, false} {
		withProbe(t, func(string, string) bool { return probe })
		r := &devRun{pid: 1, repo: "o/r", devRepo: "o/d", branch: "b", task: "t", gpuAll: "0"}
		plan := newDevPlan("o/r", "s", []DevModule{{Name: "A.lean", Hash: "h", Bytes: 10}, {Name: "B.lean", Hash: "h2", Bytes: 10}}, [][]string{{"A.lean"}, {"B.lean"}}, "p")
		plan.MarkUnitPass(plan.Leaves()[0])
		r.plan = plan
		for _, attempt := range []int{1, 2} {
			p := r.unitPrompt(plan.Leaves()[1], 1, 2, attempt, DevFailure{Kind: "review_fail", Text: "x", BaseRef: "b"}, "b")
			for _, bad := range []string{"already contains", "already holds", "already in your repo", "is in your repo", "private", "believe the local", "do not clone it"} {
				if strings.Contains(strings.ToLower(p), strings.ToLower(bad)) {
					t.Fatalf("probe=%v attempt=%d: the prompt says %q, which is not something the kernel measured", probe, attempt, bad)
				}
			}
		}
	}
}

// against the real repository: skipped when GitHub cannot be reached from here
func TestTheRealProbeAgainstTheRealRepository(t *testing.T) {
	if !devRepoProbe("https://github.com/zeng-bin3123/baloon.git", "main") {
		t.Skip("cannot read the public repository from here (no network?)")
	}
	if devRepoProbe("https://github.com/zeng-bin3123/baloon.git", "no/such/branch-at-all") {
		t.Fatal("a branch that does not exist must not read as cloneable")
	}
	if devRepoProbe("https://github.com/zeng-bin3123/this-repository-does-not-exist-xyz.git", "main") {
		t.Fatal("a repository that does not exist (or needs credentials) must not read as cloneable")
	}
}

// 2026/11/03 unit 3: the kernel had just fetched einsteinOS/dev-c880662efd over SSH, the
// anonymous HTTPS probe failed, and the prompt lost its clone line. The kernel's own fetch
// is the fact now; the probe is only asked when there is no such record.
func TestTheCloneLineRestsOnTheKernelsOwnFetch(t *testing.T) {
	old := devRepoProbe
	t.Cleanup(func() { devRepoProbe = old })
	probes := 0
	devRepoProbe = func(string, string) bool { probes++; return false } // the HTTPS link is down

	repo, branch := "zeng-bin3123/baloon", "einsteinOS/dev-c880662efd"
	if f := newDevRepoFacts(repo, branch); f.URL != "" || probes != 1 {
		t.Fatalf("no fetch, probe failed: url %q probes %d", f.URL, probes)
	}
	recordFetched(repo, []string{branch})
	f := newDevRepoFacts(repo, branch)
	if f.URL != "https://github.com/zeng-bin3123/baloon.git" || probes != 1 {
		t.Fatalf("fetched: url %q probes %d (the probe must not decide)", f.URL, probes)
	}
	if !strings.Contains(f.Block(), "git clone --branch einsteinOS/dev-c880662efd https://github.com/zeng-bin3123/baloon.git /home/claude/baloon") {
		t.Fatalf("%s", f.Block())
	}
	// another branch was not fetched: back to the probe
	if f := newDevRepoFacts(repo, "einsteinOS/other"); f.URL != "" || probes != 2 {
		t.Fatalf("other branch: %q %d", f.URL, probes)
	}
}

// gitFetchRefs records what it fetched, and not what the remote did not have
func TestGitFetchRefsRecordsTheBranchesItFetched(t *testing.T) {
	remote, clone := fetchFixture(t)
	withRemotes(t, false, "", remote)
	if _, err := gitFetchRefs(clone, "o/fetchfacts", "einsteinOS/theory", "einsteinOS/none"); err != nil {
		t.Fatal(err)
	}
	if _, ok := fetchedAt("o/fetchfacts", "einsteinOS/theory"); !ok {
		t.Fatal("fetched branch not recorded")
	}
	if _, ok := fetchedAt("o/fetchfacts", "einsteinOS/none"); ok {
		t.Fatal("missing branch recorded")
	}
}
