package kernel

import (
	"os"
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
	k1, k2 := directionKey(ws1, stubs(ws1)), directionKey(ws2, stubs(ws2))
	if k1 != k2 || k1 != "build/e2e_all_check [port] + python3.10 pytools/e2e_weights.py [port]" {
		t.Fatalf("%q / %q", k1, k2)
	}
	// the units' tests run beside the model are verification, not the direction: one more or
	// one fewer is the same direction
	more := stubs(ws1)
	more.Loaders = append(more.Loaders, E2ELoader{Exe: ws1 + "/build/gr_parity", Cmdline: ws1 + "/build/gr_parity --selftest", Port: true, PortKernels: []string{"gr_norm_kernel"}})
	if k := directionKey(ws1, more); k != k1 {
		t.Fatalf("a unit's test changed the direction: %q", k)
	}
	vllm := EvalBenchRun{Loaders: []E2ELoader{{Exe: "/usr/local/bin/python3.10", Cmdline: "python3 -", Weights: 2}}}
	if k := directionKey(ws1, vllm); k != "python3.10 - [other]" || k == k1 {
		t.Fatalf("the heredoc's direction: %q", k)
	}

	dir := t.TempDir()
	b := loadDirections(dir, "uintegration01529aaa82b9")
	b.noteDirection(k1, "integration_e2e_partial_model", time.Now(), DirectionMeasure{})
	b.noteDirection(k1, "integration_e2e_partial_model", time.Now(), DirectionMeasure{})
	b.noteDirection(directionKey(ws1, vllm), "integration_e2e_loader_not_port", time.Now(), DirectionMeasure{})
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
