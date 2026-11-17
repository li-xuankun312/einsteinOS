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
