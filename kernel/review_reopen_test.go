package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A passed unit a later review faulted runs again: its pass no longer holds, its failure is
// the reviewer's own words, and taking it clears it. A second verdict against the same unit
// keeps the first.
func TestAFaultedUnitIsReopened(t *testing.T) {
	n := DevPlanNode{ID: "u41e2bf114f", Sources: []string{"src/prefill/kernels.cu"}}
	p := &DevPlan{PortPass: map[string]bool{"u41e2bf114f": true}}
	if !p.UnitPass(n) {
		t.Fatal("it passed")
	}
	f := DevFailure{Kind: "review_fail", Text: "kv_append writes f32 scales, theory says f16 (reviewing pf7b804cf95)"}
	p.MarkReopen(n.ID, f)
	p.MarkReopen(n.ID, DevFailure{Kind: "review_fail", Text: "something else"})
	if p.UnitPass(n) || !p.Reopened(n.ID) {
		t.Fatal("a reopened unit has not passed")
	}
	got, ok := p.TakeReopen(n.ID)
	if !ok || got.Text != f.Text {
		t.Fatalf("the first verdict is kept: %+v", got)
	}
	if p.Reopened(n.ID) || !p.UnitPass(n) {
		t.Fatal("taken, it passes again until it runs")
	}
}

// A verdict naming only another unit's files passes the unit under review and reopens that
// other unit with the reviewer's words; a verdict naming one of its own files stays its own.
func TestAVerdictAboutAnotherUnitsFilesDoesNotFailThisUnit(t *testing.T) {
	written := map[string][]string{"src/prefill/kernels.cu": {"u41e2bf114f"}}
	sources := map[string][]string{"pf7b804cf95": {"include/strata/prefill/kernels.hpp"}}

	other := "FAIL: kv_append (src/prefill/kernels.cu) writes f32 scales but the theory says f16"
	blames := reviewBlames("pf7b804cf95", reviewOwners(other, written, sources))
	if len(blames) != 1 || blames[0] != "u41e2bf114f" {
		t.Fatalf("blames: %v", blames)
	}
	p := &DevPlan{PortPass: map[string]bool{"u41e2bf114f": true}}
	reason := "kv_append writes f32 scales"
	p.MarkReopen(blames[0], DevFailure{Kind: "review_fail", Text: fmtReopen(reason, "pf7b804cf95")})
	f, _ := p.TakeReopen("u41e2bf114f")
	if !strings.Contains(f.Text, reason) || !strings.Contains(f.Text, "pf7b804cf95") {
		t.Fatalf("the reopened unit's failure: %q", f.Text)
	}

	own := "FAIL: include/strata/prefill/kernels.hpp declares the wrong geometry, and src/prefill/kernels.cu follows it"
	if b := reviewBlames("pf7b804cf95", reviewOwners(own, written, sources)); len(b) == 0 {
		t.Fatal("a verdict naming its own file as well still names the other unit")
	}
}

func fmtReopen(reason, unit string) string {
	return strings.ReplaceAll(strings.ReplaceAll(reopenReasonFmt, "%[1]s", reason), "%[2]s", unit)
}

// A reopened unit runs after everything already queued, and its reopen becomes its failure
// when it does.
func TestReopenedUnitsRunLast(t *testing.T) {
	leaves := []DevPlanNode{
		{ID: "u8", Sources: []string{"a.cu"}},
		{ID: "u9", Sources: []string{"b.cu"}},
		{ID: "u20", Sources: []string{"c.hpp"}},
		{ID: "u21", Sources: []string{"d.hpp"}},
	}
	p := &DevPlan{PortPass: map[string]bool{"u8": true, "u9": true, "u20": true}}
	if got := nextUnitIndex(p, leaves); got != 3 {
		t.Fatalf("the queued unit first: %d", got)
	}
	p.MarkReopen("u8", DevFailure{Kind: "review_fail", Text: "kv_append"})
	p.MarkReopen("u9", DevFailure{Kind: "review_fail", Text: "gr_norm"})
	if got := nextUnitIndex(p, leaves); got != 3 {
		t.Fatalf("still the queued unit: %d", got)
	}
	p.PortPass["u21"] = true
	if got := nextUnitIndex(p, leaves); got != 0 {
		t.Fatalf("then the reopened ones, in plan order: %d", got)
	}
	p.TakeReopen("u8")
	if got := nextUnitIndex(p, leaves); got != 1 {
		t.Fatalf("the next reopened one: %d", got)
	}
	p.TakeReopen("u9")
	if got := nextUnitIndex(p, leaves); got != -1 {
		t.Fatalf("nothing left: %d", got)
	}
}

