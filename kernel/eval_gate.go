package kernel

// eval_gate.go — the integration unit passes only when, on this machine, the port does at
// least what the reference itself does on its own benchmarks at the same commit, and at
// least what the port did at the commit integrated before. Both sides are the kernel's
// measurements of code from outside the pipeline; there is no threshold anyone picked, only
// "not worse" in the direction the unit gives.

import (
	"fmt"
	"strings"
)

const (
	gateEvalMissingFmt   = "integration_metric_missing: %[1]s printed %[2]q (%[3]s) and the port's run of the same benchmark did not"
	gateEvalBelowRefFmt  = "integration_below_reference: %[1]q: the port %[2]g %[3]s, the reference built and run on this machine %[4]g %[3]s (%[5]s is better)"
	gateEvalRegressedFmt = "integration_regressed: %[1]q: the port %[2]g %[3]s, at the integration of %[4]s it was %[5]g %[3]s (%[6]s is better)"
	evalSourceReference  = "the reference at this commit"
	evalSourcePrevFmt    = "the port at the integration of %[1]s"
	evalSourcePrevE2EFmt = "the port's end-to-end run at the integration of %[1]s"
)

func evalKey(label, unit string) string { return evalLabel(label) + "|" + unit }

func evalWorse(port, other float64, better string) bool {
	if better == "lower" {
		return port > other
	}
	return port < other
}

// evalGate judges rec against the reference's numbers in it and the previous record (nil for
// the first integrated commit). mask maps the reference's labels to the names the port uses.
func evalGate(rec EvalRecord, prev *EvalRecord, mask func(string) string) (kind, text string) {
	dev := map[string]Metric{}
	for _, m := range rec.Dev.Metrics() {
		dev[evalKey(m.Label, m.Unit)] = m
	}
	var kinds, texts []string
	fail := func(k, t string) { kinds = append(kinds, k); texts = append(texts, t) }
	for _, r := range rec.Reference.Metrics() {
		label := mask(r.Label)
		d, ok := dev[evalKey(label, r.Unit)]
		switch {
		case !ok:
			fail("integration_metric_missing", fmt.Sprintf(gateEvalMissingFmt, evalSourceReference, label, r.Unit))
		case evalWorse(d.Value, r.Value, r.Better):
			fail("integration_below_reference", fmt.Sprintf(gateEvalBelowRefFmt, label, d.Value, r.Unit, r.Value, r.Better))
		}
	}
	if prev != nil {
		// the end-to-end rates against the previous integration's: 11/10 23:33 commit
		// e5509707's run (57.50 / 11.19 tok/s) was never set against 6801c77f's (57.54 /
		// 11.20) — only the port's benchmarks were, and there were none
		e2e := map[string]Metric{}
		for _, m := range rec.E2E.Metrics {
			e2e[evalKey(m.Label, m.Unit)] = m
		}
		for _, p := range prev.E2E.Metrics {
			d, ok := e2e[evalKey(p.Label, p.Unit)]
			switch {
			case !ok:
				fail("integration_metric_missing", fmt.Sprintf(gateEvalMissingFmt, fmt.Sprintf(evalSourcePrevE2EFmt, shortSHA(prev.RefSHA)), p.Label, p.Unit))
			case evalWorse(d.Value, p.Value, p.Better):
				fail("integration_regressed", fmt.Sprintf(gateEvalRegressedFmt, p.Label, d.Value, p.Unit, shortSHA(prev.RefSHA), p.Value, p.Better))
			}
		}
		for _, p := range prev.Dev.Metrics() {
			d, ok := dev[evalKey(p.Label, p.Unit)]
			switch {
			case !ok:
				fail("integration_metric_missing", fmt.Sprintf(gateEvalMissingFmt, fmt.Sprintf(evalSourcePrevFmt, shortSHA(prev.RefSHA)), p.Label, p.Unit))
			case evalWorse(d.Value, p.Value, p.Better):
				fail("integration_regressed", fmt.Sprintf(gateEvalRegressedFmt, p.Label, d.Value, p.Unit, shortSHA(prev.RefSHA), p.Value, p.Better))
			}
		}
	}
	if len(kinds) == 0 {
		return "", ""
	}
	return kinds[0], strings.Join(texts, "\n")
}
