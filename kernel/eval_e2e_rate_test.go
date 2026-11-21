package kernel

import (
	"strings"
	"testing"
)

// 11/20 19:42 to 11/21 03:01 the integration's e2e.sh printed "decode: … tok/s" from a formula
// over one kernel's milliseconds. The rate is the generated tokens over the time the port's
// loading process ran; what was printed is kept and judged by nothing.
func TestTheEndToEndRateIsTheKernelsMeasurement(t *testing.T) {
	br := EvalBenchRun{Bench: EvalBench{File: "e2e.sh"}, GeneratedIDs: make([]int, 64),
		Loaders: []E2ELoader{
			{PID: 1, Exe: "/usr/local/bin/python3.10", Seconds: 1},
			{PID: 2, Exe: "/ws/build/generate", Port: true, Weights: 26, PortKernels: []string{"gdn_rec_kernel"}, Seconds: 8},
		}}
	m, why := e2eMeasureRate(br, map[string][]string{"u1": {"gdn_rec_kernel"}})
	if why != "" || m.Label != e2eMeasuredLabel || m.Value != 8 {
		t.Fatalf("64 tokens over the port's 8 s: %+v %q", m, why)
	}
	if _, why := e2eMeasureRate(EvalBenchRun{GeneratedIDs: []int{1}, Loaders: []E2ELoader{{Exe: "python3", Weights: 1}}}, nil); why != e2eRateNoLoaderWhy {
		t.Fatalf("no port loader: %q", why)
	}
	if _, why := e2eMeasureRate(EvalBenchRun{Loaders: []E2ELoader{{Port: true, Weights: 1, PortKernels: []string{"k"}, Seconds: 3}}}, nil); why != e2eRateNoTokensWhy {
		t.Fatalf("no tokens: %q", why)
	}
	// a run that prints a formula's rates and has no port loader is unmeasured
	printed := EvalBenchRun{Bench: EvalBench{File: "e2e.sh"}, RateWhy: e2eRateNoLoaderWhy,
		Printed: []Metric{{"prefill", 93.6, "tok/s", "higher"}, {"decode", 3.12, "tok/s", "higher"}}}
	if k, txt := e2eRateGate(printed); k != "integration_e2e_unmeasured" || !strings.Contains(txt, e2eRateNoLoaderWhy) {
		t.Fatalf("printed rates passed: %q %s", k, txt)
	}
	br.Metrics = []Metric{m}
	if k, txt := e2eRateGate(br); k != "" {
		t.Fatalf("a measured rate failed: %s", txt)
	}
}
