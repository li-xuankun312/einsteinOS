package claudeweb

import (
	"encoding/json"
	"strings"
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

func TestTheToolsTheStreamMissedAreThoseAfterTheLastOneThisMachineSaw(t *testing.T) {
	all, err := conversationToolUses(download157e4fa7(t))
	if err != nil {
		t.Fatal(err)
	}
	// 06:37:53 "writeback 5 results + Continue": round 9 ran here and its results went back
	l := newToolLedger()
	for _, id := range round9Tools {
		l.record(id, "[pid] exit=0")
		l.markDelivered(id)
	}
	missed := toolsTheStreamMissed(all, l)
	if len(missed) != len(round10Tools) {
		t.Fatalf("%d missed, want the %d of round 10: %+v", len(missed), len(round10Tools), missed)
	}
	for i, id := range round10Tools {
		if missed[i].ID != id {
			t.Fatalf("missed[%d] = %s, want %s (in the order the remote ran them)", i, missed[i].ID, id)
		}
	}

	// a conversation whose stream carried every call has missed none
	for _, id := range round10Tools {
		l.record(id, "x")
	}
	if m := toolsTheStreamMissed(all, l); len(m) != 0 {
		t.Fatalf("every call was seen and %d are still called missed", len(m))
	}

	// a call before the last seen one that this machine lacks was not missed by the stream
	l2 := newToolLedger()
	l2.record(round9Tools[0], "x")
	l2.record(round10Tools[5], "x")
	if m := toolsTheStreamMissed(all, l2); len(m) != 0 {
		t.Fatalf("calls before the last seen one were taken as missed: %+v", m)
	}

	// a ledger that knows none: all of them
	if m := toolsTheStreamMissed(all, newToolLedger()); len(m) != len(all) {
		t.Fatalf("%d of %d with an empty ledger", len(m), len(all))
	}
}

func TestMirrorCatchUpRunsWhatTheStreamMissedThroughTheConversationsLedger(t *testing.T) {
	const conv = "157e4fa7-83b1-43a9-a5d8-e0261be1aa11"
	l := newToolLedger()
	for _, id := range round9Tools {
		l.record(id, "[pid] exit=0")
		l.markDelivered(id)
	}
	var ran []string
	raw := download157e4fa7(t)
	keepConvMirror(conv, &convMirror{
		ledger:   l,
		exec:     func(name, input string) string { ran = append(ran, input); return "[pid] exit=0" },
		download: func() ([]byte, error) { return raw, nil },
	})
	defer func() { convMirrorsMu.Lock(); delete(convMirrors, conv); convMirrorsMu.Unlock() }()

	got, err := MirrorCatchUp(conv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(round10Tools) || len(ran) != len(round10Tools) {
		t.Fatalf("ran %d (%v), returned %d — want the %d calls of round 10", len(ran), ran, len(got), len(round10Tools))
	}
	for i, id := range round10Tools {
		if !strings.Contains(ran[i], id) {
			t.Fatalf("run %d was %s, want the call %s (in the remote's order)", i, ran[i], id)
		}
		if !l.knows(id) {
			t.Fatalf("%s ran here but the ledger does not have it", id)
		}
	}

	// a second catch-up runs nothing: the ledger has them now
	ran = nil
	if got, err := MirrorCatchUp(conv); err != nil || len(got) != 0 || len(ran) != 0 {
		t.Fatalf("second catch-up: %d returned, %d run, err %v", len(got), len(ran), err)
	}

	if _, err := MirrorCatchUp("not-a-conversation-of-this-process"); err == nil {
		t.Fatal("a conversation this process did not run was caught up without an error")
	}
}

// A later loop for the same conversation takes over the earlier loop's ledger: the catch-up
// runs only what this machine has not run, not the whole conversation again.
func TestALaterLoopKeepsWhatTheMachineAlreadyRanForTheConversation(t *testing.T) {
	const conv = "06ca028c-0000-4000-8000-000000000000"
	first := newToolLedger()
	first.record("toolu_a", "[pid 1 exit=0] ok")
	first.markDelivered("toolu_b")
	keepConvMirror(conv, &convMirror{ledger: first})
	second := newToolLedger() // the loop after the proxy refused a round: its ledger is empty
	second.record("toolu_c", "[pid 2 exit=0] ok")
	keepConvMirror(conv, &convMirror{ledger: second})

	all := []RecoveredTool{{ID: "toolu_a"}, {ID: "toolu_b"}, {ID: "toolu_c"}, {ID: "toolu_d"}}
	missed := toolsTheStreamMissed(all, second)
	if len(missed) != 1 || missed[0].ID != "toolu_d" {
		var ids []string
		for _, m := range missed {
			ids = append(ids, m.ID)
		}
		t.Fatalf("the catch-up would run %v again", ids)
	}
}
