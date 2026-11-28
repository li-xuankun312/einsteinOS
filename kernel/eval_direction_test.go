package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 11/21 08:44-14:07 the integration's runs were "python3 - + vllm", then "pytools/e2e_weights.py
// + build/e2e_all_check + parity binaries". A direction is what the processes measurably were;
// two runs of the same programs in two verification directories are one direction.
func TestADirectionIsTheRunsProcesses(t *testing.T) {
	ws1, ws2 := "/tmp/einsteinos_verify_1", "/tmp/einsteinos_verify_2"
	stubs := func(ws string) EvalBenchRun {
		return EvalBenchRun{Loaders: []E2ELoader{
			{Exe: "/usr/local/bin/python3.10", Cmdline: "python3 " + ws + "/pytools/e2e_weights.py /root/model", Port: true, Weights: 1, PortKernels: []string{"k"}},
			{Exe: ws + "/build/e2e_all_check", Cmdline: ws + "/build/e2e_all_check /root/model", Port: true, Weights: 1},
			{Exe: ws + "/build/kv_q8_parity", Port: true, PortKernels: []string{"kv_append_q8_kernel"}},
			{Exe: "/usr/bin/cat", Cmdline: "cat x"}, // neither read the model nor launched a ported kernel
		}}
	}
	k1, k2 := directionKey(ws1, stubs(ws1), nil), directionKey(ws2, stubs(ws2), nil)
	if k1 != k2 || k1 != "python3.10 pytools/e2e_weights.py [port]" {
		t.Fatalf("%q / %q", k1, k2)
	}
	// the units' tests run beside the model are verification, not the direction: one more or
	// one fewer is the same direction
	more := stubs(ws1)
	more.Loaders = append(more.Loaders, E2ELoader{Exe: ws1 + "/build/gr_parity", Cmdline: ws1 + "/build/gr_parity --selftest", Port: true, PortKernels: []string{"gr_norm_kernel"}})
	if k := directionKey(ws1, more, nil); k != k1 {
		t.Fatalf("a unit's test changed the direction: %q", k)
	}
	vllm := EvalBenchRun{Loaders: []E2ELoader{{Exe: "/usr/local/bin/python3.10", Cmdline: "python3 -", Weights: 2}}}
	if k := directionKey(ws1, vllm, nil); k != "python3.10 - [other]" || k == k1 {
		t.Fatalf("the heredoc's direction: %q", k)
	}

	dir := t.TempDir()
	b := loadDirections(dir, "uintegration01529aaa82b9")
	b.noteDirection(k1, "integration_e2e_partial_model", time.Now(), DirectionMeasure{})
	b.noteDirection(k1, "integration_e2e_partial_model", time.Now(), DirectionMeasure{})
	b.noteDirection(directionKey(ws1, vllm, nil), "integration_e2e_loader_not_port", time.Now(), DirectionMeasure{})
	if err := writeDirections(dir, b); err != nil {
		t.Fatal(err)
	}
	got := loadDirections(dir, "uintegration01529aaa82b9")
	if len(got.Dirs) != 2 || got.Dirs[k1].Verifications != 2 || got.Dirs[k1].LastGate != "integration_e2e_partial_model" {
		t.Fatalf("the book read back: %+v", got.Dirs)
	}
	if _, err := os.Stat(dir + "/.einsteinOS/eval/directions/uintegration01529aaa82b9.json"); err != nil {
		t.Fatal(err)
	}
}

// Only the measures real work raises count as progress, each against the direction's own best;
// progress gives directionLength more verifications; a gate further on is none.
func TestMeaningfulProgressLengthensADirection(t *testing.T) {
	t.Setenv("EINSTEINOS_DIRECTION_LENGTH", "2")
	b := loadDirections(t.TempDir(), "u")
	const key = "build/generate [port] + python3.10 pytools/ref_packer.py [port]"
	d, p := b.noteDirection(key, "integration_e2e_loader_not_port", time.Now(), DirectionMeasure{TookPermille: 400})
	if p || d.Budget != 2 || d.BestTookPermille != 400 {
		t.Fatalf("first run: progress %v budget %d best %d", p, d.Budget, d.BestTookPermille)
	}
	if d, p = b.noteDirection(key, "integration_e2e_partial_model", time.Now(), DirectionMeasure{TookPermille: 400}); p || d.Budget != 2 {
		t.Fatalf("a gate further on counted as progress: %v %d", p, d.Budget)
	}
	if d, p = b.noteDirection(key, "integration_e2e_partial_model", time.Now(), DirectionMeasure{TookPermille: 950, Units: 3}); !p || d.Budget != 4 {
		t.Fatalf("more of the model taken in was not progress: %v %d", p, d.Budget)
	}
	if d, p = b.noteDirection(key, "integration_wrong_tokens", time.Now(), DirectionMeasure{TookPermille: 950, Units: 3, Tokens: 5}); !p || d.Budget != 6 || d.BestTokens != 5 {
		t.Fatalf("agreeing tokens were not progress: %v %+v", p, d)
	}
}

