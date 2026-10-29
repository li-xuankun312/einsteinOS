package kernel

import (
	"strings"
	"testing"
)

// The exact situation in the Strata run: compile passed, smoke pending, the
// reviewer said FAIL about a Lean proof and a missing Makefile. This used to be
// classified "unknown" with an empty reason.
func TestClassifyDevFailure_ReviewVerdictIsNotLost(t *testing.T) {
	st := map[string]string{
		"compile": "pass", "smoke": "pending",
		"review": "FAIL: Lean proof s2_decode_range is invalid (Nat.and_le_right); Makefile and CMakeLists.txt were not created",
	}
	ty, msg := classifyDevFailure(st)
	if ty != "review_fail" || !strings.Contains(msg, "Makefile") || !strings.Contains(msg, "s2_decode_range") {
		t.Fatalf("review reason lost: type=%q msg=%q", ty, msg)
	}

	st["review"] = "FAIL: proof_failed: s2_decode_range (error: Function expected)"
	if ty, msg = classifyDevFailure(st); ty != "proof_fail" || !strings.HasPrefix(msg, "proof_failed:") {
		t.Fatalf("proof gate verdict mistyped: %q %q", ty, msg)
	}
}

func TestClassifyDevFailure_MachineEvidenceWinsOverReview(t *testing.T) {
	cases := []struct {
		st   map[string]string
		want string
	}{
		{map[string]string{"compile": "fail", "compile_error": "x", "review": "FAIL: y"}, "compile_fail"},
		{map[string]string{"compile": "unknown", "review": "FAIL: y"}, "compile_unknown"},
		{map[string]string{"compile": "pass", "gpu_test": "fail", "review": "FAIL: y"}, "gpu_fail"},
		{map[string]string{"compile": "pass", "smoke": "fail", "review": "FAIL: y"}, "smoke_fail"},
		{map[string]string{"compile": "pass", "smoke": "pass"}, "unknown"},
	}
	for _, c := range cases {
		if got, _ := classifyDevFailure(c.st); got != c.want {
			t.Errorf("%v: got %q want %q", c.st, got, c.want)
		}
	}
}

// idleStop feeds n tool rounds then text-only rounds and returns how many idle
// rounds it took for the condition to fire (0 = never within 30).
func idleStop(role string) int {
	c := TerminationForRole(role)
	for i := 0; i < 5; i++ {
		if c.Check(ToolEvent{ToolName: "bash", InputRaw: `{"command":"ls"}`, Output: "ok", Round: i + 1, HasTools: true}) != nil {
			return -1 // fired during tool rounds: wrong
		}
	}
	for i := 1; i <= 30; i++ {
		if c.Check(ToolEvent{HasTools: false, Round: 5 + i}) != nil {
			return i
		}
	}
	return 0
}

func TestTermination_IdleProtection(t *testing.T) {
	// dev_fix_1 used to sit in text-only rounds until MaxRounds(20)
	// (15 idle rounds, ~17 minutes in the Strata run).
	if n := idleStop("dev_fix_1"); n != 3 {
		t.Fatalf("dev_fix must stop after 3 idle rounds, got %d", n)
	}
	if n := idleStop("proof_fix_2"); n != 3 {
		t.Fatalf("proof_fix must stop after 3 idle rounds, got %d", n)
	}
}

func TestTermination_ToolRoundsResetIdleCount(t *testing.T) {
	c := TerminationForRole("dev_fix_1")
	for i := 0; i < 2; i++ {
		c.Check(ToolEvent{HasTools: false})
	}
	c.Check(ToolEvent{ToolName: "bash", InputRaw: `{"command":"make"}`, Output: "ok", HasTools: true})
	for i := 0; i < 2; i++ {
		if c.Check(ToolEvent{HasTools: false}) != nil {
			t.Fatalf("fired after only 2 idle rounds following a tool round")
		}
	}
}

// preflight must never block on absence of evidence, and must not touch theory
// that has nothing to verify.
func TestPreflight_NoPairsAndNoVerifier(t *testing.T) {
	SetAxleVerifier(nil)
	Proofs.Reset()
	got, err := dev_proof_preflight(1, "o/r", "b", "## MODULE: x\nno lean here")
	if err != nil || got != "## MODULE: x\nno lean here" {
		t.Fatalf("theory without proofs must pass through: %v %q", err, got)
	}
	spec := specVariants()["real-newlines"]
	got, err = dev_proof_preflight(1, "o/r", "b", spec)
	if err != nil || got != spec {
		t.Fatalf("no verifier must not block or alter theory: %v", err)
	}
}

