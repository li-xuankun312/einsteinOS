package kernel

import (
	"fmt"
	"strings"
	"testing"
)

func TestHWProbePromptIsFactsOnlyOneBatchAndAsksForTheMarker(t *testing.T) {
	p := hwProbePrompt()
	for _, want := range []string{"FACTS ONLY", "ONE command", fmt.Sprint(LocalOutputCap), DoneMarker} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "context:") {
		t.Error("the task must not be in the probe prompt: the child compared the machine with it")
	}
}

func TestParseGPUIndices(t *testing.T) {
	cases := map[string]string{
		"0,1,2,3\n":           "0,1,2,3",
		"":                    "",
		"\n":                  "",
		"0\n1\n1\n":           "0,1",
		"0,1,nvidiactl,3":     "0,1,3",
		"ls: cannot access x": "",
	}
	for in, want := range cases {
		if got := parseGPUIndices(in); got != want {
			t.Errorf("%q → %q want %q", in, got, want)
		}
	}
}

func TestDetectLocalGPUsMeasuresAndDoesNotGuess(t *testing.T) {
	old := gpuProbeFn
	t.Cleanup(func() { gpuProbeFn = old })

	gpuProbeFn = func(string) (string, error) { return "0,1,2,3\n", nil }
	if ids, ok := detectLocalGPUs(); !ok || ids != "0,1,2,3" {
		t.Fatalf("%q %v", ids, ok)
	}
	// a CPU-only machine: measured, and the answer is NONE — not the old default of 4
	gpuProbeFn = func(string) (string, error) { return "", nil }
	if ids, ok := detectLocalGPUs(); !ok || ids != "" {
		t.Fatalf("a machine with no device nodes must report none, got %q %v", ids, ok)
	}
	// the measurement itself failing is not "no GPU": the caller falls back
	gpuProbeFn = func(string) (string, error) { return "", fmt.Errorf("exit 1") }
	if _, ok := detectLocalGPUs(); ok {
		t.Fatal("a failed measurement must say so")
	}
}

func TestGPUIndexCommandOnThisMachine(t *testing.T) {
	bootKernel()
	out, err := gpuProbeFn(gpuIndexCmd)
	if err != nil {
		t.Fatalf("the real command failed here: %v", err)
	}
	// this sandbox has no GPU nodes; the point is that the shell pipeline runs and yields only digits
	if got := parseGPUIndices(out); got != strings.TrimSpace(out) {
		t.Fatalf("pipeline printed something that is not an index list: %q", out)
	}
}