// The three measures of a run: the port's process that took in the most of the model is the
// runner; its units; the agreeing tokens.
func TestADirectionsMeasures(t *testing.T) {
	perUnit := map[string][]string{"u1": {"a"}, "u2": {"b"}, "u3": {"c"}}
	br := EvalBenchRun{ModelBytes: 1000, Loaders: []E2ELoader{
		{Exe: "python3", Weights: 26, TookBytes: 990}, // vllm: not the port's
		{Exe: "/ws/build/e2e_all_check", Port: true, Weights: 1, TookBytes: 3, PortKernels: []string{"a", "b", "c"}},
		{Exe: "/ws/build/generate", Port: true, Weights: 26, TookBytes: 940, PortKernels: []string{"a", "b"}},
	}}
	m := measureDirection(br, perUnit, 7)
	if m.TookPermille != 940 || m.Units != 2 || m.UnitsOf != 3 || m.Tokens != 7 {
		t.Fatalf("%+v", m)
	}
}

// A direction that used its budget with no meaningful progress is closed, and says so with what
// it measured; a direction still progressing, or a run that passed, is not.
func TestADirectionWithoutProgressCloses(t *testing.T) {
	t.Setenv("EINSTEINOS_DIRECTION_LENGTH", "2")
	b := loadDirections(t.TempDir(), "u")
	const key = "build/e2e_all_check [port] + python3.10 pytools/e2e_weights.py [port]"
	b.noteDirection(key, "integration_e2e_partial_model", time.Now(), DirectionMeasure{Units: 9})
	d, _ := b.noteDirection(key, "integration_e2e_partial_model", time.Now(), DirectionMeasure{Units: 9})
	if !d.Closed() {
		t.Fatalf("two runs without progress in a budget of two did not close it: %+v", d)
	}
	k, txt := directionClosedGate("e2e.sh", d)
	if k != "integration_direction_closed" || !strings.Contains(txt, key) || !strings.Contains(txt, "kernels of at most 9 unit(s)") {
		t.Fatalf("%q %s", k, txt)
	}
	b2 := loadDirections(t.TempDir(), "u")
	b2.noteDirection(key, "integration_e2e_partial_model", time.Now(), DirectionMeasure{TookPermille: 100})
	if d, _ := b2.noteDirection(key, "integration_e2e_partial_model", time.Now(), DirectionMeasure{TookPermille: 500}); d.Closed() || d.Budget != 4 {
		t.Fatalf("a progressing direction closed: %+v", d)
	}
	b3 := loadDirections(t.TempDir(), "u")
	b3.noteDirection(key, "", time.Now(), DirectionMeasure{})
	if d, _ := b3.noteDirection(key, "", time.Now(), DirectionMeasure{}); d.Closed() {
		t.Fatal("passing runs closed their direction")
	}
}

// What the conversation and the next attempt are given: every direction as measured, closed
// ones with when — facts, no advice.
func TestTheDirectionsAreToldAsMeasured(t *testing.T) {
	t.Setenv("EINSTEINOS_DIRECTION_LENGTH", "1")
	b := loadDirections(t.TempDir(), "u")
	t0 := time.Date(2026, 11, 21, 10, 39, 0, 0, time.UTC)
	b.noteDirection("python3.10 - [other]", "integration_e2e_loader_not_port", t0, DirectionMeasure{})
	b.noteDirection("build/e2e_all_check [port] + python3.10 pytools/e2e_weights.py [port]", "integration_e2e_partial_model", t0.Add(time.Hour), DirectionMeasure{TookPermille: 1, Units: 9})
	s := b.summary(66 << 30)
	for _, want := range []string{
		"the most of the model's 66.00 GiB its runner took in",
		"\n  python3.10 - [other] — 1/1, 0%, 0 unit(s), 0 token(s), closed at 11/21 10:39",
		"\n  build/e2e_all_check [port] + python3.10 pytools/e2e_weights.py [port] — 1/1, 0%, 9 unit(s), 0 token(s), closed at 11/21 11:39",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("no %q in\n%s", want, s)
		}
	}
	if strings.Index(s, "python3.10 - [other]") > strings.Index(s, "build/e2e_all_check") {
		t.Fatal("not oldest first")
	}
}

