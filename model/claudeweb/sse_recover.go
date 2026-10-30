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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
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

// canonInput normalises a tool input so key order / whitespace differences
// between the streamed JSON and the persisted JSON do not matter.
func canonInput(raw string) string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return strings.TrimSpace(raw)
	}
	b, _ := json.Marshal(v) // encoding/json sorts map keys
	return string(b)
}

func toolKey(name, input string) string { return name + "\x00" + canonInput(input) }

// matchRound maps the persisted last message onto the tools we saw streaming.
//
// Primary: tool_use id. The ids in the stream and in the persisted message are
// presumed identical, but nothing in the logs or the web exports we have proves
// it — and if they differed, an id-only wait would stall for the whole window on
// every SSE break. So, when NO persisted id matches, fall back to
// (tool name + canonical input). Content-matched tools get the STREAM id written
// back so the executed/delivered ledger keeps working unchanged.
//
// Residual risk of the content fallback: two consecutive rounds issuing an
// identical single command. Id matching, when it works, is not affected.
func matchRound(have, want []RecoveredTool) (out []RecoveredTool, n int, via string) {
	ids := make(map[string]struct{}, len(have))
	for _, t := range have {
		ids[t.ID] = struct{}{}
	}
	for _, w := range want {
		if _, ok := ids[w.ID]; ok {
			n++
		}
	}
	if n > 0 {
		return have, n, "id"
	}
	used := make([]bool, len(have))
	out = make([]RecoveredTool, len(have))
	copy(out, have)
	for _, w := range want {
		k := toolKey(w.Name, w.Input)
		for i, h := range have {
			if !used[i] && toolKey(h.Name, h.Input) == k {
				used[i] = true
				out[i].ID = w.ID
				n++
				break
			}
		}
	}
	if n == 0 {
		return nil, 0, ""
	}
	return out, n, "content"
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
func waitForRoundTools(c recoverClient, convID string, want []RecoveredTool, cfg recoverCfg) ([]RecoveredTool, error) {
	if len(want) == 0 {
		return nil, errRecoverNoIDs
	}
	deadline := cfg.Now().Add(cfg.Window)
	var partialSince time.Time
	for poll := 1; ; poll++ {
		tools, derr := c.DownloadLastTools(convID)
		if derr == nil {
			matched, n, via := matchRound(tools, want)
			switch {
			case n == len(want):
				cfg.Logf("claudeweb: SSE recovery: round's message found after %d poll(s) (%d/%d tools matched by %s, %d tools in message)",
					poll, n, len(want), via, len(matched))
				return matched, nil
			case n > 0:
				if partialSince.IsZero() {
					partialSince = cfg.Now()
				} else if cfg.Now().Sub(partialSince) >= cfg.Grace {
					cfg.Logf("claudeweb: SSE recovery: accepting message with %d/%d tools matched by %s after %s grace", n, len(want), via, cfg.Grace)
					return matched, nil
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
			elapsed := cfg.Now().Sub(deadline.Add(-cfg.Window))
			remaining := deadline.Sub(cfg.Now())
			// this is deliberately the ONLY evidence a caller gets while waiting:
			// whether the server is still working is unknowable from here (see
			// sse_recover.go's package doc). The remaining-time figure at least
			// tells a person watching the log how much longer this will wait
			// before giving up and falling back — for a long interactive wait,
			// checking the conversation directly in the browser is the one
			// reliable way to see whether it is still generating.
			cfg.Logf("claudeweb: SSE recovery poll %d (%s; is_pending=%v — not trusted) [%s elapsed, giving up in %s if nothing changes]",
				poll, state, pending, elapsed.Round(time.Second), remaining.Round(time.Second))
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
