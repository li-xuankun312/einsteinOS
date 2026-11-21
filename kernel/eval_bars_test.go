package kernel

import (
	"strings"
	"testing"
)

// The commit is complete when the measured end-to-end rate is above google's, anthropic's,
// zhipu's and deepseek's tok/s, given as EINSTEINOS_TOKS_BARS; a bar not given cannot be
// compared with, and a commit is not complete on it.
func TestTheRateMustBeAboveTheBars(t *testing.T) {
	br := EvalBenchRun{Bench: EvalBench{File: "e2e.sh"}, Metrics: []Metric{{e2eMeasuredLabel, 120, "tok/s", "higher"}}}
	t.Setenv("EINSTEINOS_TOKS_BARS", "google=100, anthropic=80,deepseek=60")
	if k, txt, _ := integrationBarsGate(br); k != "integration_bars_unmeasured" || !strings.Contains(txt, "no tok/s is given for zhipu") {
		t.Fatalf("a missing bar passed: %q %s", k, txt)
	}
	t.Setenv("EINSTEINOS_TOKS_BARS", "google=100,anthropic=130,zhipu=90,deepseek=60")
	k, txt, done := integrationBarsGate(br)
	if k != "integration_below_bars" || !strings.Contains(txt, "anthropic 130 tok/s") || strings.Contains(txt, "google") || len(done) != 4 {
		t.Fatalf("below one bar: %q %s %d", k, txt, len(done))
	}
	t.Setenv("EINSTEINOS_TOKS_BARS", "google=100,anthropic=110,zhipu=90,deepseek=60")
	if k, txt, done := integrationBarsGate(br); k != "" || len(done) != 4 {
		t.Fatalf("above every bar: %q %s", k, txt)
	}
	if k, _, _ := integrationBarsGate(EvalBenchRun{RateWhy: e2eRateNoLoaderWhy}); k != "integration_e2e_unmeasured" {
		t.Fatalf("no measured rate: %q", k)
	}
}
