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

// 11/22 05:11-12:48: the port's runner (e2e_weights.py, which read the weights and launched
// ported kernels) printed no ids; vllm, transformers or a forward pass written in python were
// the processes that would have. Only the runner's printed ids are the port's output.
func TestOnlyTheRunnersPrintedIDsAreThePortsOutput(t *testing.T) {
	perUnit := map[string][]string{"u1": {"gdn_rec_kernel"}}
	br := EvalBenchRun{Bench: EvalBench{File: "e2e.sh"}, ModelBytes: 100,
		Loaders: []E2ELoader{
			{PID: 7, Exe: "/usr/local/bin/python3.10", Cmdline: "python3 pytools/e2e_sub.py", Port: true, Weights: 26, TookBytes: 100, Seconds: 30},
			{PID: 9, Exe: "/usr/local/bin/python3.10", Cmdline: "python3 pytools/e2e_weights.py", Port: true, Weights: 26, TookBytes: 100, PortKernels: []string{"gdn_rec_kernel"}, Seconds: 8},
		},
		TokensBy: map[int][]string{7: {"prompt: 1 2", "generated: 5 6 7"}}}
	br.PromptIDs, br.GeneratedIDs = e2eRunnerTokens(br, perUnit)
	if br.PromptIDs != nil || br.GeneratedIDs != nil {
		t.Fatalf("another process's ids were taken: %v %v", br.PromptIDs, br.GeneratedIDs)
	}
	_, why := e2eMeasureRate(br, perUnit)
	for _, w := range []string{"pid 9, python3 pytools/e2e_weights.py", "pid 7 /usr/local/bin/python3.10 (python3 pytools/e2e_sub.py): prompt: generated:"} {
		if !strings.Contains(why, w) {
			t.Fatalf("missing %q in %q", w, why)
		}
	}
	br.TokensBy[9] = []string{"prompt: 1 2", "generated: 3 4 5 6"}
	br.PromptIDs, br.GeneratedIDs = e2eRunnerTokens(br, perUnit)
	if len(br.PromptIDs) != 2 || len(br.GeneratedIDs) != 4 || br.GeneratedIDs[0] != 3 {
		t.Fatalf("the runner's ids: %v %v", br.PromptIDs, br.GeneratedIDs)
	}
	if m, why := e2eMeasureRate(br, perUnit); why != "" || m.Value != 0.5 {
		t.Fatalf("4 ids over the runner's 8 s: %+v %q", m, why)
	}
}

func TestWithoutTheProbesLinesTheOutputIsRead(t *testing.T) {
	br := EvalBenchRun{PromptIDs: []int{1}, GeneratedIDs: []int{2}}
	if p, g := e2eRunnerTokens(br, nil); len(p) != 1 || len(g) != 1 {
		t.Fatalf("%v %v", p, g)
	}
}
