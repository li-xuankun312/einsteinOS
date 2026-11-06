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
