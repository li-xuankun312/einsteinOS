package kernel

import (
	"encoding/json"
	"testing"
)

// 11/17 06:41:50 dev_unit_8 (157e4fa7) ended on its loop's do_exit(error) — the server refused a
// Continue with wiggle_container_in_use because round 10 was still running — and dev_collect
// waited until the remote was done (06:45). What the remote ran in round 10 must be in the
// mirror before the kernel verifies it; and that it ran there means the conversation worked.
func TestDevCollectBringsTheMirrorUpToTheRemoteOnceItIsDone(t *testing.T) {
	bootKernel()
	devSettleSleep = 0
	const conv = "157e4fa7-83b1-43a9-a5d8-e0261be1aa11"

	// the tail: round 10 still running at the first look, finished at the second
	looks := 0
	working, _ := json.Marshal(map[string]any{"chat_messages": []any{
		map[string]any{"sender": "assistant", "stop_reason": "end_turn"},
		map[string]any{"sender": "human"}}})
	settled, _ := json.Marshal(map[string]any{"chat_messages": []any{
		map[string]any{"sender": "assistant", "stop_reason": "end_turn"},
		map[string]any{"sender": "human"},
		map[string]any{"sender": "assistant", "stop_reason": "end_turn"}}})
	SetConvTailFn(func(string) ([]byte, error) {
		looks++
		if looks == 1 {
			return working, nil
		}
		return settled, nil
	})
	defer SetConvTailFn(nil)

	var caughtUpAt, calls int
	SetMirrorCatchUpFn(func(convID string) ([]string, error) {
		calls++
		caughtUpAt = looks
		if convID != conv {
			t.Fatalf("caught up %s, want %s", convID, conv)
		}
		return []string{"bash_tool:toolu_r10_sum_chunksAux", "bash_tool:toolu_r10_offsets_aligned", "bash_tool:toolu_r10_test_sh"}, nil
	})
	defer SetMirrorCatchUpFn(nil)

	pid, err := ForkExec("true", "")
	if err != nil {
		t.Fatal(err)
	}
	Wait(pid)
	SetResult(pid, `[Error] {"type":"error","error":{"type":"invalid_request_error","details":{"error_code":"wiggle_container_in_use"}}}`)
	getMeta(pid).ConvID = conv

	_, status := devCollectLite(pid, "Niko1221/Strata")
	if calls != 1 {
		t.Fatalf("the mirror was brought up to the remote %d times, want once", calls)
	}
	if caughtUpAt < 2 {
		t.Fatalf("the catch-up ran at look %d — before the remote was done", caughtUpAt)
	}
	if status["compile"] == "no_response" {
		t.Fatal("a conversation whose remote ran tool calls was read as no response — its mirror would be reset and the work thrown away")
	}

	// the remote ran nothing the stream missed: an error text with no work is still no response
	SetMirrorCatchUpFn(func(string) ([]string, error) { return nil, nil })
	pid2, _ := ForkExec("true", "")
	Wait(pid2)
	SetResult(pid2, "[API Error] status 502")
	getMeta(pid2).ConvID = "no-work-conv"
	if _, s := devCollectLite(pid2, "o/r"); s["compile"] != "no_response" {
		t.Fatalf("an error with no work done must still be no response: %v", s)
	}
}

func TestMirrorCatchUpWithNothingRegisteredRunsNothing(t *testing.T) {
	SetMirrorCatchUpFn(nil)
	if ran, err := mirrorCatchUp("x"); ran != nil || err != nil {
		t.Fatalf("%v %v", ran, err)
	}
	called := false
	SetMirrorCatchUpFn(func(string) ([]string, error) { called = true; return nil, nil })
	defer SetMirrorCatchUpFn(nil)
	if _, _ = mirrorCatchUp(""); called {
		t.Fatal("a child without a conversation was caught up")
	}
}
