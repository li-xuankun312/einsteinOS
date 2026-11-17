package claudeweb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

// 11/17 06:37:54–06:45 dev_unit_8 (157e4fa7), end to end on this machine's real shadow: the
// mirror holds ExpertCache.lean as round 9 left it (the omega that Axle refused at line 59);
// the remote's round 10 — which the stream never carried — edited it twice, the second edit on
// the text of the first, ran its check and ended with a done line. After the catch-up the
// mirror holds both edits, in that order, and the done line has changed nothing.
func TestTheMirrorHoldsRound10OfDevUnit8AfterTheCatchUp(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	dir := t.TempDir()
	lean := filepath.Join(dir, "lean", "Baloon", "ExpertCache.lean")
	if err := os.MkdirAll(filepath.Dir(lean), 0o755); err != nil {
		t.Fatal(err)
	}
	round9 := "namespace Baloon.ExpertCache\ntheorem layer_range_in_slots (l slots nl : Nat) : l * (slots / nl) ≤ slots := by\n  omega\nend Baloon.ExpertCache\n"
	if err := os.WriteFile(lean, []byte(round9), 0o644); err != nil {
		t.Fatal(err)
	}
	edit1 := "  omega\n"
	with1 := "  have h2 := Nat.mul_le_mul_left (slots / nl) (Nat.le_refl l)\n  omega\n"
	with2 := "  have h2 := Nat.mul_le_mul_left (slots / nl) (Nat.le_refl l)\n  simpa [Nat.mul_comm] using h2\n"
	in := func(old, new string) map[string]any {
		return map[string]any{"path": lean, "old_str": old, "new_str": new}
	}
	r10 := []any{
		map[string]any{"type": "tool_use", "id": "toolu_r10_layer_range_in_slots", "name": "str_replace", "input": in(edit1, with1)},
		map[string]any{"type": "tool_result", "tool_use_id": "toolu_r10_layer_range_in_slots", "content": []any{map[string]any{"type": "text", "text": "ok"}}},
		map[string]any{"type": "tool_use", "id": "toolu_r10_simpa", "name": "str_replace", "input": in("  omega\n", "  simpa [Nat.mul_comm] using h2\n")},
		map[string]any{"type": "tool_use", "id": "toolu_r10_test_sh", "name": "bash_tool",
			"input": map[string]any{"command": "grep -c 'simpa' " + lean, "description": "check"}},
		map[string]any{"type": "text", "text": round10Text},
	}
	r9 := toolBlocks(round9Tools)
	raw, _ := json.Marshal(map[string]any{"chat_messages": []any{
		map[string]any{"sender": "assistant", "stop_reason": "end_turn", "content": r9},
		map[string]any{"sender": "human", "content": []any{map[string]any{"type": "text", "text": "writeback 5 results + Continue"}}},
		map[string]any{"sender": "assistant", "stop_reason": "end_turn", "content": r10},
	}})

	l := newToolLedger()
	for _, id := range round9Tools {
		l.record(id, "[pid] exit=0")
		l.markDelivered(id)
	}
	s := &ShadowExecutor{WorkDir: dir, Enabled: true}
	const conv = "157e4fa7-e2e"
	keepConvMirror(conv, &convMirror{
		ledger:   l,
		exec:     func(name, input string) string { return s.Execute(name, input) },
		download: func() ([]byte, error) { return raw, nil },
	})
	defer func() { convMirrorsMu.Lock(); delete(convMirrors, conv); convMirrorsMu.Unlock() }()

	ran, err := MirrorCatchUp(conv)
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 3 {
		t.Fatalf("ran %d calls, want round 10's three: %+v", len(ran), ran)
	}
	got, _ := os.ReadFile(lean)
	if string(got) != strings.Replace(round9, edit1, with2, 1) {
		t.Fatalf("the mirror does not hold round 10's two edits in order:\n%s", got)
	}
	if res, _ := l.cached("toolu_r10_test_sh"); !strings.Contains(res, "1") {
		t.Fatalf("round 10's check did not run on the edited file here: %q", res)
	}
	for _, id := range round9Tools {
		if res, _ := l.cached(id); res != "[pid] exit=0" {
			t.Fatalf("round 9's %s was run again (%q)", id, res)
		}
	}
}
