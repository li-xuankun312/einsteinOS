package kernel

import (
	"strings"
	"testing"
)

// 11/21 06:04:48-06:26:42 a restarted run built the reference again for 21m54s, for 0
// benchmarks. The reference at a commit an earlier run measured is read from that commit's
// record on the state branch; a record of another commit, or of other benchmarks, is not used.
func TestTheReferenceAnEarlierRunMeasuredIsReadFromTheStateBranch(t *testing.T) {
	dir := t.TempDir()
	repo, sha := "Niko1221/Strata", "01529aaa82b9aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ref := EvalRun{Repo: repo, SHA: sha, BuildExit: 0, BuildTail: "[100%] Built target generate", Err: "no benchmark shipped"}
	if err := writeEvalRecord(dir, EvalRecord{Repo: repo, RefSHA: sha, Reference: ref, Gate: "integration_nothing_compared"}); err != nil {
		t.Fatal(err)
	}
	got := storedReference(dir, repo, sha, nil)
	if got == nil || got.BuildTail != ref.BuildTail || got.Err != ref.Err {
		t.Fatalf("the stored reference was not read: %+v", got)
	}
	if storedReference(dir, repo, "ffffffffffffffffffffffffffffffffffffffff", nil) != nil {
		t.Fatal("a record of another commit was used")
	}
	if storedReference(dir, repo, sha, []EvalBench{{Name: "native_mmvq_multi"}}) != nil {
		t.Fatal("a record of other benchmarks was used")
	}

	// given it, the evaluation does not build the reference (a build here would fail: the
	// reference has no clone) and keeps what was stored
	resetEvalSearch()
	oa := evalAskFn
	defer func() { evalAskFn = oa }()
	evalAskFn = func(string) (string, error) { return "", nil }
	b := evalBaseFor(repo, sha, nil, got)
	if b.reference.BuildTail != ref.BuildTail || b.reference.BuildExit != 0 {
		t.Fatalf("the evaluation built the reference again: %+v", b.reference)
	}
	if strings.Contains(b.reference.Err, "clone") {
		t.Fatalf("it tried the reference's clone: %q", b.reference.Err)
	}
}
