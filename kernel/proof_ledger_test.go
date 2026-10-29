package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// Response shapes copied from the Strata run (review#1 / dev_unit#2).
const (
	respOK = `{"content":"import Mathlib\n\ntheorem selection_width_le_nkv (n_kv K r : Nat) (hr : r ≥ 1) :\n    min n_kv (K + r - 1) ≤ n_kv := by\n  omega\n","failed_declarations":[],"lean_messages":{"errors":[],"infos":[],"warnings":["-:3:49-3:51: warning: Variable name ` + "`hr`" + ` is not explicitly referenced."]},"negation":null,"okay":true,"timings":{"total_ms":43}}`

	respNatAnd = `{"content":"x","failed_declarations":[],"lean_messages":{"errors":["-:5:28-5:48: error: Function expected at\n  Nat.and_le_right\nbut this term has type\n  ?m.44 &&& ?m.45 ≤ ?m.45"],"infos":[],"warnings":[]},"okay":false,"timings":{"total_ms":50}}`

	respUserErr = `{"user_error":"failed to compile formal_statement:\n-:5:17: error: Function expected at\n  load8_fp16\nbut this term has type\n  ?m.1"}`
)

func payload(stmt, content string) string { return axlePayload(stmt, content) }

const (
	stmtDecode = "theorem s2_decode_range (byte : Nat) (k : Fin 4) :\n    ((byte >>> (k.val * 2)) &&& 3) < 4 := by sorry"
	badDecode  = "theorem s2_decode_range (byte : Nat) (k : Fin 4) :\n    ((byte >>> (k.val * 2)) &&& 3) < 4 := by\n  have : 3 < 4 := by omega\n  exact Nat.lt_of_le_of_lt (Nat.and_le_right _ 3) this"
	goodDecode = "theorem s2_decode_range (byte : Nat) (k : Fin 4) :\n    ((byte >>> (k.val * 2)) &&& 3) < 4 := by\n  omega"
	stmtWidth  = "theorem selection_width_le_nkv (n_kv K r : Nat) (hr : r ≥ 1) :\n    min n_kv (K + r - 1) ≤ n_kv := by sorry"
	goodWidth  = "theorem selection_width_le_nkv (n_kv K r : Nat) (hr : r ≥ 1) :\n    min n_kv (K + r - 1) ≤ n_kv := by\n  omega"
)

func TestAxleVerdict(t *testing.T) {
	if ok, _, usable := AxleVerdict(respOK); !ok || !usable {
		t.Fatalf("okay response misread: ok=%v usable=%v", ok, usable)
	}
	// shadow prefix must be tolerated
	if ok, _, usable := AxleVerdict("[pid 0 exit=0] " + respOK); !ok || !usable {
		t.Fatalf("prefixed response misread")
	}
	ok, msg, usable := AxleVerdict(respNatAnd)
	if ok || !usable || !strings.Contains(msg, "Nat.and_le_right") {
		t.Fatalf("failing response misread: ok=%v usable=%v msg=%q", ok, usable, msg)
	}
	ok, msg, usable = AxleVerdict(respUserErr)
	if ok || !usable || !strings.Contains(msg, "load8_fp16") {
		t.Fatalf("user_error misread: ok=%v usable=%v msg=%q", ok, usable, msg)
	}
	for _, junk := range []string{"", "Usage: curl [options...] <url>", "<!DOCTYPE html>", `{"detail":"x"}`} {
		if _, _, usable := AxleVerdict(junk); usable {
			t.Fatalf("junk treated as evidence: %q", junk)
		}
	}
}

func TestRecordAxleStatuses(t *testing.T) {
	l := NewProofLedger()
	l.RecordAxle(payload(stmtWidth, goodWidth), respOK)
	l.RecordAxle(payload(stmtDecode, badDecode), respNatAnd)

	if r, _ := l.Get("selection_width_le_nkv"); r.Status != ProofVerified || r.VerifiedContent == "" {
		t.Fatalf("width should be verified: %+v", r)
	}
	r, _ := l.Get("s2_decode_range")
	if r.Status != ProofFailed || !strings.Contains(r.Error, "Nat.and_le_right") {
		t.Fatalf("decode should be failed with the Axle error: %+v", r)
	}
	if r.VerifiedContent != "" {
		t.Fatalf("failed theorem must not carry verified content")
	}
}

