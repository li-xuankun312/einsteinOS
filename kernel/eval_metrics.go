package kernel

// eval_metrics.go — the kernel reads numbers out of what a benchmark printed; it does not
// decide them. A number counts only with a unit the table below knows, so its direction
// (higher or lower is better) comes from the unit, not from anyone's judgment.

import (
	"regexp"
	"strconv"
	"strings"
)

// Metric is one number a benchmark printed.
type Metric struct {
	Label  string  `json:"label"` // what it measures, as printed (normalized)
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Better string  `json:"better"` // higher | lower
}

// evalUnits: unit as printed → normalized unit and direction.
var evalUnits = map[string][2]string{
	"tok/s": {"tok/s", "higher"}, "tokens/s": {"tok/s", "higher"}, "t/s": {"tok/s", "higher"},
	"gb/s": {"GB/s", "higher"}, "gib/s": {"GiB/s", "higher"}, "gflops": {"GFLOPS", "higher"},
	"tflops": {"TFLOPS", "higher"}, "it/s": {"it/s", "higher"}, "samples/s": {"samples/s", "higher"},
	"ms": {"ms", "lower"}, "us": {"us", "lower"}, "µs": {"us", "lower"}, "ppl": {"ppl", "lower"},
}

var (
	evalKVRe    = regexp.MustCompile(`(?i)([A-Za-z][\w .()/\-]{0,60}?)\s*[:=]\s*([0-9]+(?:\.[0-9]+)?)\s*(tok/s|tokens/s|t/s|GB/s|GiB/s|GFLOPS|TFLOPS|it/s|samples/s|ms|us|µs)\b`)
	evalPPLRe   = regexp.MustCompile(`(?i)\b(PPL|perplexity)\s*[:=]\s*([0-9]+(?:\.[0-9]+)?)`)
	evalNumRe   = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)`)
	evalSpaceRe = regexp.MustCompile(`\s+`)
)

func evalLabel(s string) string {
	return strings.ToLower(strings.TrimSpace(evalSpaceRe.ReplaceAllString(s, " ")))
}

// metricsIn reads every metric of a benchmark's output: "label: value unit" lines, PPL lines,
// and markdown tables whose header names a unit (llama-bench's "| … | test | t/s |").
func metricsIn(output string) []Metric {
	var out []Metric
	seen := map[string]bool{}
	add := func(label, value, unit string) {
		u, ok := evalUnits[strings.ToLower(unit)]
		v, err := strconv.ParseFloat(value, 64)
		label = evalLabel(label)
		if !ok || err != nil || label == "" || seen[label+"|"+u[0]] {
			return
		}
		seen[label+"|"+u[0]] = true
		out = append(out, Metric{Label: label, Value: v, Unit: u[0], Better: u[1]})
	}
	lines := strings.Split(output, "\n")
	var header []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|") {
			cells := strings.Split(strings.Trim(t, "|"), "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			switch {
			case strings.Trim(strings.Join(cells, ""), "-: ") == "":
				continue // the |---| separator
			case header == nil:
				header = cells
				continue
			}
			if len(cells) != len(header) {
				continue
			}
			var label []string
			for i, c := range cells {
				if _, isUnit := evalUnits[strings.ToLower(header[i])]; isUnit {
					continue
				}
				if evalNumRe.MatchString(c) { // a numeric setting keeps its column name: ngl=99
					c = header[i] + "=" + c
				}
				label = append(label, c)
			}
			for i, c := range cells {
				if _, isUnit := evalUnits[strings.ToLower(header[i])]; isUnit {
					if m := evalNumRe.FindStringSubmatch(c); m != nil {
						add(strings.Join(label, " "), m[1], header[i])
					}
				}
			}
			continue
		}
		header = nil
		for _, m := range evalKVRe.FindAllStringSubmatch(l, -1) {
			add(m[1], m[2], m[3])
		}
		if m := evalPPLRe.FindStringSubmatch(l); m != nil {
			add("ppl", m[2], "ppl")
		}
	}
	return out
}