func TestPreflight_AllVerifiedPassesThrough(t *testing.T) {
	SetAxleVerifier(func(string) string { return "[pid 0 exit=0] " + respOK })
	defer SetAxleVerifier(nil)
	Proofs.Reset()
	spec := specVariants()["real-newlines"]
	got, err := dev_proof_preflight(1, "o/r", "b", spec)
	if err != nil || got != spec {
		t.Fatalf("all-verified theory must pass unchanged: %v", err)
	}
	if g := Proofs.Check(SpecTheorems(spec)); g.Blocked() || len(g.Verified) != 2 {
		t.Fatalf("ledger should show both verified: %+v", g)
	}
}

// The Strata case: s2_decode_range's proof is wrong in the spec and nothing can
// repair it. The pipeline must STOP and name the theorem — not proceed.
func TestPreflight_FailingProofBlocksPipeline(t *testing.T) {
	SetAxleVerifier(fakeAxle)
	defer SetAxleVerifier(nil)
	rounds := 0
	old := proofFixRunner
	proofFixRunner = func(int64, string, string, int, string) error { rounds++; return nil } // child does nothing
	defer func() { proofFixRunner = old }()
	Proofs.Reset()
	spec := specVariants()["json-escaped"] // the encoding the real run had
	got, err := dev_proof_preflight(1, "o/r", "b", spec)
	if err == nil {
		t.Fatalf("a proof Axle rejects must block the pipeline")
	}
	if !strings.Contains(err.Error(), "BLOCKED") || !strings.Contains(err.Error(), "s2_decode_range") ||
		!strings.Contains(err.Error(), "Nat.and_le_right") {
		t.Fatalf("block must name the theorem and Axle's error: %v", err)
	}
	if rounds != 2 {
		t.Fatalf("repair budget is 2 rounds, ran %d", rounds)
	}
	if got != spec {
		t.Fatalf("theory must be left untouched when repair failed")
	}
}

// The happy repair path end to end: the proof_fix child finds a proof Axle
// accepts; the kernel re-verifies it against the ORIGINAL statement, patches the
// theory, and the pipeline proceeds. No code or reviewer involved.
func TestPreflight_RepairPatchesTheory(t *testing.T) {
	SetAxleVerifier(fakeAxle)
	defer SetAxleVerifier(nil)
	old := proofFixRunner
	var prompt string
	proofFixRunner = func(_ int64, _ string, _ string, _ int, p string) error {
		prompt = p
		// what a real child does via the shadow Axle path: one verified attempt
		Proofs.RecordAxle(payload(stmtDecode, goodDecode), respOK)
		return nil
	}
	defer func() { proofFixRunner = old }()
	Proofs.Reset()
	for name, spec := range specVariants() {
		Proofs.Reset()
		got, err := dev_proof_preflight(1, "o/r", "b", spec)
		if err != nil {
			t.Fatalf("%s: repair should have succeeded: %v", name, err)
		}
		if strings.Contains(got, "Nat.and_le_right") {
			t.Fatalf("%s: bad proof still in theory", name)
		}
		if g := Proofs.Check(SpecTheorems(got)); g.Blocked() || len(g.Verified) != 2 {
			t.Fatalf("%s: both theorems must be verified after repair: %+v", name, g)
		}
		if !strings.Contains(prompt, "s2_decode_range") || !strings.Contains(prompt, "Nat.and_le_right") ||
			!strings.Contains(prompt, "ONLY the proof") {
			t.Fatalf("%s: repair prompt must carry the theorem, Axle's error and the immutability rule:\n%s", name, prompt)
		}
	}
}

// A child that "verifies" a WEAKER statement must not get its proof patched in:
// the kernel re-checks against the original statement.
func TestPreflight_WeakenedStatementIsRejected(t *testing.T) {
	SetAxleVerifier(fakeAxle)
	defer SetAxleVerifier(nil)
	old := proofFixRunner
	proofFixRunner = func(int64, string, string, int, string) error {
		weak := "theorem s2_decode_range (byte : Nat) : True := by sorry"
		Proofs.RecordAxle(payload(weak, "theorem s2_decode_range (byte : Nat) : True := by\n  trivial"), respOK)
		return nil
	}
	defer func() { proofFixRunner = old }()
	Proofs.Reset()
	// fakeAxle only rejects Nat.and_le_right, so make the kernel re-check meaningful:
	// patch attempt uses the child's `trivial` proof against the spec statement.
	SetAxleVerifier(func(p string) string {
		if strings.Contains(p, "trivial") {
			return "[pid 0 exit=0] " + respNatAnd // real Axle: `trivial` cannot prove the real statement
		}
		return fakeAxle(p)
	})
	_, err := dev_proof_preflight(1, "o/r", "b", specVariants()["real-newlines"])
	if err == nil {
		t.Fatalf("weakened-statement proof must not unblock the pipeline")
	}
}