func TestSorryIsNeverVerified(t *testing.T) {
	l := NewProofLedger()
	// Axle returns okay=true for `by sorry` (just a warning). Not a proof.
	l.RecordAxle(payload(stmtWidth, stmtWidth), respOK)
	if r, _ := l.Get("selection_width_le_nkv"); r.Status != ProofUnverified {
		t.Fatalf("sorry content must be unverified, got %+v", r)
	}
	// but the word inside a comment or identifier must not poison a real proof
	commented := goodWidth + "\n-- no sorry needed here"
	l2 := NewProofLedger()
	l2.RecordAxle(payload(stmtWidth, commented), respOK)
	if r, _ := l2.Get("selection_width_le_nkv"); r.Status != ProofVerified {
		t.Fatalf("comment mentioning sorry wrongly blocked verification: %+v", r)
	}
}

func TestNonAxleResponseChangesNothing(t *testing.T) {
	l := NewProofLedger()
	l.RecordAxle(payload(stmtWidth, goodWidth), respOK)
	l.RecordAxle(payload(stmtWidth, goodWidth), "Usage: curl [options...] <url>")
	l.RecordAxle(payload(stmtWidth, goodWidth), "")
	if r, _ := l.Get("selection_width_le_nkv"); r.Status != ProofVerified || r.Attempts != 1 {
		t.Fatalf("infra noise altered ledger: %+v", r)
	}
}

func TestGateAndVerdict(t *testing.T) {
	l := NewProofLedger()
	l.RecordAxle(payload(stmtWidth, goodWidth), respOK)
	l.RecordAxle(payload(stmtDecode, badDecode), respNatAnd)
	names := []string{"selection_width_le_nkv", "s2_decode_range", "never_seen"}

	g := l.Check(names)
	if !g.Blocked() || len(g.Failed) != 1 || g.Failed[0] != "s2_decode_range" {
		t.Fatalf("gate wrong: %+v", g)
	}
	if len(g.Unverified) != 1 || g.Unverified[0] != "never_seen" {
		t.Fatalf("missing record must be unverified: %+v", g)
	}
	v := l.FailVerdict(g)
	if !strings.HasPrefix(v, "FAIL: proof_failed: s2_decode_range") || !strings.Contains(v, "Nat.and_le_right") {
		t.Fatalf("verdict not typed/informative: %q", v)
	}

	l.MarkDegraded("s2_decode_range", "repair budget spent")
	g = l.Check(names)
	if g.Blocked() || len(g.Degraded) != 1 {
		t.Fatalf("degraded failure must stop blocking but stay visible: %+v", g)
	}
	tbl := l.Table(names)
	if !strings.Contains(tbl, "UNPROVEN") || !strings.Contains(tbl, "VERIFIED") || !strings.Contains(tbl, "UNVERIFIED") {
		t.Fatalf("table must show all three states:\n%s", tbl)
	}
	// fresh evidence resets the degrade flag
	l.RecordAxle(payload(stmtDecode, badDecode), respNatAnd)
	if !l.Check(names).Blocked() {
		t.Fatalf("new failure after degrade must block again")
	}
}

// Spec text as the Strata run actually stored it: one long line with literal
// backslash-n sequences (see dev.txt L128), and the same thing with real newlines.
func specVariants() map[string]string {
	real := "## MODULE: s2_gemv\n### LAYER 1b — LEAN\n```lean\n-- axle_verify: formal_statement\n" + stmtWidth +
		"\n\n-- axle_verify: content\n" + goodWidth + "\n```\n\n```lean\n-- axle_verify: formal_statement\n" + stmtDecode +
		"\n\n-- axle_verify: content\n" + badDecode + "\n```\n"
	esc := strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(real)
	return map[string]string{"real-newlines": real, "json-escaped": esc}
}

