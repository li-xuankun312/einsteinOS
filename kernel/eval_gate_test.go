package kernel

import (
	"strings"
	"testing"
)

func evalRunOf(ms ...Metric) EvalRun { return EvalRun{Benches: []EvalBenchRun{{Metrics: ms}}} }

// Not worse than the reference on this machine, under the masked name the port uses; not
// worse than the port at the previous integration; a benchmark the port does not run fails;
// the first commit is judged against the reference alone.
func TestEvalGate(t *testing.T) {
	mask := func(s string) string { return strings.ReplaceAll(s, "strata", "ref") }
	ref := evalRunOf(Metric{"strata-bench decode", 80, "tok/s", "higher"}, Metric{"ppl", 6.10, "ppl", "lower"})
	rec := EvalRecord{RefSHA: "bbbbbbbbbbbb", Reference: ref,
		Dev: evalRunOf(Metric{"ref-bench decode", 85, "tok/s", "higher"}, Metric{"ppl", 6.10, "ppl", "lower"})}
	if k, txt := evalGate(rec, nil, mask); k != "" {
		t.Fatalf("first commit, not worse than the reference: %s %s", k, txt)
	}
	rec.Dev = evalRunOf(Metric{"ref-bench decode", 79.9, "tok/s", "higher"}, Metric{"ppl", 6.10, "ppl", "lower"})
	if k, _ := evalGate(rec, nil, mask); k != "integration_below_reference" {
		t.Fatalf("slower than the reference: %q", k)
	}
	rec.Dev = evalRunOf(Metric{"ref-bench decode", 85, "tok/s", "higher"})
	if k, txt := evalGate(rec, nil, mask); k != "integration_metric_missing" || !strings.Contains(txt, `"ppl"`) {
		t.Fatalf("no perplexity from the port: %q %s", k, txt)
	}
	prev := &EvalRecord{RefSHA: "aaaaaaaaaaaa", Dev: evalRunOf(Metric{"ref-bench decode", 90, "tok/s", "higher"}, Metric{"ppl", 6.05, "ppl", "lower"})}
	rec.Dev = evalRunOf(Metric{"ref-bench decode", 85, "tok/s", "higher"}, Metric{"ppl", 6.10, "ppl", "lower"})
	k, txt := evalGate(rec, prev, mask)
	if k != "integration_regressed" || !strings.Contains(txt, "aaaaaaaa") || strings.Count(txt, "integration_regressed") != 2 {
		t.Fatalf("worse than the previous integration on both: %q %s", k, txt)
	}
}
