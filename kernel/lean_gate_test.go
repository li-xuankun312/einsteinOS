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
