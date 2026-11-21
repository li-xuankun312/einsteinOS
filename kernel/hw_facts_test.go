package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGroundedRound(t *testing.T) {
	cases := []struct {
		tools     int
		delivered bool
		want      bool
	}{
		{1, false, false}, // the probe's first turn: command + report from the sandbox
		{0, false, false}, // text before anything reached it from this machine
		{2, true, false},  // a later turn that ran tools: its text rests on their sandbox output
		{0, true, true},   // after the writeback, no tool of its own
	}
	for _, c := range cases {
		if got := GroundedRound(c.tools, c.delivered); got != c.want {
			t.Errorf("tools=%d delivered=%v: %v", c.tools, c.delivered, got)
		}
	}
}

// fakeMachine makes gpuProbeFn run the real commands against a directory that looks
// like /dev on the Iluvatar machine, with a toolchain directory holding the programs.
func fakeMachine(t *testing.T, devNames, tools []string) {
	t.Helper()
	dev := t.TempDir()
	for _, n := range devNames {
		os.WriteFile(filepath.Join(dev, n), nil, 0o644)
	}
	tc := t.TempDir()
	for _, n := range tools {
		os.WriteFile(filepath.Join(tc, n), []byte("#!/bin/sh\n"), 0o755)
	}
	oldFn, oldDirs, oldTools := gpuProbeFn, toolchainDirs, toolchainTools
	t.Cleanup(func() { gpuProbeFn, toolchainDirs, toolchainTools = oldFn, oldDirs, oldTools })
	toolchainDirs = []string{tc}
	toolchainTools = []string{"clang++", "nvcc", "ixsmi", "no-such-tool-xyz"}
	gpuProbeFn = func(cmd string) (string, error) {
		out, err := exec.Command("bash", "-c", strings.ReplaceAll(cmd, "'/dev'", "'"+dev+"'")).Output()
		return string(out), err
	}
}

// The 2026/11/02-03 probe, replayed: turn 1 runs the command and reports from the sandbox;
// the kernel writes this machine's output back; turn 2 reports from it and ends.
func TestHardwareBlockIsThisMachineNotTheSandbox(t *testing.T) {
	conv := "9d9dd1c5-0e86-4b65-9cde-fa6266eb9f3a"
	sandbox := "GPU: lspci listed no GPU or accelerator devices. Neither ixsmi nor nvidia-smi is installed. Kernel: Linux 6.18"
	real := "4x Iluvatar BI-V150 (ixsmi), Ubuntu 20.04 kernel 5.4"
	NoteRoundText(conv, sandbox, 1, false) // turn 1: its own command, nothing written back yet
	delivered := true                      // the kernel's writeback of the local output
	NoteRoundText(conv, real+"\n"+DoneMarker, 0, delivered)
	if got := ConvGrounded(conv); got != real {
		t.Fatalf("grounded text %q", got)
	}

	var devNames []string
	for _, n := range []string{"iluvatar0", "iluvatar1", "iluvatar2", "iluvatar3", "nvidia0", "nvidia7", "nvidiactl", "null"} {
		devNames = append(devNames, n)
	}
	fakeMachine(t, devNames, []string{"clang++", "nvcc"})
	h := measureHW()
	h.ProbeConv, h.Summary = conv, ConvGrounded(conv)
	h.Raw = "[bash_tool] toolu_01\n== GPU ==\nIluvatar BI-V150 x4\n"
	got := h.Render()

	for _, want := range []string{
		hwFactsHeader,
		"GPUs: 0,1,2,3 (device nodes /dev/iluvatar<N>)",
		toolchainDirs[0] + "/clang++ " + toolchainDirs[0] + "/nvcc",
		"[the probe conversation 9d9dd1c5, after this machine's output reached it]\n" + real,
		"[this machine's output of the probe's commands]\n[bash_tool] toolu_01",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, bad := range []string{"lspci listed no GPU", "Linux 6.18", DoneMarker, "no-such-tool-xyz"} {
		if strings.Contains(got, bad) {
			t.Errorf("contains %q:\n%s", bad, got)
		}
	}
	// the order says what to trust first: measured, then the summary, then the raw output
	if !(strings.Index(got, "GPUs:") < strings.Index(got, real) && strings.Index(got, real) < strings.Index(got, "Iluvatar BI-V150 x4")) {
		t.Errorf("order:\n%s", got)
	}
	if !isCurrentHWInfo(got) || isCurrentHWInfo("Here is what the probe returned:\n- **GPU:** lspci listed no GPU") {
		t.Error("isCurrentHWInfo")
	}
}

func TestHardwareBlockWithoutGPUsOrSummary(t *testing.T) {
	fakeMachine(t, []string{"null", "tty0"}, nil)
	toolchainTools = []string{"no-such-tool-xyz"} // nothing a real machine's PATH could hold
	h := measureHW()
	h.ProbeConv = "abcdef12-0000"
	got := h.Render()
	for _, want := range []string{
		"GPUs: none (no /dev/<iluvatar|nvidia><N> device nodes)",
		"toolchain: none of no-such-tool-xyz in " + toolchainDirs[0] + " or on PATH",
		"[the probe conversation abcdef12 wrote nothing after this machine's output reached it]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if got := (HWFacts{}).Render(); !strings.Contains(got, hwGPUUnmeasured) {
		t.Errorf("unmeasured:\n%s", got)
	}
	for _, s := range []string{got, h.Render()} {
		if strings.Contains(s, "%!") {
			t.Errorf("format damage:\n%s", s)
		}
	}
}

func TestHardwareSectionAndToolchainInPrompts(t *testing.T) {
	if hardwareSection("") != "" || hardwareSection("  \n") != "" {
		t.Fatal("no info, no block")
	}
	if got := hardwareSection("x"); got != "[HARDWARE]\nx\n\n" {
		t.Fatalf("%q", got)
	}
	old := toolchainDirs
	t.Cleanup(func() { toolchainDirs = old })
	toolchainDirs = []string{"/opt/tc/bin", "/opt/tc2/bin"}
	if got := devVerifyEnv(""); !strings.HasPrefix(got, "export PATH=/opt/tc/bin:/opt/tc2/bin:$PATH; ") {
		t.Fatalf("verify env %q", got)
	}
	r := &devRun{pid: 1, repo: "o/r", devRepo: "o/d", branch: "b", task: "t", gpuAll: "0", hwInfo: HWFacts{GPUMeasured: true, GPUs: "0", GPUVendor: "iluvatar"}.Render()}
	plan := newDevPlan("o/r", "s", []DevModule{{Name: "A.lean", Hash: "h", Bytes: 10}}, [][]string{{"A.lean"}}, "p")
	r.plan = plan
	p := r.unitPrompt(plan.Leaves()[0], 0, 1, 1, DevFailure{}, "b")
	if !strings.Contains(p, "the compilers are in /opt/tc/bin:/opt/tc2/bin on this machine") {
		t.Errorf("toolchain dirs not in the dev prompt")
	}
	if strings.Count(p, "[HARDWARE]") != 1 || !strings.Contains(p, "[HARDWARE]\n"+hwFactsHeader) {
		t.Errorf("hardware block:\n%s", p[:min(len(p), 600)])
	}
	if strings.Contains(p, "/usr/local/corex") || strings.Contains(p, "%!") {
		t.Errorf("hard-coded path or format damage in the dev prompt")
	}
}
