package kernel

// eval_bars.go — the rates the integration's own must be above for a commit to be complete.
//
// The run ends when the port's end-to-end rate, as the kernel measures it (eval_e2e_rate.go),
// is above the tok/s of each of google, anthropic, zhipu and deepseek. The numbers are the
// .format argument EINSTEINOS_TOKS_BARS ("google=…,anthropic=…,zhipu=…,deepseek=…"); a bar not
// given is a comparison that cannot be made, and a commit is not complete on it.

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// barNames: whose tok/s the integration's rate must be above.
var barNames = []string{"google", "anthropic", "zhipu", "deepseek"}

// integrationBars: the bars given in EINSTEINOS_TOKS_BARS, by name.
var integrationBars = func() map[string]float64 {
	out := map[string]float64{}
	for _, part := range strings.Split(os.Getenv("EINSTEINOS_TOKS_BARS"), ",") {
		name, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil && v > 0 {
			out[strings.ToLower(strings.TrimSpace(name))] = v
		}
	}
	return out
}

const (
	gateBarsUnmeasuredFmt = "integration_bars_unmeasured: the commit is complete when the end-to-end rate the kernel measured is above the tok/s of %[1]s; no tok/s is given for %[2]s (EINSTEINOS_TOKS_BARS), so the comparison cannot be made"
	gateBelowBarsFmt      = "integration_below_bars: the end-to-end rate the kernel measured, %[1]s tok/s, is not above %[2]s"
	barFmt                = "%[1]s %[2]s tok/s"
)

// integrationBarsGate: the measured rate is above every bar; the bars it was compared with
// are returned as comparisons.
func integrationBarsGate(br EvalBenchRun) (kind, text string, done []EvalComparison) {
	bars := integrationBars()
	var missing []string
	for _, n := range barNames {
		if _, ok := bars[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return "integration_bars_unmeasured", fmt.Sprintf(gateBarsUnmeasuredFmt, strings.Join(barNames, ", "), strings.Join(missing, ", ")), nil
	}
	var rate *Metric
	for i, m := range br.Metrics {
		if m.Label == e2eMeasuredLabel {
			rate = &br.Metrics[i]
		}
	}
	if rate == nil {
		return "integration_e2e_unmeasured", fmt.Sprintf(gateE2ERateUnmeasuredFmt, br.Bench.File, br.RateWhy, br.Tail), nil
	}
	var notAbove []string
	names := append([]string{}, barNames...)
	sort.Strings(names)
	for _, n := range names {
		bar := Metric{Label: n + " tok/s", Value: bars[n], Unit: "tok/s", Better: "higher"}
		worse := rate.Value <= bar.Value
		done = append(done, EvalComparison{Port: *rate, Other: &bar, Worse: worse, Source: "EINSTEINOS_TOKS_BARS " + n})
		if worse {
			notAbove = append(notAbove, fmt.Sprintf(barFmt, n, strconv.FormatFloat(bar.Value, 'f', -1, 64)))
		}
	}
	if len(notAbove) > 0 {
		return "integration_below_bars", fmt.Sprintf(gateBelowBarsFmt, strconv.FormatFloat(rate.Value, 'f', 2, 64), strings.Join(notAbove, ", ")), done
	}
	return "", "", done
}
