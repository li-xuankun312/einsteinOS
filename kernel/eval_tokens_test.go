package kernel

import (
	"fmt"
	"strings"
	"testing"
)

// The end-to-end run's prompt and generated token ids are read from its output; a run that
// prints only what it generated — 11/09's printed "generated: 220 248046 198 …" and no
// prompt — has its correctness unmeasured.
func TestTheEndToEndRunPrintsItsTokens(t *testing.T) {
	out := "load: 11.1 s\nselfcheck: chunked vs sequential prefill rel L2 = 0.000e+00, argmax 44784 vs 44784\n" +
		"generated: 220 248046 198 248045 248068 271\nprefill: 57.54 tok/s\ndecode: 11.20 tok/s\n"
	p, g := e2eTokens(out)
	if len(p) != 0 || fmt.Sprint(g) != "[220 248046 198 248045 248068 271]" {
		t.Fatalf("11/09's output: prompt %v generated %v", p, g)
	}
	br := EvalBenchRun{Bench: EvalBench{File: ".einsteinOS/verify/u/e2e.sh"}, Tail: out, GeneratedIDs: g}
	if k, txt := e2eTokensGate(br); k != "integration_correctness_unmeasured" || !strings.Contains(txt, `"prompt:"`) {
		t.Fatalf("no prompt line: %q %s", k, txt)
	}
	p, g = e2eTokens("prompt: 9707, 11, 1879\n" + out)
	br.PromptIDs, br.GeneratedIDs = p, g
	if k, _ := e2eTokensGate(br); k != "" || fmt.Sprint(p) != "[9707 11 1879]" {
		t.Fatalf("both lines: %v %v %q", p, g, k)
	}
}
