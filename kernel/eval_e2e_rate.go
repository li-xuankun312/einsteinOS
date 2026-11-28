package kernel

// eval_e2e_rate.go — the end-to-end rate is the kernel's measurement, not a number printed.
//
// 11/20 19:42 to 11/21 03:01 the integration's e2e.sh (df250df, cb0950d, cbb5dc6) printed
// "prefill: … tok/s" and "decode: … tok/s" from a formula: a shared-expert kernel's
// milliseconds / 8 × (hidden / 2560) × layers / 0.10 for decode, and decode × 30 for prefill.
// The kernel took them as the port's rates. The rate is now measured on the process that ran
// the port on the model: the tokens it generated (the ids the run prints, which the
// correctness gate checks against the model's own implementation) over the time the watcher
// saw that process running. What the script prints is kept beside it, and judged by nothing.

import (
	"fmt"
	"sort"
	"strings"
)

// e2eMeasuredLabel: the label of the rate the kernel measures.
const e2eMeasuredLabel = "end-to-end generated, measured by the kernel"

const (
	gateE2ERateUnmeasuredFmt = "integration_e2e_unmeasured: the kernel measured no rate of %[1]s: %[2]s; output: %[3]s"
	e2eRateNoLoaderWhy       = "no process of the workspace ran ported kernels on the weights"
	e2eRateNoTokensWhy       = "it printed no generated token ids (\"generated: …\")"
	e2eRateNoTimeWhy         = "the port's loading process was not seen running for any time"
	// %[1]d the runner's pid, %[2]s its command line, %[3]s the processes that printed ids
	e2eRateOthersTokensWhy = "the process that ran the port on the model (pid %[1]d, %[2]s) printed no generated token ids (\"generated: …\"); the ids in the output were printed by: %[3]s"
	// %[1]d pid, %[2]s its executable, %[3]s its command line, %[4]s the lines' kinds
	e2eTokensByFmt = "pid %[1]d %[2]s (%[3]s): %[4]s"
)

// e2eRunnerTokens: the prompt and generated ids the runner printed (the probe's out lines of
// its pid). Without the probe's lines (an older probe log, a test) the run's output is read.
func e2eRunnerTokens(br EvalBenchRun, perUnit map[string][]string) (prompt, generated []int) {
	if br.TokensBy == nil {
		return br.PromptIDs, br.GeneratedIDs
	}
	r, ok := e2eRunner(br, perUnit)
	if !ok {
		return nil, nil
	}
	return e2eTokens(strings.Join(br.TokensBy[r.PID], "\n"))
}

// e2eTokenPrinters: the processes that printed token lines, one entry each.
func e2eTokenPrinters(br EvalBenchRun) string {
	var pids []int
	for pid := range br.TokensBy {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	exe := map[int]E2ELoader{}
	for _, l := range br.Loaders {
		exe[l.PID] = l
	}
	var out []string
	for _, pid := range pids {
		var kinds []string
		for _, l := range br.TokensBy[pid] {
			kinds = append(kinds, strings.TrimSpace(strings.SplitN(l, ":", 2)[0])+":")
		}
		out = append(out, fmt.Sprintf(e2eTokensByFmt, pid, exe[pid].Exe, exe[pid].Cmdline, strings.Join(kinds, " ")))
	}
	if len(out) == 0 {
		return "no process"
	}
	return strings.Join(out, "; ")
}

// e2eMeasureRate: the rate of the port's process that ran the model (e2eRunner: it read the
// weights and launched, itself, a kernel of every unit of perUnit) — the generated tokens over
// the time it was seen running — or why there is none.
func e2eMeasureRate(br EvalBenchRun, perUnit map[string][]string) (Metric, string) {
	r, ok := e2eRunner(br, perUnit)
	switch {
	case !ok:
		return Metric{}, e2eRateNoLoaderWhy
	case len(br.GeneratedIDs) == 0 && len(br.TokensBy) > 0:
		return Metric{}, fmt.Sprintf(e2eRateOthersTokensWhy, r.PID, r.Cmdline, e2eTokenPrinters(br))
	case len(br.GeneratedIDs) == 0:
		return Metric{}, e2eRateNoTokensWhy
	}
	secs := r.Seconds
	if secs <= 0 {
		secs = procWatchTick.Seconds() // seen once: it ran for less than one tick
	}
	return Metric{Label: e2eMeasuredLabel, Value: float64(len(br.GeneratedIDs)) / secs, Unit: "tok/s", Better: "higher"}, ""
}

// e2eRateGate: the run has the kernel's measured rate.
func e2eRateGate(br EvalBenchRun) (kind, text string) {
	for _, m := range br.Metrics {
		if m.Label == e2eMeasuredLabel {
			return "", ""
		}
	}
	why := br.RateWhy
	if strings.TrimSpace(why) == "" {
		why = e2eRateNoLoaderWhy
	}
	return "integration_e2e_unmeasured", fmt.Sprintf(gateE2ERateUnmeasuredFmt, br.Bench.File, why, br.Tail)
}
