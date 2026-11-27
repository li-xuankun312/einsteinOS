package kernel

import (
	"strings"
	"testing"
)

// 11/26 19:55:15 the smoke_fail text kept the last 3000 bytes of test.sh's output, sections that
// all passed, and the test that failed was in none of it
func TestTheLinesOfAFailedTestAreGivenFirst(t *testing.T) {
	out := "== elementwise_parity: elementwise_parity --selftest\n  rms max_abs_err=0.31\nelementwise_parity: checks=12 failures=2\nelementwise_parity FAILED\n" +
		"== pool_test: pool_test --selftest\nchecks: 3106, failures: 0\npool_test: PASSED\n" + strings.Repeat("== x: x\nx PASSED\n", 400)
	got := smokeFailLines(out, 60)
	want := smokeFailHead + "== elementwise_parity: elementwise_parity --selftest\n  elementwise_parity: checks=12 failures=2\n  elementwise_parity FAILED\n" + smokeFailEnd
	if got != want {
		t.Fatalf("got\n%s", got)
	}
	if smokeFailLines("== a: a\nchecks=3 failures=0\na PASSED\n", 60) != "" {
		t.Fatal("a passing output gave lines")
	}
}

// 11/26 20:42:55 to 21:39:21 six verifications whose test.sh exited 1 were sent back as gate=ok
func TestAVerificationWhoseTestFailedJudgedNoGate(t *testing.T) {
	for _, c := range []struct {
		r    VerifyResult
		want string
	}{
		{VerifyResult{Compile: "pass", Smoke: "fail"}, "not judged"},
		{VerifyResult{Compile: "fail", Smoke: "skipped"}, "not judged"},
		{VerifyResult{Compile: "pass", Smoke: "pass"}, "ok"},
		{VerifyResult{Compile: "pass", Smoke: "pass", Gate: "kernel_unported"}, "kernel_unported"},
	} {
		if got := gateOf(c.r); got != c.want {
			t.Fatalf("%+v: %q", c.r, got)
		}
	}
}
