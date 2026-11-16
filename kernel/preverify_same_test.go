package kernel

import (
	"fmt"
	"testing"
)

// A done line on the files of the last rejected verification gets that result back; changed
// files, or a passing last result, are measured again.
func TestARejectedVerificationIsNotRerunOnTheSameFiles(t *testing.T) {
	var none *preVerifyResult
	if none.sameRejected("d1") {
		t.Fatal("nothing verified yet")
	}
	p := &preVerifyResult{ok: false, digest: "d1"}
	if !p.sameRejected("d1") || p.sameRejected("d2") {
		t.Fatal("same files reuse, changed files re-measure")
	}
	if (&preVerifyResult{ok: true, digest: "d1"}).sameRejected("d1") {
		t.Fatal("a pass is not a rejection")
	}
}

// A conversation that writes its done line on an unchanged rejected tree three times in a row
// is let go, and the kernel judges the tree. 11/15 17:47–19:53 dev_unit_33 did it 13 times
// over two hours (smoke_fail), each time getting the same report back, until MaxRounds(20).
func TestAConversationThatChangesNothingIsLetGo(t *testing.T) {
	p := &preVerifyResult{digest: "d1"}
	for n, want := range map[int]bool{1: false, 2: false, 3: true, 4: true, 13: true} {
		if p.unchangedEnd(n) != want {
			t.Errorf("%d: %v", n, p.unchangedEnd(n))
		}
	}
	for n, want := range map[string]string{"1": "st", "2": "nd", "3": "rd", "4": "th", "11": "th", "12": "th", "13": "th", "21": "st", "102": "nd"} {
		var i int
		fmt.Sscan(n, &i)
		if ordinalSuffix(i) != want {
			t.Errorf("%s: %s", n, ordinalSuffix(i))
		}
	}
}
