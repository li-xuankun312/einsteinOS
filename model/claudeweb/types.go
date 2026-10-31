package claudeweb

import "encoding/json"

// CompletionRequest is the request body sent to the claude.ai web API.
type CompletionRequest struct {
	Prompt                 string                  `json:"prompt"`
	Timezone               string                  `json:"timezone,omitempty"`
	Locale                 string                  `json:"locale,omitempty"`
	Model                  string                  `json:"model"`
	Effort                 string                  `json:"effort,omitempty"`
	ThinkingMode           string                  `json:"thinking_mode,omitempty"`
	Tools                  []WebTool               `json:"tools"`
	TurnMessageUUIDs       *TurnMessageUUIDs       `json:"turn_message_uuids,omitempty"`
	Attachments            []json.RawMessage        `json:"attachments"`
	Files                  []json.RawMessage        `json:"files"`
	SyncSources            []json.RawMessage        `json:"sync_sources"`
	RenderingMode          string                  `json:"rendering_mode,omitempty"`
	CreateConversationParams *CreateConversationParams `json:"create_conversation_params,omitempty"`
	// For tool results
	ParentMessageUUID string       `json:"parent_message_uuid,omitempty"`
	ToolResults       []ToolResult `json:"tool_results,omitempty"`
}

type TurnMessageUUIDs struct {
	HumanMessageUUID     string `json:"human_message_uuid"`
	AssistantMessageUUID string `json:"assistant_message_uuid"`
}

type CreateConversationParams struct {
	Name                          string `json:"name"`
	Model                         string `json:"model"`
	IncludeConversationPreferences bool   `json:"include_conversation_preferences"`
	IsTemporary                   bool   `json:"is_temporary"`
}

// WebTool is a tool definition in the claude.ai web API format.
type WebTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Type        string          `json:"type,omitempty"`
}

// ToolResult sends a tool execution result back to the conversation.
type ToolResult struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

// SSE Event types returned by the web API.

type SSEEvent struct {
	Event string
	Data  json.RawMessage
}

type EventType struct {
	Type string `json:"type"`
}

// message_start
type MessageStartEvent struct {
	Type    string       `json:"type"`
	Message StartMessage `json:"message"`
}

type StartMessage struct {
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	Role          string            `json:"role"`
	Model         string            `json:"model"`
	UUID          string            `json:"uuid"`
	ParentUUID    string            `json:"parent_uuid"`
	Content       []json.RawMessage `json:"content"`
	StopReason    *string           `json:"stop_reason"`
	StopSequence  *string           `json:"stop_sequence"`
	RequestID     string            `json:"request_id"`
}

// content_block_start
type ContentBlockStartEvent struct {
	Type         string       `json:"type"`
	Index        int          `json:"index"`
	ContentBlock ContentBlock `json:"content_block"`
}

type ContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// content_block_delta
type ContentBlockDeltaEvent struct {
	Type  string     `json:"type"`
	Index int        `json:"index"`
	Delta BlockDelta `json:"delta"`
}

type BlockDelta struct {
	Type        string `json:"type"` // text_delta, input_json_delta
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

// content_block_stop
type ContentBlockStopEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

// message_delta
type MessageDeltaEvent struct {
	Type  string       `json:"type"`
	Delta MessageDelta `json:"delta"`
}

type MessageDelta struct {
	StopReason   *string `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
}

// message_stop
type MessageStopEvent struct {
	Type string `json:"type"`
}
