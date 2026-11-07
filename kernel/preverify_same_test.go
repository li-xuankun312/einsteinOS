package kernel

import "testing"

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