// Taking a reopen makes it the unit's failure, so its next conversation is a fix conversation
// that is given the reviewer's words.
func TestAReopenBecomesTheUnitsFailure(t *testing.T) {
	p := &DevPlan{PortPass: map[string]bool{"u9": true}, LastFailure: map[string]DevFailure{}, FixAttempts: map[string]int{}}
	f := DevFailure{Kind: "review_fail", Text: "gr_norm normalizes jointly over all 4 streams", BaseRef: "einsteinOS/dev-x"}
	p.MarkReopen("u9", f)
	got, ok := p.TakeReopen("u9")
	if !ok || got.Text != f.Text || got.BaseRef != f.BaseRef {
		t.Fatalf("%+v", got)
	}
	p.LastFailure["u9"] = got
	p.FixAttempts["u9"] = 1
	if p.LastFailure["u9"].Text != f.Text || p.FixAttempts["u9"]+1 != 2 {
		t.Fatal("its next conversation is a fix conversation with that verdict")
	}
}

// A unit is reopened at most devMaxReopens times, so two units whose reviews fault each
// other's files cannot reopen each other without end.
func TestReopeningIsBounded(t *testing.T) {
	p := &DevPlan{}
	for i := 0; i < devMaxReopens; i++ {
		if got := p.ReopenableUnits([]string{"u8", "u9"}); len(got) != 2 {
			t.Fatalf("round %d: %v", i, got)
		}
		p.MarkReopen("u8", DevFailure{Text: "x"})
		p.MarkReopen("u9", DevFailure{Text: "y"})
		p.TakeReopen("u8")
		p.TakeReopen("u9")
	}
	if got := p.ReopenableUnits([]string{"u8", "u9"}); len(got) != 0 {
		t.Fatalf("past the limit: %v", got)
	}
}

// A unit that comes back from the plan with review failures about other units' files has
// those units reopened and its own attempts cleared, instead of being asked to split. The
// stored failure is the one the plan on zeng-bin3123/baloon einsteinOS/state held for
// pf7b804cf95 at 837f437 (11/08 08:34), word for word — it names no file — and the port
// maps and files are on the dev branch of a clone whose working tree is on another branch,
// as the kernel's clone was on 11/08.
func TestStoredReviewFailuresAreAttributedOnResume(t *testing.T) {
	const branch = "einsteinOS/dev-3b59928b8f"
	dir := t.TempDir()
	git := func(a ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, a...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", a, out)
		}
	}
	write := func(f, text string) {
		p := filepath.Join(dir, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(text), 0o644)
	}
	git("init", "-q", "-b", "main")
	write("README.md", "baloon\n")
	git("add", "-A")
	git("commit", "-qm", "main")
	git("checkout", "-qb", branch)
	write(PortMapPath("pf7b804cf95"), "include/ref/prefill/kernels.hpp\tgr_norm\tinclude/baloon/prefill_kernels_api.hpp\tgr_norm\n")
	write(PortMapPath("u41e2bf114f"), "src/prefill/kernels.cu\tgr_norm\tsrc/prefill/kernels.cu\tgr_norm\n")
	write(PortMapPath("u4decb4e657"), "src/kernels/cuda/gr.cu\tgr_norm_kernel\tsrc/kernels/gpu/gr_core.cu\tgr_norm_kernel\n")
	write("include/baloon/prefill_kernels_api.hpp", "int gr_norm(const float* x, float* out, int T, float eps);                                  // rms over the 4 streams of a token\n")
	write("src/prefill/kernels.cu", "int gr_norm(const float* x, float* out, int T, float eps) {\n    if (!x || !out || T <= 0) return -1;\n    gr_norm_kernel<<<(unsigned)T, 256>>>(x, out, eps);\n}\n")
	write("src/kernels/gpu/gr_core.cu", "__global__ void gr_norm_kernel(const float* x, float* out, float eps) {\n}\n")
	git("add", "-A")
	git("commit", "-qm", "units")
	git("checkout", "-q", "main")

	stored := "gr_norm normalizes jointly over all 4 streams (D=10240) but theory/decode reference norm each stream row (N=2560) separately, and the unit test cannot detect it"
	p := &DevPlan{
		PortPass:    map[string]bool{"u41e2bf114f": true, "u4decb4e657": true},
		FixAttempts: map[string]int{"pf7b804cf95": 3},
		LastFailure: map[string]DevFailure{"pf7b804cf95": {Kind: "review_fail", Text: stored, BaseRef: branch}},
	}
	unit := DevPlanNode{ID: "pf7b804cf95", Sources: []string{"include/ref/prefill/kernels.hpp"}}
	if need, _ := dev_node_needs_split(p, unit); !need {
		t.Fatal("as it stands the plan asks to split it")
	}
	blame := func(u DevPlanNode, verdict string) []string {
		return blameOnBranch(dir, branch, u.ID, verdict, map[string][]string{u.ID: u.Sources}, nil)
	}
	blames := resumeStoredReviewFailure(p, unit, branch, blame)
	if len(blames) != 1 || blames[0] != "u41e2bf114f" {
		t.Fatalf("blames: %v", blames)
	}
	if need, why := dev_node_needs_split(p, unit); need {
		t.Fatalf("it starts again instead of splitting: %s", why)
	}
	if !p.Reopened("u41e2bf114f") || p.UnitPass(DevPlanNode{ID: "u41e2bf114f", Sources: []string{"x.cu"}}) {
		t.Fatal("the unit whose definition it is runs again")
	}
}

