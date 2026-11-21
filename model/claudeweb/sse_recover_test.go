package claudeweb

import (
	"testing"
	"time"
)

type fakeRecoverClient struct {
	now     *time.Time
	appear  time.Time // the round's message is persisted from this time on
	tools   []RecoveredTool
	failErr bool
}

func (f *fakeRecoverClient) CompletionStatus(string) (bool, bool, error) {
	return false, f.failErr, nil
}
func (f *fakeRecoverClient) DownloadLastTools(string) ([]RecoveredTool, error) {
	if f.now.Before(f.appear) {
		return []RecoveredTool{{ID: "earlier_round", Name: "bash_tool", Input: `{"command":"ls"}`}}, nil
	}
	return f.tools, nil
}

func fakeClock() (*time.Time, recoverCfg) {
	now := time.Date(2026, 11, 12, 2, 58, 0, 0, time.UTC)
	cfg := defaultRecoverCfg()
	cfg.Now = func() time.Time { return now }
	cfg.Sleep = func(d time.Duration) { now = now.Add(d) }
	cfg.Logf = func(string, ...any) {}
	return &now, cfg
}

// A round of Opus 5.5 runs past any fixed window: the wait lasts until the round's message is
// persisted, and every poll tells the kernel the conversation is alive. 11/12 02:58 unit 5's
// round broke its stream; the 30-minute window would have given up while it was generating.
func TestRecoveryWaitsForTheRoundPastThirtyMinutesAndTouchesEachPoll(t *testing.T) {
	now, cfg := fakeClock()
	want := []RecoveredTool{{ID: "toolu_a", Name: "bash_tool", Input: `{"command":"make"}`}}
	c := &fakeRecoverClient{now: now, appear: now.Add(45 * time.Minute), tools: want}
	touches := 0
	cfg.Touch = func() { touches++ }
	got, err := waitForRoundTools(c, "conv", want, cfg)
	if err != nil || len(got) != 1 || got[0].ID != "toolu_a" {
		t.Fatalf("got %v, %v: the round's message, 45 minutes later", got, err)
	}
	if want := int(45*time.Minute/cfg.Interval) + 1; touches < want {
		t.Fatalf("touched %d times, want every poll (%d)", touches, want)
	}
}

// The server reporting an error ends the wait: the round will never appear.
func TestRecoveryStopsWhenTheServerReportsAnError(t *testing.T) {
	now, cfg := fakeClock()
	c := &fakeRecoverClient{now: now, appear: now.Add(1000 * time.Hour), failErr: true}
	if _, err := waitForRoundTools(c, "conv", []RecoveredTool{{ID: "x", Name: "n", Input: "{}"}}, cfg); err != errRecoverServer {
		t.Fatalf("%v", err)
	}
}

// A window, when one is set, still bounds the wait.
func TestARecoveryWindowWhenSetStillBoundsTheWait(t *testing.T) {
	now, cfg := fakeClock()
	cfg.Window = 5 * time.Minute
	c := &fakeRecoverClient{now: now, appear: now.Add(time.Hour)}
	if _, err := waitForRoundTools(c, "conv", []RecoveredTool{{ID: "x", Name: "n", Input: "{}"}}, cfg); err == nil {
		t.Fatal("a window that elapsed must end the wait")
	}
}
