package kernel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A unit's Lean modules are elaborated by Axle before it passes. 11/14 19:25 unit
// ub848524604 was merged with MtpDraftChain.lean's carve referring to "ns" after a script had
// renamed its pattern to "n :: rest"; nothing in the verification elaborated the file.
func TestALeanModuleThatDoesNotCheckFailsTheUnit(t *testing.T) {
	ws := t.TempDir()
	put := func(f, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(ws, f)), 0o755)
		os.WriteFile(filepath.Join(ws, f), []byte(body), 0o644)
	}
	// the merged definition, as it is on einsteinOS/dev-af57b5e66f (d2b04ae)
	put("lean/ub848/MtpDraftChain.lean", "def bump (n : Nat) : Nat := (n + 255) / 256 * 256\n"+
		"def carve (base : Nat) : List Nat → List Nat\n  | [] => []\n  | n :: rest => base :: carve (base + bump n) ns\n")
	put("src/core/session.cpp", "int x;\n")
	var sent []map[string]any
	old := leanVerifyFn
	defer func() { leanVerifyFn = old }()
	leanVerifyFn = func(payload string) string {
		var p map[string]any
		json.Unmarshal([]byte(payload), &p)
		sent = append(sent, p)
		if strings.Contains(p["content"].(string), "carve (base + bump n) ns") {
			return `{"okay":false,"lean_messages":{"errors":["MtpDraftChain.lean:4:44: unknown identifier 'ns'"]},"tool_messages":{"errors":[]}}`
		}
		return `{"okay":true,"lean_messages":{"errors":[]},"tool_messages":{"errors":[]}}`
	}
	changed := []string{"src/core/session.cpp", "lean/ub848/MtpDraftChain.lean"}
	k, txt := leanGate(ws, changed)
	if k != "lean_unproved" || !strings.Contains(txt, "unknown identifier 'ns'") || len(sent) != 1 {
		t.Fatalf("the merged carve: %q %s (%d sent)", k, txt, len(sent))
	}
	if c := sent[0]["content"].(string); !strings.Contains(c, "theorem "+leanCheckName+" : True := trivial") || sent[0]["formal_statement"] != "theorem "+leanCheckName+" : True := sorry" {
		t.Fatalf("payload: %v", sent[0])
	}
	// the review's fix (19:30): the body uses the renamed tail
	put("lean/ub848/MtpDraftChain.lean", "def bump (n : Nat) : Nat := (n + 255) / 256 * 256\n"+
		"def carve (base : Nat) : List Nat → List Nat\n  | [] => []\n  | n :: rest => base :: carve (base + bump n) rest\n")
	if k, txt := leanGate(ws, changed); k != "" {
		t.Fatalf("fixed: %q %s", k, txt)
	}
	put("lean/ub848/MtpDraftChain.lean", "theorem carve_len : True := by\n  sorry\n")
	if k, _ := leanGate(ws, changed); k != "lean_unproved" {
		t.Fatalf("sorry: %q", k)
	}
	put("lean/ub848/MtpDraftChain.lean", "theorem t : True := trivial\n")
	leanVerifyFn = func(string) string { return "" }
	if k, _ := leanGate(ws, changed); k != GateLeanUnchecked || !gateIsMachine(k) {
		t.Fatalf("no Axle: %q", k)
	}
	if k, _ := leanGate(ws, []string{"src/core/session.cpp"}); k != "" {
		t.Fatalf("no Lean changed: %q", k)
	}
}

