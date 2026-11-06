package stepstudio

import "encoding/json"

const (
	EventMessageStart      = "message_start"
	EventContentBlockStart = "content_block_start"
	EventReasoningDelta    = "reasoning_delta"
	EventTextDelta         = "text_delta"
	EventToolCallStart     = "tool_call_start"
	EventToolCallDelta     = "tool_call_delta"
	EventContentBlockStop  = "content_block_stop"
	EventMessageDelta      = "message_delta"
	EventMessageStop       = "message_stop"
	EventError             = "error"
)

type StudioEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type MessageStartData struct {
	MessageID string `json:"message_id"`
}

type ContentBlockStartData struct {
	BlockIndex int    `json:"block_index"`
	BlockType  string `json:"block_type"`
}

type DeltaData struct {
	BlockIndex int    `json:"block_index"`
	Delta      string `json:"delta"`
}

type ToolCallStartData struct {
	BlockIndex    int    `json:"block_index"`
	ToolCallID    string `json:"tool_call_id"`
	ToolName      string `json:"tool_name"`
	ArgumentsJSON string `json:"arguments_json"`
}

type ToolCallDeltaData struct {
	BlockIndex   int    `json:"block_index"`
	Delta        string `json:"delta"`
	ProgressText string `json:"progress_text,omitempty"`
}

type UsageInfo struct {
	TokensIn     int `json:"tokens_in"`
	TokensOut    int `json:"tokens_out"`
	CachedTokens int `json:"cached_tokens"`
}

type MessageDeltaData struct {
	FinishReason string     `json:"finish_reason"`
	Usage        *UsageInfo `json:"usage,omitempty"`
}

type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
