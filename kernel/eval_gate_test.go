package kernel

import (
	"encoding/json"
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

// The end-to-end rates are set against the previous integration's. 11/10 23:33 commit
// e5509707's run (prefill 57.50, decode 11.19 tok/s) passed without being compared with
// 6801c77f's (57.54, 11.20): only the port's benchmarks were, and it had none.
func TestTheEndToEndRatesAreComparedWithThePreviousIntegration(t *testing.T) {
	id := func(s string) string { return s }
	e2e := func(prefill, decode float64) EvalBenchRun {
		return EvalBenchRun{Metrics: []Metric{{"prefill", prefill, "tok/s", "higher"}, {"decode", decode, "tok/s", "higher"}}}
	}
	prev := &EvalRecord{RefSHA: "6801c77ff6f7a1e0", E2E: e2e(57.54, 11.20)}
	rec := EvalRecord{RefSHA: "e5509707730d31f9", E2E: e2e(57.50, 11.19)}
	k, txt := evalGate(rec, prev, id)
	if k != "integration_regressed" || strings.Count(txt, "integration_regressed") != 2 || !strings.Contains(txt, "6801c77f") {
		t.Fatalf("11/10's run against 11/09's: %q %s", k, txt)
	}
	rec.E2E = EvalBenchRun{Metrics: []Metric{{"prefill", 60, "tok/s", "higher"}}}
	if k, txt := evalGate(rec, prev, id); k != "integration_metric_missing" || !strings.Contains(txt, "end-to-end") {
		t.Fatalf("no decode rate: %q %s", k, txt)
	}
	rec.E2E = e2e(60, 12)
	if k, txt := evalGate(rec, prev, id); k != "" {
		t.Fatalf("faster on both: %q %s", k, txt)
	}
}

// Every comparison the verdict made is kept in the record. 11/09 and 11/10 both records said
// "compared": null, and what the verdict had been set against could not be read from them.
func TestTheRecordKeepsEveryComparison(t *testing.T) {
	id := func(s string) string { return s }
	rec := EvalRecord{RefSHA: "e5509707730d31f9", Reference: evalRunOf(Metric{"decode", 50, "tok/s", "higher"}),
		Dev: evalRunOf(Metric{"decode", 55, "tok/s", "higher"}),
		E2E: EvalBenchRun{Metrics: []Metric{{"prefill", 57.50, "tok/s", "higher"}, {"decode", 11.19, "tok/s", "higher"}}}}
	prev := &EvalRecord{RefSHA: "6801c77ff6f7a1e0", E2E: EvalBenchRun{Metrics: []Metric{{"prefill", 57.54, "tok/s", "higher"}, {"decode", 11.20, "tok/s", "higher"}}}}
	k, _, done := evalGateCompared(rec, prev, id)
	if k != "integration_regressed" || len(done) != 3 {
		t.Fatalf("the reference's decode and the previous run's two rates: %q %+v", k, done)
	}
	if done[0].Source != evalSourceReference || done[0].Other.Value != 50 || done[0].Worse {
		t.Fatalf("against the reference: %+v", done[0])
	}
	if !strings.Contains(done[1].Source, "6801c77f") || done[1].Other.Value != 57.54 || !done[1].Worse {
		t.Fatalf("against the previous run: %+v", done[1])
	}
	b, _ := json.Marshal(EvalRecord{Compared: done})
	if !strings.Contains(string(b), `"source":"the reference at this commit"`) || strings.Contains(string(b), `"compared":null`) {
		t.Fatalf("record: %s", b)
	}
}