// 11/17 09:08:45 dev_unit_8 (08aaed5a): four Lean files changed, the check stopped at the
// first that failed (PrefillOrchestration.lean) and ServeEngine.lean was never sent to Axle.
// Every changed file is checked and every failing one is named.
func TestEveryChangedLeanFileIsCheckedAndEveryFailingOneNamed(t *testing.T) {
	ws := t.TempDir()
	for f, body := range map[string]string{
		"lean/Baloon/ExpertCache.lean":          "theorem a : 1 = 1 := rfl\n",
		"lean/Baloon/ExpertSource.lean":         "theorem b : 2 = 2 := rfl\n",
		"lean/Baloon/PrefillOrchestration.lean": "theorem chunks_pos : 0 < min T n := by\n  split_ifs\n",
		"lean/Baloon/ServeEngine.lean":          "theorem admit_sound : x := by\n  omega\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(ws, f)), 0o755)
		os.WriteFile(filepath.Join(ws, f), []byte(body), 0o644)
	}
	var sent int
	old := leanVerifyFn
	defer func() { leanVerifyFn = old }()
	leanVerifyFn = func(payload string) string {
		sent++
		switch {
		case strings.Contains(payload, "split_ifs"):
			return `{"okay":false,"lean_messages":{"errors":["-:29:24-29:40: error: Tactic split_ifs failed: no if-then-else conditions to split"]}}`
		case strings.Contains(payload, "admit_sound"):
			return `{"okay":false,"lean_messages":{"errors":["-:14:2-14:7: error: omega could not prove the goal"]}}`
		}
		return `{"okay":true,"lean_messages":{"errors":[]}}`
	}
	changed := []string{"lean/Baloon/ExpertCache.lean", "lean/Baloon/ExpertSource.lean", "lean/Baloon/PrefillOrchestration.lean", "lean/Baloon/ServeEngine.lean"}
	k, txt := leanGate(ws, changed)
	if k != "lean_unproved" || sent != 4 {
		t.Fatalf("%q, %d of 4 files sent to Axle", k, sent)
	}
	for _, want := range []string{"PrefillOrchestration.lean does not check", "split_ifs", "ServeEngine.lean does not check", "omega could not prove"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("%q missing from the gate text:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "ExpertCache") || strings.Contains(txt, "ExpertSource") {
		t.Fatalf("a file that checks is named:\n%s", txt)
	}
	// a file Axle could not answer on beside one that fails: the unit's failure is reported
	leanVerifyFn = func(payload string) string {
		if strings.Contains(payload, "admit_sound") {
			return ""
		}
		if strings.Contains(payload, "split_ifs") {
			return `{"okay":false,"lean_messages":{"errors":["e"]}}`
		}
		return `{"okay":true}`
	}
	if k, txt := leanGate(ws, changed); k != "lean_unproved" || !strings.Contains(txt, "PrefillOrchestration") {
		t.Fatalf("%q %s", k, txt)
	}
}

// 11/17 dev_unit_8 (08aaed5a): ExpertSource.lean failed at 80:64 (unsolved goals), then at 83:2
// (No goals to be solved), each found one verification after the other. Every error Axle
// answers for a file is in the gate text.
func TestEveryErrorAxleAnswersForAFileIsInTheGateText(t *testing.T) {
	ws := t.TempDir()
	f := "lean/Baloon/ExpertSource.lean"
	os.MkdirAll(filepath.Dir(filepath.Join(ws, f)), 0o755)
	os.WriteFile(filepath.Join(ws, f), []byte("theorem pcieShare_le : x := by\n  omega\n"), 0o644)
	old := leanVerifyFn
	defer func() { leanVerifyFn = old }()
	leanVerifyFn = func(string) string {
		return `{"okay":false,"lean_messages":{"errors":[` +
			`"-:80:64-81:24: error: unsolved goals",` +
			`"-:83:2-83:8: error: No goals to be solved",` +
			`"-:72:29-72:58: error: Application type mismatch"]}}`
	}
	k, txt := leanGate(ws, []string{f})
	if k != "lean_unproved" {
		t.Fatalf("%q", k)
	}
	for _, want := range []string{"80:64-81:24: error: unsolved goals", "83:2-83:8: error: No goals to be solved", "72:29-72:58: error: Application type mismatch"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("%q missing:\n%s", want, txt)
		}
	}
}