func TestParseSpecProofsBothEncodings(t *testing.T) {
	for name, spec := range specVariants() {
		got := ParseSpecProofs(spec)
		if len(got) != 2 {
			t.Fatalf("%s: want 2 proofs, got %d: %+v", name, len(got), got)
		}
		if got[1].Name != "s2_decode_range" || !strings.Contains(got[1].Content, "Nat.and_le_right") ||
			!strings.HasSuffix(strings.TrimSpace(got[1].Statement), "by sorry") {
			t.Fatalf("%s: bad parse of decode: %+v", name, got[1])
		}
		if n := SpecTheorems(spec); len(n) != 2 {
			t.Fatalf("%s: SpecTheorems = %v", name, n)
		}
	}
}

// fakeAxle accepts proofs that use omega/decide and rejects the Nat.and_le_right
// misuse — the same verdicts the real service gave.
func fakeAxle(payloadJSON string) string {
	var p struct{ Content string }
	_ = json.Unmarshal([]byte(payloadJSON), &p)
	if strings.Contains(p.Content, "Nat.and_le_right") {
		return "[pid 0 exit=0] " + respNatAnd
	}
	return "[pid 0 exit=0] " + respOK
}

func TestEnsureSpecVerifiedFindsTheBadProof(t *testing.T) {
	SetAxleVerifier(fakeAxle)
	defer SetAxleVerifier(nil)
	for name, spec := range specVariants() {
		l := NewProofLedger()
		bad := l.EnsureSpecVerified(spec)
		if len(bad) != 1 || bad[0] != "s2_decode_range" {
			t.Fatalf("%s: kernel should find exactly the bad proof, got %v", name, bad)
		}
		if g := l.Check(SpecTheorems(spec)); !g.Blocked() {
			t.Fatalf("%s: gate must block on the failed proof", name)
		}
	}
}

func TestNoVerifierMeansUnverifiedNotVerified(t *testing.T) {
	SetAxleVerifier(nil)
	l := NewProofLedger()
	bad := l.EnsureSpecVerified(specVariants()["real-newlines"])
	if len(bad) != 2 {
		t.Fatalf("without a verifier nothing may count as verified, got %v", bad)
	}
	if l.Check([]string{"selection_width_le_nkv"}).Blocked() {
		t.Fatalf("absence of evidence must not block (infra outage ≠ bad proof)")
	}
}

func TestPatchSpecProof(t *testing.T) {
	SetAxleVerifier(fakeAxle)
	defer SetAxleVerifier(nil)
	for name, spec := range specVariants() {
		l := NewProofLedger()
		patched, ok := l.PatchSpecProof(spec, "s2_decode_range", goodDecode)
		if !ok {
			t.Fatalf("%s: patch refused", name)
		}
		if strings.Contains(patched, "Nat.and_le_right") {
			t.Fatalf("%s: old proof still present", name)
		}
		if bad := l.EnsureSpecVerified(patched); len(bad) != 0 {
			t.Fatalf("%s: patched spec should now verify fully, still bad: %v", name, bad)
		}
		// the untouched theorem's text must survive byte-for-byte
		if !strings.Contains(patched, escapeIf(name, goodWidth)) {
			t.Fatalf("%s: unrelated theorem was altered", name)
		}
	}
}

func escapeIf(variant, s string) string {
	if variant == "json-escaped" {
		return escapeLikeSpec(s)
	}
	return s
}

func TestPatchRefusals(t *testing.T) {
	SetAxleVerifier(fakeAxle)
	defer SetAxleVerifier(nil)
	spec := specVariants()["real-newlines"]
	l := NewProofLedger()
	if _, ok := l.PatchSpecProof(spec, "s2_decode_range", stmtDecode); ok {
		t.Fatalf("sorry must never be patched in as a proof")
	}
	if _, ok := l.PatchSpecProof(spec, "s2_decode_range", badDecode); ok {
		t.Fatalf("a proof Axle rejects must not be patched in")
	}
	if _, ok := l.PatchSpecProof(spec, "no_such_theorem", goodDecode); ok {
		t.Fatalf("unknown theorem must not be patched")
	}
}
