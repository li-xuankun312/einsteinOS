package kernel

import "testing"

// A conversation the proxy's refusals ended before it wrote anything is not an attempt at the
// unit: dev_collect marks it, and only when it really ended that way.
func TestAConversationEndedByAnAPIErrorIsMarkedSo(t *testing.T) {
	set := func(pid int64, result string) {
		metaMu.Lock()
		taskMeta[pid] = &TaskMeta{Result: result}
		metaMu.Unlock()
		t.Cleanup(func() { metaMu.Lock(); delete(taskMeta, pid); metaMu.Unlock() })
	}
	set(991001, "[API Error] 429 \u5f53\u524d\u8f66\u961f\u6a21\u578b \u63d0\u95ee\u6b21\u6570\u5df2\u8fbe\u4e0a\u9650")
	if _, st := devCollectLite(991001, "o/r"); st[apiEndedKey] != "true" {
		t.Fatalf("an API error was not marked: %v", st)
	}
	// 11/19 08:17:01 a2983f70 (dev_fix_1_pcd027bc02f), as its diag record holds it: the refusal
	// came in the stream, after a request that went out
	set(991003, `[Error] {"type":"error","error":{"type":"rate_limit_error","message":"{\"type\":\"exceeded_limit\",\"resetsAt\":null,\"remaining\":null,\"perModelLimit\":null,\"representativeClaim\":\"concurrents\",\"overageInUse\":false,\"windows\":null}","details":{"error_code":"concurrent_limit_reached"}},"request_id":"req_011ChBxE3KGqVmbuMsqqbSXx"}`)
	if _, st := devCollectLite(991003, "o/r"); st[apiEndedKey] != "true" {
		t.Fatalf("a refusal in the stream was not marked as an API error: %v", st)
	}
	set(991004, "the test prints [Error] when an index is out of range; fixed it and the build passes now. padding padding padding padding")
	if _, st := devCollectLite(991004, "o/r"); st[apiEndedKey] != "" {
		t.Fatalf("an answer that mentions [Error] was marked as an API error: %v", st)
	}
	set(991002, "wrote the files.\n"+"padding so this is not a thin conversation. padding. padding. padding. padding. padding. ")
	if _, st := devCollectLite(991002, "o/r"); st[apiEndedKey] != "" {
		t.Fatalf("a conversation that answered was marked as an API error: %v", st)
	}
}
