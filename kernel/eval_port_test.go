package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

// The port's counterpart of a reference benchmark is found by its (masked) name where the
// port's build put it and run with the reference's arguments; a missing one runs nothing.
func TestEvalRunsThePortsCounterparts(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "build/bin"), 0o755)
	os.WriteFile(filepath.Join(ws, "build/bin/ref-bench"), []byte("#!/bin/sh\necho \"decode ($1): 64.5 tok/s\"\n"), 0o755)
	run := evalRunPort(ws, []EvalBench{
		{Kind: "cmake_test", Name: "bench_decode", Command: "ref-bench tg128"},
		{Kind: "cmake_target", Name: "ref-perplexity"},
	})
	if len(run.Benches) != 2 || run.Benches[0].Exit != 0 || run.Benches[1].Exit != -1 {
		t.Fatalf("%+v", run.Benches)
	}
	ms := run.Metrics()
	if len(ms) != 1 || ms[0].Label != "decode (tg128)" || ms[0].Value != 64.5 {
		t.Fatalf("%+v", ms)
	}
}
