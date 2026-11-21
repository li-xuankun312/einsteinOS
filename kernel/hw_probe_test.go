package kernel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHWProbePromptIsFactsOnlyOneBatchAndAsksForTheMarker(t *testing.T) {
	p := hwProbePrompt()
	for _, want := range []string{"[HARDWARE]", "[ROUNDS] this conversation ends after", fmt.Sprint(LocalOutputCap()), DoneMarker} {
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

// the real shell pipeline against a directory that looks like /dev on the machines in question
func runGPUCmd(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, n), nil, 0o644)
	}
	out, err := exec.Command("bash", "-c", gpuIndexCmd(dir)).Output()
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestGPUIndexCommandPicksOneVendor(t *testing.T) {
	// the Iluvatar machine: iluvatar0-3 AND nvidia0-7 (+ the nodes that are not cards)
	var names []string
	for i := 0; i < 4; i++ {
		names = append(names, fmt.Sprintf("iluvatar%d", i))
	}
	for i := 0; i < 8; i++ {
		names = append(names, fmt.Sprintf("nvidia%d", i))
	}
	names = append(names, "nvidiactl", "nvidia-uvm", "null", "tty0")
	if got := runGPUCmd(t, names...); got != "0,1,2,3" {
		t.Fatalf("Iluvatar machine: %q, want 0,1,2,3 (4 cards, not the union with the nvidia nodes)", got)
	}
	// an NVIDIA machine
	if got := runGPUCmd(t, "nvidia0", "nvidia1", "nvidiactl", "nvidia-uvm"); got != "0,1" {
		t.Fatalf("nvidia only: %q", got)
	}
	// numeric order, not lexical
	if got := runGPUCmd(t, "iluvatar10", "iluvatar2", "iluvatar0"); got != "0,2,10" {
		t.Fatalf("order: %q", got)
	}
	// no GPU at all: an empty answer, not an error
	if got := runGPUCmd(t, "null", "tty0"); got != "" {
		t.Fatalf("none: %q", got)
	}
}

func TestGPUIndexCommandOnThisMachine(t *testing.T) {
	bootKernel()
	out, err := gpuProbeFn(gpuIndexCmd("/dev"))
	if err != nil {
		t.Fatalf("the real command failed here: %v", err)
	}
	if got := parseGPUIndices(out); got != strings.TrimSpace(out) {
		t.Fatalf("pipeline printed something that is not an index list: %q", out)
	}
}
