package kernel

import "testing"

// The shapes the evaluated repositories print: llama-bench's table, llama-perplexity's final
// estimate, and "label: value unit" lines; a number without a known unit is not a metric.
func TestMetricsAreReadFromWhatABenchmarkPrinted(t *testing.T) {
	out := `| model | size | backend | ngl | test | t/s |
| ------ | ---: | --- | --: | ---: | ---: |
| qwen 7B Q4_0 | 3.56 GiB | CUDA | 99 | pp512 | 2410.31 ± 12.04 |
| qwen 7B Q4_0 | 3.56 GiB | CUDA | 99 | tg128 | 88.52 ± 0.21 |

Final estimate: PPL = 6.1234 +/- 0.03812
decode latency: 11.3 ms
prefill: 1520.5 tok/s
memory bandwidth = 812.4 GB/s
layers: 32
`
	got := map[string]Metric{}
	for _, m := range metricsIn(out) {
		got[m.Label+"|"+m.Unit] = m
	}
	want := map[string]Metric{
		"qwen 7b q4_0 size=3.56 gib cuda ngl=99 pp512|tok/s": {Value: 2410.31, Better: "higher"},
		"qwen 7b q4_0 size=3.56 gib cuda ngl=99 tg128|tok/s": {Value: 88.52, Better: "higher"},
		"ppl|ppl":               {Value: 6.1234, Better: "lower"},
		"decode latency|ms":     {Value: 11.3, Better: "lower"},
		"prefill|tok/s":         {Value: 1520.5, Better: "higher"},
		"memory bandwidth|GB/s": {Value: 812.4, Better: "higher"},
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok || g.Value != w.Value || g.Better != w.Better {
			t.Errorf("%s: got %+v (all: %v)", k, g, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("extra metrics: %v", got)
	}
}

// Strata's published matrix (bench/results/2026-09-24-final/matrix.md): the unit is in the
// heading, the columns are context lengths.
func TestMetricsFromTablesWhoseUnitIsInTheHeading(t *testing.T) {
	md := "### Prompt processing (tok/s)\n\n| Model | 1K | 4K | 32K |\n|---|---:|---:|---:|\n| Q2_0 | 389 | 539 | 571 |\n| IQ3_XXS | 285 | 410 | - |\n\n### Output (tok/s)\n\n| Model | 1K | 4K | 32K |\n|---|---:|---:|---:|\n| Q2_0 | 88.7 | 94.6 | 87.5 |\n\n### Notes\n\n| Model | 1K |\n|---|---|\n| Q2_0 | 7 |\n"
	got := map[string]float64{}
	for _, m := range metricsIn(md) {
		got[m.Label+"|"+m.Unit+"|"+m.Better] = m.Value
	}
	want := map[string]float64{
		"prompt processing q2_0 1k|tok/s|higher": 389, "prompt processing q2_0 32k|tok/s|higher": 571,
		"prompt processing iq3_xxs 4k|tok/s|higher": 410, "output q2_0 4k|tok/s|higher": 94.6,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %v (all %v)", k, got[k], got)
		}
	}
	if len(got) != 8 {
		t.Errorf("8 numbers under unit headings, got %d: %v", len(got), got)
	}
}

// The reference's generate prints its figures as "<what>  N tokens in T ms  ->  R tok/s"
// (src/program/generate.cpp:2694, 2697); the port's counterpart prints the same.
func TestMetricsFromTheReferencesGenerateLines(t *testing.T) {
	out := "decode                   256 tokens in 2706.4 ms  ->  94.59 tok/s\n" +
		"prefill                  3562 tokens in 6611.2 ms  ->  538.75 tok/s  (time to first token 6670.0 ms)\n"
	got := map[string]float64{}
	for _, m := range metricsIn(out) {
		got[m.Label+"|"+m.Unit] = m.Value
	}
	if got["decode|tok/s"] != 94.59 || got["prefill|tok/s"] != 538.75 || len(got) != 2 {
		t.Fatalf("%v", got)
	}
}
