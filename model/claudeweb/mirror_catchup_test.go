package claudeweb

import (
	"encoding/json"
	"testing"
)

// round9Tools are dev_unit_8's (157e4fa7) round-9 tool calls of 11/17 06:36:39–06:37:52, as the
// stream carried them and this machine ran them (pids 302–306, and a git command shadow ran
// with its pushes as no-ops).
var round9Tools = []string{
	"toolu_01HDEp4T5NyeeZKUu2k3khqo", "toolu_012AKAWQpxkVihQMzP32E9qY", "toolu_01PpmwEuMpxmRsXYDbqvMVgA",
	"toolu_01NmnnysCaRLWQzK8J1kQpqk", "toolu_015z7v3onnEEXtPuUxPqtSTv", "toolu_01QhpvCrejiMCUKFHztwEJkz",
}

// round10Text is the end of the reply that started at 06:37:54 (assistant message
// 01a1e839-f43b-763d-…): the stream carried none of it; the conversation export has it.
const round10Text = "commits made, all tests pass. Summary of preventive fixes:\n" +
	"| `sum_chunksAux` + `Nat.min_le_right T n` | omega needs `min T n ≤ n` |\n" +
	"| `offsets_aligned` base case → `subst` + `exact` | |\n\nEINSTEINOS_DONE"

// round10Tools stand for the five Lean edits of that reply and its test run. Their ids are not
// in the kernel's log — the stream never carried them — so they are named here by position.
var round10Tools = []string{"toolu_r10_sum_chunksAux", "toolu_r10_chunksAux_bounds", "toolu_r10_layer_range_in_slots",
	"toolu_r10_last_layer_has_quota", "toolu_r10_offsets_aligned", "toolu_r10_test_sh"}

func toolBlocks(ids []string) []any {
	var out []any
	for _, id := range ids {
		out = append(out,
			map[string]any{"type": "tool_use", "id": id, "name": "bash_tool",
				"input": map[string]any{"command": "echo " + id, "description": "step"}},
			map[string]any{"type": "tool_result", "tool_use_id": id, "content": []any{map[string]any{"type": "text", "text": "ok"}}})
	}
	return out
}

// download157e4fa7 is a conversation download shaped like 157e4fa7 at 06:45: round 9, the
// kernel's writeback of its results, and round 10 with its tools and its done line.
func download157e4fa7(t *testing.T) []byte {
	t.Helper()
	r9 := append(toolBlocks(round9Tools), map[string]any{"type": "text", "text": "fixing layer_range_ordered"})
	r10 := append(toolBlocks(round10Tools), map[string]any{"type": "text", "text": round10Text})
	conv := map[string]any{"uuid": "157e4fa7-83b1-43a9-a5d8-e0261be1aa11", "chat_messages": []any{
		map[string]any{"uuid": "h9", "sender": "human", "content": []any{map[string]any{"type": "text", "text": "writeback round 8"}}},
		map[string]any{"uuid": "a9", "sender": "assistant", "stop_reason": "end_turn", "content": r9},
		map[string]any{"uuid": "01a1e839-f43b-762a-bc17-557b5cb31c60", "sender": "human", "content": []any{map[string]any{"type": "text", "text": "writeback 5 results + Continue"}}},
		map[string]any{"uuid": "01a1e839-f43b-763d-81f7-00d263bea794", "sender": "assistant", "stop_reason": "end_turn", "content": r10},
	}}
	b, err := json.Marshal(conv)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConversationToolUsesAreReadInMessageOrder(t *testing.T) {
	got, err := conversationToolUses(download157e4fa7(t))
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{}, round9Tools...), round10Tools...)
	if len(got) != len(want) {
		t.Fatalf("%d tool uses, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].ID != want[i] || got[i].Name != "bash_tool" || got[i].Input == "" {
			t.Fatalf("tool %d = %+v, want id %s", i, got[i], want[i])
		}
	}
	if _, err := conversationToolUses([]byte("not json")); err == nil {
		t.Fatal("a download that is not JSON was read without an error")
	}
}
