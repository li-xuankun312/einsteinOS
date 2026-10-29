package claudeweb

// sse_recover.go — recovering a round whose SSE stream broke before stop_reason.
//
// What the logs and the web-UI exports (1019_sci) established about the API:
//
//   - Re-sending the same completion request starts a NEW generation (duplicate
//     user message, extra quota). It is not a replay.
//   - The server keeps generating after the client disconnects.
//   - The assistant message of the in-flight turn is NOT in a downloaded
//     conversation until it has finished: 1.md (exported mid-turn) ends with a
//     "dangling" human message; 3.md, exported ~100s later, has the full 52-block
//     assistant message.
//   - completion_status is_pending=false is not proof of completion (commit
//     985237ce said so; the theory chunks showed it again — "server idle" at the
//     first probe, content appearing ~16 minutes later).
//   - Sending anything while the previous response is still generating is
//     refused (wiggle_container_in_use / "Another response is already running").
//
// So the only safe wait is a GET-only poll for the message we are looking for,
// and the only reliable "found it" test is the tool_use IDs we already saw in
// the broken stream. The old code accepted "any last assistant message that has
// tools": after an SSE break that was the PREVIOUS round's message (50 tools in
// the Strata dev_unit#1), which was then replayed.
//
// Local shadow results are deliberately NOT a substitute: shadow skips git, ssh,
// curl/wget, lean and (via the kernel) unknown tools, so the local view of a
// round is incomplete by design.

import (
	"errors"
	"fmt"
	"log"
	"time"
)

// recoverClient is the part of Client that recovery needs.
type recoverClient interface {
	CompletionStatus(convID string) (pending bool, isError bool, err error)
	DownloadLastTools(convID string) ([]RecoveredTool, error)
}

type recoverCfg struct {
	Interval time.Duration // between polls
	Window   time.Duration // give up after this long
	Grace    time.Duration // once only SOME wanted IDs are visible, wait this long for the rest
	Now      func() time.Time
	Sleep    func(time.Duration)
	Logf     func(format string, args ...any)
}

// Defaults are generous on purpose. A theory chunk's first turn legitimately
// takes 15+ minutes to generate; chunk 4's persisted message showed up ~16
// minutes after the break. Waiting costs nothing; guessing wrong loses the round.
func defaultRecoverCfg() recoverCfg {
	return recoverCfg{
		Interval: 10 * time.Second,
		Window:   30 * time.Minute,
		Grace:    60 * time.Second,
		Now:      time.Now,
		Sleep:    time.Sleep,
		Logf:     log.Printf,
	}
}

var (
	errRecoverServer = errors.New("server reported an error for this conversation")
	errRecoverWindow = errors.New("recovery window elapsed without the round's message appearing")
	errRecoverNoIDs  = errors.New("no tool_use ids to look for")
)

func idSet(tools []RecoveredTool) map[string]struct{} {
	s := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		s[t.ID] = struct{}{}
	}
	return s
}

// waitForRoundTools polls (GET only — never a completion request) until the
// persisted last assistant message contains the tool_use IDs in want.
//
//   - all of want present        → return that message's tools at once
//   - some of want present       → wait cfg.Grace for the rest, then return
//   - none present (including "no assistant message found" or a message from an
//     earlier round) → keep waiting; that is exactly the not-yet-persisted state
//   - completion_status error    → stop; the round will never appear
//   - is_pending=false           → logged only, never ends the wait
func waitForRoundTools(c recoverClient, convID string, want []string, cfg recoverCfg) ([]RecoveredTool, error) {
	if len(want) == 0 {
		return nil, errRecoverNoIDs
	}
	deadline := cfg.Now().Add(cfg.Window)
	var partialSince time.Time
	for poll := 1; ; poll++ {
		tools, derr := c.DownloadLastTools(convID)
		if derr == nil {
			have := idSet(tools)
			n := 0
			for _, id := range want {
				if _, ok := have[id]; ok {
					n++
				}
			}
			switch {
			case n == len(want):
				cfg.Logf("claudeweb: SSE recovery: round's message found after %d poll(s) (%d/%d wanted tool ids, %d tools)",
					poll, n, len(want), len(tools))
				return tools, nil
			case n > 0:
				if partialSince.IsZero() {
					partialSince = cfg.Now()
				} else if cfg.Now().Sub(partialSince) >= cfg.Grace {
					cfg.Logf("claudeweb: SSE recovery: accepting message with %d/%d wanted ids after %s grace", n, len(want), cfg.Grace)
					return tools, nil
				}
			}
		}

		pending, isErr, serr := c.CompletionStatus(convID)
		if serr == nil && isErr {
			return nil, errRecoverServer
		}
		if poll == 1 || poll%6 == 0 {
			state := "download: ok, round's ids not there yet"
			if derr != nil {
				state = "download: " + derr.Error()
			}
			cfg.Logf("claudeweb: SSE recovery poll %d (%s; is_pending=%v — not trusted)", poll, state, pending)
		}

		if !cfg.Now().Before(deadline) {
			return nil, fmt.Errorf("%w (%s, %d polls)", errRecoverWindow, cfg.Window, poll)
		}
		cfg.Sleep(cfg.Interval)
	}
}

// ---------------------------------------------------------------------------
// toolLedger: what has been executed locally and what has been delivered
// ---------------------------------------------------------------------------

// toolLedger tracks, per conversation, tool_use IDs by state:
//
//	executed  → local shadow ran it; the result (possibly "" = shadow skipped it)
//	            is cached so recovery never runs it a second time
//	delivered → its result (or deliberate absence) already went to the remote in
//	            a writeback, so it must never be sent again
type toolLedger struct {
	results   map[string]string
	delivered map[string]struct{}
}

func newToolLedger() *toolLedger {
	return &toolLedger{results: map[string]string{}, delivered: map[string]struct{}{}}
}

func (l *toolLedger) record(id, result string) {
	if id != "" {
		l.results[id] = result
	}
}
func (l *toolLedger) cached(id string) (string, bool) { r, ok := l.results[id]; return r, ok }
func (l *toolLedger) markDelivered(ids ...string) {
	for _, id := range ids {
		if id != "" {
			l.delivered[id] = struct{}{}
		}
	}
}
func (l *toolLedger) wasDelivered(id string) bool { _, ok := l.delivered[id]; return id != "" && ok }

// recoveredResults turns the recovered tool list into writeback results.
// Already-delivered tools are skipped (the replay bug), already-executed tools
// reuse their cached result (so the round's results are not lost), and only
// tools the local machine has never seen are executed. An empty result means
// "shadow skipped it — let the remote's own result stand" and is omitted.
func recoveredResults(l *toolLedger, tools []RecoveredTool, exec func(name, input string) string) []ToolResult {
	var out []ToolResult
	for _, t := range tools {
		if l.wasDelivered(t.ID) {
			continue
		}
		res, ok := l.cached(t.ID)
		if !ok {
			res = exec(t.Name, t.Input)
			l.record(t.ID, res)
		}
		if res == "" {
			continue
		}
		out = append(out, ToolResult{ToolUseID: t.ID, Content: res})
	}
	return out
}