// A unit that used its fix attempts and has nothing to split is deferred: the run goes on to
// the units behind it, the unit is recorded with what the review said, and its files are left
// to the integration unit.
func TestAnUnsplittableExhaustedUnitIsDeferred(t *testing.T) {
	leaves := []DevPlanNode{{ID: "pf7b804cf95", Sources: []string{"a.hpp"}}, {ID: "p4fde6588a6", Sources: []string{"b.hpp"}}}
	p := &DevPlan{
		FixAttempts: map[string]int{"pf7b804cf95": 3},
		LastFailure: map[string]DevFailure{"pf7b804cf95": {Kind: "review_fail", Text: "gr_norm normalizes jointly over all 4 streams"}},
	}
	if nextUnitIndex(p, leaves) != 0 {
		t.Fatal("it is the unit in front")
	}
	if need, _ := dev_node_needs_split(p, leaves[0]); !need {
		t.Fatal("the plan asks to split it")
	}
	p.MarkDeferred("pf7b804cf95", p.LastFailure["pf7b804cf95"])
	if got := nextUnitIndex(p, leaves); got != 1 {
		t.Fatalf("the run goes on to the next unit: %d", got)
	}
	if ids := p.DeferredUnits(); len(ids) != 1 || ids[0] != "pf7b804cf95" {
		t.Fatalf("deferred: %v", ids)
	}
	if p.Deferred["pf7b804cf95"].Text == "" {
		t.Fatal("what the review said is kept")
	}
}

// A unit deferred on a review failure is deferred with the fact that the attribution was
// measured and found no other unit; a deferral on any other failure claims nothing of the kind.
func TestADeferredReviewFailureSaysTheAttributionFoundNobody(t *testing.T) {
	p := &DevPlan{}
	p.MarkDeferred("pf7b804cf95", deferredFailure(DevFailure{Kind: "review_fail", Text: "gr_norm normalizes jointly over all 4 streams"}))
	if got := p.Deferred["pf7b804cf95"].Attribution; got != deferredAttribution {
		t.Fatalf("attribution: %q", got)
	}
	p.MarkDeferred("u1", deferredFailure(DevFailure{Kind: "compile_fail", Text: "error"}))
	if got := p.Deferred["u1"].Attribution; got != "" {
		t.Fatalf("a compile failure was never attributed: %q", got)
	}
}

// 11/10 14:36: unit p81030368e8's review said kv_append_q8_step in src/ilu_kv_q8.cu quantizes
// with the unrounded 127/amax. The dev branch's port maps gave that file only to units of
// earlier plans; with the plan's units as the only owners nobody else is blamed, and the
// verdict is the reviewed unit's own failure.
func TestABlameOnUnitsOutsideThePlanIsNoBlame(t *testing.T) {
	const branch = "einsteinOS/dev-3089b9ac95"
	dir := t.TempDir()
	git := func(a ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, a...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", a, out)
		}
	}
	write := func(f, text string) {
		p := filepath.Join(dir, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(text), 0o644)
	}
	git("init", "-q", "-b", branch)
	write(PortMapPath("u02c65ed515"), "src/kernels/cuda/kv_q8.cu\tkv_append_q8\tsrc/ilu_kv_q8.cu\tkv_append_q8_step\n")
	write(PortMapPath("u0063babba8"), "src/kernels/cuda/kv_q8.cu\tkv_append_q8_kernel\tsrc/kernels/cuda/kv_q8.cu\tkv_append_q8_kernel\n")
	write(PortMapPath("p81030368e8"), "include/strata/kernels/kv_q8.hpp\tkv_append_q8\tinclude/baloon/kv_q8_kernels.hpp\tkv_append_q8_step\n")
	write("src/ilu_kv_q8.cu", "cudaError_t kv_append_q8_step(const float* x, int8_t* q, int n) {\n    const float inv = 127.f / amax;\n}\n")
	write("include/baloon/kv_q8_kernels.hpp", "cudaError_t kv_append_q8_step(const float* x, int8_t* q, int n);\n")
	git("add", "-A")
	git("commit", "-qm", "units")

	verdict := "kv_append_q8_step (kv_q8_kernels.hpp, implemented in src/ilu_kv_q8.cu) quantizes with unrounded 127/amax instead of the stored fp16 scale the kv_cache_q8 theory requires, and this unit's test never exercises it."
	if got := blameOnBranch(dir, branch, "p81030368e8", verdict, nil, nil); len(got) != 1 || got[0] != "u02c65ed515" {
		t.Fatalf("with every port map an owner the unit of the earlier plan is blamed: %v", got)
	}
	plan := map[string]bool{"p81030368e8": true, "u0063babba8": true}
	if got := blameOnBranch(dir, branch, "p81030368e8", verdict, nil, plan); len(got) != 0 {
		t.Fatalf("no unit of this plan owns src/ilu_kv_q8.cu: the verdict is p81030368e8's own, got %v", got)
	}
}
