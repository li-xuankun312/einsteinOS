package kernel

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShapeOutputKeepsHeadAndTailAndSaysWhatWasCut(t *testing.T) {
	small := "tiny output"
	if got := ShapeOutput(small, 100); got != small {
		t.Fatalf("a result that fits must be untouched, got %q", got)
	}
	var b strings.Builder
	b.WriteString("FIRST LINE\n")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "filler line %d\n", i)
	}
	b.WriteString("error: undefined symbol ix_frobnicate\nLAST LINE\n")
	long := b.String()

	got := ShapeOutput(long, 4000)
	if !strings.Contains(got, "FIRST LINE") {
		t.Error("the head is gone")
	}
	if !strings.Contains(got, "error: undefined symbol ix_frobnicate") || !strings.Contains(got, "LAST LINE") {
		t.Error("the TAIL is gone: a build's errors are printed last")
	}
	if !WasCut(got) || !strings.Contains(got, fmt.Sprintf("of %d shown", len(long))) {
		t.Errorf("the cut must be announced with the total size: %q", got[len(got)/2-80:len(got)/2+120])
	}
	if len(got) > 4000+200 {
		t.Errorf("result %d bytes, limit 4000 + marker", len(got))
	}
	if WasCut(small) || WasCut(long) {
		t.Error("WasCut must only fire on shaped output")
	}
}

func TestShapeOutputCutsOnRuneBoundaries(t *testing.T) {
	s := strings.Repeat("证明", 5000) // 3 bytes per rune
	for _, limit := range []int{1000, 1001, 1002, 4999} {
		got := ShapeOutput(s, limit)
		if !utf8.ValidString(got) {
			t.Fatalf("limit %d produced invalid UTF-8", limit)
		}
	}
}

// the 2026/11/02 case: a loop over seven files printed seven verdicts and the old cut kept only the first.
func TestALoopOverFilesStillShowsTheLastVerdict(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&b, "== File%d.lean\n%s\nverdict-of-file-%d\n", i, strings.Repeat("x", 2900), i)
	}
	out := b.String() // about 20 KB
	old := out[:2000] + "\n..."
	if strings.Contains(old, "verdict-of-file-7") {
		t.Fatal("test premise: the old cut cannot show the last verdict")
	}
	got := ShapeOutput(out, defaultLocalResultCap)
	for _, want := range []string{"== File1.lean", "verdict-of-file-7"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lost %q", want)
		}
	}
	if !WasCut(got) {
		t.Fatal("a 20 KB output under a 16 KB cap must be cut, and say so")
	}
}

func TestShapeRoundSharesOneBudget(t *testing.T) {
	small1, small2 := "ok", "exit 0"
	big := strings.Repeat("0123456789", 3000) // 30 KB
	big2 := strings.Repeat("abcdefghij", 3000)
	out := ShapeRound([]string{small1, big, small2, big2}, 20000, 20000)
	if out[0] != small1 || out[2] != small2 {
		t.Fatal("results below their fair share must be left alone")
	}
	total := 0
	for _, o := range out {
		total += len(o)
	}
	if total > 20000+2*200 {
		t.Fatalf("round total %d exceeds the 20000 budget (plus two markers)", total)
	}
	for _, i := range []int{1, 3} {
		if !WasCut(out[i]) {
			t.Errorf("result %d should have been cut and say so", i)
		}
	}
	// within budget: untouched
	same := ShapeRound([]string{"a", "b"}, 100, 1000)
	if same[0] != "a" || same[1] != "b" {
		t.Fatal("under budget must be unchanged")
	}
}

func TestLocalCapsCanBeOverridden(t *testing.T) {
	t.Setenv("EINSTEIN_LOCAL_RESULT_CAP", "5000")
	t.Setenv("EINSTEIN_LOCAL_ROUND_BUDGET", "9000")
	if LocalResultCap() != 5000 || LocalRoundBudget() != 9000 {
		t.Fatal("env override ignored")
	}
	t.Setenv("EINSTEIN_LOCAL_RESULT_CAP", "garbage")
	if LocalResultCap() != defaultLocalResultCap {
		t.Fatal("a bad value must fall back to the default")
	}
}
