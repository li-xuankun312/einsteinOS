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