// 11/22 18:42 pytools/e2e_port_check.py, which opened the weight files for 2.7 s beside
// pytools/e2e_infer.py, made a new direction of a closed one: the direction is the runner's
func TestAHelperBesideTheRunnerIsTheSameDirection(t *testing.T) {
	ws := "/tmp/einsteinos_verify_uint"
	perUnit := map[string][]string{"u1": {"k"}}
	run := EvalBenchRun{ModelBytes: 100, Loaders: []E2ELoader{
		{Exe: "/usr/local/bin/python3.10", Cmdline: "python3 " + ws + "/pytools/e2e_infer.py /m", Port: true, Weights: 26, TookBytes: 100, PortKernels: []string{"k"}, Seconds: 118},
	}}
	k1 := directionKey(ws, run, perUnit)
	if k1 != "python3.10 pytools/e2e_infer.py [port]" {
		t.Fatalf("%q", k1)
	}
	run.Loaders = append(run.Loaders, E2ELoader{Exe: "/usr/local/bin/python3.10", Cmdline: "python3 " + ws + "/pytools/e2e_port_check.py /m", Port: true, Weights: 26, TookBytes: 3, Seconds: 2.7})
	if k := directionKey(ws, run, perUnit); k != k1 {
		t.Fatalf("a helper reading the files made a new direction: %q", k)
	}
	// without a runner, the process that took in the most of the model names it
	norunner := EvalBenchRun{ModelBytes: 100, Loaders: []E2ELoader{
		{Exe: "/usr/local/bin/python3.10", Cmdline: "python3 " + ws + "/pytools/a.py", Port: true, Weights: 3, TookBytes: 10},
		{Exe: "/usr/local/bin/python3.10", Cmdline: "python3 " + ws + "/pytools/b.py", Port: true, Weights: 3, TookBytes: 60},
	}}
	if k := directionKey(ws, norunner, perUnit); k != "python3.10 pytools/b.py [port]" {
		t.Fatalf("%q", k)
	}
}

// 11/23 the integration's e2e.sh still ran the programs of a direction closed since 11/22: the
// closed directions every program of which the script names outside a comment are measured
// from it before any build
func TestAnE2EScriptRunningAClosedDirectionIsMeasured(t *testing.T) {
	tree := t.TempDir()
	os.MkdirAll(filepath.Join(tree, ".einsteinOS/verify/uint"), 0o755)
	os.WriteFile(filepath.Join(tree, ".einsteinOS/verify/uint/e2e.sh"), []byte(`#!/bin/bash
# Direction: build/e2e_sub [port] (comment only)
python3.10 "$ROOT/pytools/e2e_weights.py" "$MODEL_REAL" || true
"$BUILD/e2e_all_check" "$MODEL_REAL" || true
python3.10 "$ROOT/pytools/e2e_infer.py"
`), 0o644)
	at := time.Date(2026, 11, 22, 7, 55, 0, 0, time.UTC)
	book := &DirectionBook{Unit: "uint", Dirs: map[string]*DirectionStat{
		"build/e2e_all_check [port] + python3.10 pytools/e2e_weights.py [port]": {ClosedAt: at},
		"python3.10 pytools/e2e_infer.py [port]":                                {},
		"build/e2e_sub [port]":                                                  {ClosedAt: at},
	}}
	got := e2eClosedDirections(tree, "uint", book)
	if len(got) != 1 || got[0] != ".einsteinOS/verify/uint/e2e.sh names every program of the direction closed at 11/22 07:55: build/e2e_all_check [port] + python3.10 pytools/e2e_weights.py [port]" {
		t.Fatalf("%q", got)
	}
	if e2eClosedDirections(tree, "uint", nil) != nil {
		t.Fatal("no book, a direction")
	}
}

// 11/28 04:41:02 build/e2e_all_check, a program the port built, took in the whole model and
// launched ported kernels itself, and the run was named after python3.10 pytools/e2e_infer.py,
// whose reads and mappings of the same files summed higher: of the processes that took in the
// model, the port's built program names the run, as it is the runner when both cover the units
func TestAProgramThePortBuiltNamesTheRunBeforeAnInterpreter(t *testing.T) {
	ws := "/tmp/einsteinos_verify_uint"
	perUnit := map[string][]string{"u1": {"k"}, "u2": {"j"}}
	run := EvalBenchRun{ModelBytes: 100, Loaders: []E2ELoader{
		{Exe: "/usr/local/bin/python3.10", Cmdline: "python3 " + ws + "/pytools/e2e_infer.py /m", Port: true, Weights: 26, TookBytes: 180, PortKernels: []string{"k"}, Seconds: 118},
		{Exe: ws + "/build/e2e_all_check", Cmdline: ws + "/build/e2e_all_check /m", Port: true, Built: true, Weights: 26, TookBytes: 100, PortKernels: []string{"j"}, Seconds: 13},
	}}
	if k := directionKey(ws, run, perUnit); k != "build/e2e_all_check [port]" {
		t.Fatalf("%q", k)
	}
	run.Loaders[1].TookBytes = 50
	if k := directionKey(ws, run, perUnit); k != "python3.10 pytools/e2e_infer.py [port]" {
		t.Fatalf("one that did not take in the model named the run: %q", k)
	}
	run.Loaders[1].TookBytes = 100
	run.Loaders[0].PortKernels = []string{"j", "k"}
	run.Loaders[1].PortKernels = []string{"j", "k"}
	if r, ok := e2eRunner(run, perUnit); !ok || !r.Built {
		t.Fatalf("%v %+v", ok, r)
	}
}
