package claudeweb

// mirror_catchup.go — the tool calls a conversation's remote ran that this machine never saw.
//
// The stream is the only way a tool call reaches this machine during a round. When it stops
// carrying events while the remote goes on, the remote's calls are real and normal — the
// conversation is working — but this machine's mirror never gets them. 11/17 06:37:54
// dev_unit_8 (157e4fa7) started round 10; the stream delivered no tool_use, ended without a
// stop_reason at 06:41:47, and the remote went on to edit five Lean proofs, run its tests and
// end its reply. The kernel verified the mirror at 06:45:49 without those edits.

import (
	"encoding/json"
	"fmt"
	"sync"

	"google.golang.org/adk/v2/kernel"
)

// conversationToolUses reads every tool_use block of a downloaded conversation, in the order
// of its assistant messages and of the blocks within each.
func conversationToolUses(raw []byte) ([]RecoveredTool, error) {
	var conv struct {
		ChatMessages []struct {
			Sender  string            `json:"sender"`
			Content []json.RawMessage `json:"content"`
		} `json:"chat_messages"`
	}
	if err := json.Unmarshal(raw, &conv); err != nil {
		return nil, fmt.Errorf("parse conversation: %w", err)
	}
	var out []RecoveredTool
	for _, msg := range conv.ChatMessages {
		if msg.Sender != "assistant" {
			continue
		}
		for _, b := range msg.Content {
			var block struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if json.Unmarshal(b, &block) != nil || block.Type != "tool_use" {
				continue
			}
			out = append(out, RecoveredTool{ID: block.ID, Name: block.Name, Input: string(block.Input)})
		}
	}
	return out, nil
}

// knows: this machine has seen the tool call — it ran it (cached, even as "" when shadow
// skipped it) or its result went back to the remote.
func (l *toolLedger) knows(id string) bool {
	if _, ok := l.cached(id); ok {
		return true
	}
	return l.wasDelivered(id)
}

// toolsTheStreamMissed are the conversation's tool calls after the last one this machine has
// seen, in order: what the remote ran after the stream stopped carrying its round. Calls
// before that last one are the past this machine already went through — one that the ledger
// lacks among them was left to the remote on purpose (a broken round's reads), not missed.
// A ledger that knows none of them missed them all.
func toolsTheStreamMissed(all []RecoveredTool, l *toolLedger) []RecoveredTool {
	last := -1
	for i, t := range all {
		if l.knows(t.ID) {
			last = i
		}
	}
	var out []RecoveredTool
	for _, t := range all[last+1:] {
		if !l.knows(t.ID) {
			out = append(out, t)
		}
	}
	return out
}

// convMirror is what bringing a conversation's mirror up to its remote needs once the loop that
// ran the conversation has ended: the loop's ledger, how this machine runs a tool call for this
// conversation, and how to download it.
type convMirror struct {
	ledger   *toolLedger
	exec     func(name, input string) string
	download func() ([]byte, error)
}

var (
	convMirrorsMu sync.Mutex
	convMirrors   = map[string]*convMirror{}
)

// keepConvMirror keeps a conversation's ledger, executor and download past its loop.
func keepConvMirror(convID string, cm *convMirror) {
	if convID == "" || cm == nil {
		return
	}
	convMirrorsMu.Lock()
	convMirrors[convID] = cm
	convMirrorsMu.Unlock()
}

// MirrorCatchUp downloads convID and runs on this machine, in order, the tool calls the
// stream did not carry — through recoveredResults and the conversation's own ledger, as the
// recovery of a broken round with tools does, so nothing is run twice. It returns the calls
// it ran. Nothing goes back to the remote: its round is over, and its calls ran normally there;
// what was missing is this machine's copy of them.
func MirrorCatchUp(convID string) ([]RecoveredTool, error) {
	convMirrorsMu.Lock()
	cm := convMirrors[convID]
	convMirrorsMu.Unlock()
	if cm == nil {
		return nil, fmt.Errorf("conversation %s was not run by this process", truncate(convID, 8))
	}
	raw, err := cm.download()
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	all, err := conversationToolUses(raw)
	if err != nil {
		return nil, err
	}
	missed := toolsTheStreamMissed(all, cm.ledger)
	recoveredResults(cm.ledger, missed, cm.exec)
	return missed, nil
}

// the kernel brings a conversation's mirror up to its remote after the conversation's loop has
// ended (kernel/mirror_catchup.go); the calls are named by tool and id
func init() {
	kernel.SetMirrorCatchUpFn(func(convID string) ([]string, error) {
		missed, err := MirrorCatchUp(convID)
		var ran []string
		for _, t := range missed {
			ran = append(ran, t.Name+":"+t.ID)
		}
		return ran, err
	})
}
