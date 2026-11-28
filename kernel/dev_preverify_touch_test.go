package kernel

import (
	"testing"
	"time"
)

// A conversation whose done line is being verified by the kernel is active for as long as the
// verification runs: the watchdog measures the conversation, not the kernel's work for it.
func TestAConversationWaitingOnItsVerificationIsNotIdle(t *testing.T) {
	const pid, role = int64(990001), "dev_unit_41_touch_test"
	metaMu.Lock()
	taskMeta[pid] = &TaskMeta{Role: role, LastActivity: time.Now().Add(-3 * time.Hour).Unix()}
	metaMu.Unlock()
	defer func() { metaMu.Lock(); delete(taskMeta, pid); metaMu.Unlock() }()
	old := preVerifyTouchEvery
	preVerifyTouchEvery = 20 * time.Millisecond
	defer func() { preVerifyTouchEvery = old }()
	var during int64
	RegisterPreVerify(role, func() (bool, string) {
		metaMu.Lock()
		taskMeta[pid].LastActivity = 0
		metaMu.Unlock()
		time.Sleep(100 * time.Millisecond)
		metaMu.Lock()
		during = taskMeta[pid].LastActivity
		metaMu.Unlock()
		return true, ""
	})
	defer UnregisterPreVerify(role)
	PreVerify(role)
	if time.Since(time.Unix(during, 0)) > 5*time.Second {
		t.Fatalf("not marked active while the verification ran: %d", during)
	}
}
