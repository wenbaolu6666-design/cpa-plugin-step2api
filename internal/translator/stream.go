package translator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
	"github.com/google/uuid"
)

type StreamTranslator struct {
	ID           string
	Created      int64
	Model        string
	SentRole     bool
	SentFinish   bool
	MessageID    string
	textBuffer   strings.Builder
	textStreamed bool
	bufferText   bool
	hasToolCalls bool
}

func NewStreamTranslator(model string) *StreamTranslator {
	return &StreamTranslator{
		ID:      "chatcmpl-" + uuid.NewString()[:8],
		Created: time.Now().Unix(),
		Model:   model,
	}
}

func (t *StreamTranslator) RoleChunk() []byte {
	if t.SentRole {
		return nil
	}
	t.SentRole = true
	frame := map[string]any{
		"id":      t.ID,
		"object":  "chat.completion.chunk",
		"created": t.Created,
		"model":   t.Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": "",
				},
				"finish_reason": nil,
			},
		},
	}
	b, _ := json.Marshal(frame)
	return []byte(fmt.Sprintf("data: %s\n\n", string(b)))
}

func (t *StreamTranslator) TranslateEvent(ev stepstudio.StudioEvent) [][]byte {
	var out [][]byte

	switch ev.Type {
	case stepstudio.EventMessageStart:
		var data stepstudio.MessageStartData
		_ = json.Unmarshal(ev.Data, &data)
		t.MessageID = data.MessageID
		if r := t.RoleChunk(); r != nil {
			out = append(out, r)
		}

	case stepstudio.EventContentBlockStart:
		var data stepstudio.ContentBlockStartData
		_ = json.Unmarshal(ev.Data, &data)
		if r := t.RoleChunk(); r != nil {
			out = append(out, r)
		}
		if data.BlockType == "CONTENT_BLOCK_TYPE_REASONING" {
			frame := t.makeDeltaChunk(map[string]any{
				"reasoning_content": "",
			}, nil, nil)
			out = append(out, frame)
		}

	case stepstudio.EventReasoningDelta:
		var data stepstudio.DeltaData
		_ = json.Unmarshal(ev.Data, &data)
		if data.Delta != "" {
			frame := t.makeDeltaChunk(map[string]any{
				"reasoning_content": data.Delta,
			}, nil, nil)
			out = append(out, frame)
		}

	case stepstudio.EventTextDelta:
		var data stepstudio.DeltaData
		_ = json.Unmarshal(ev.Data, &data)
		if data.Delta == "" {
			break
		}
		if t.textStreamed {
			out = append(out, t.makeDeltaChunk(map[string]any{"content": data.Delta}, nil, nil))
			break
		}
		t.textBuffer.WriteString(data.Delta)
		probe := strings.TrimLeft(t.textBuffer.String(), " \t\r\n")
		if probe == "" {
			break
		}
		if strings.HasPrefix(probe, "{") || strings.HasPrefix(probe, "```") {
			t.bufferText = true
			break
		}
		t.textStreamed = true
		buffered := t.textBuffer.String()
		t.textBuffer.Reset()
		out = append(out, t.makeDeltaChunk(map[string]any{"content": buffered}, nil, nil))

	case stepstudio.EventToolCallStart:
		var data stepstudio.ToolCallStartData
		_ = json.Unmarshal(ev.Data, &data)
		t.hasToolCalls = true
		if r := t.RoleChunk(); r != nil {
			out = append(out, r)
		}
		frame := t.makeDeltaChunk(map[string]any{
			"tool_calls": []map[string]any{
				{
					"index": data.BlockIndex,
					"id":    data.ToolCallID,
					"type":  "function",
					"function": map[string]any{
						"name":      data.ToolName,
						"arguments": data.ArgumentsJSON,
					},
				},
			},
		}, nil, nil)
		out = append(out, frame)

	case stepstudio.EventToolCallDelta:
		var data stepstudio.ToolCallDeltaData
		_ = json.Unmarshal(ev.Data, &data)
		text := data.Delta
		if text == "" {
			text = data.ProgressText
		}
		frame := t.makeDeltaChunk(map[string]any{
			"tool_calls": []map[string]any{
				{
					"index": data.BlockIndex,
					"function": map[string]any{
						"arguments": text,
					},
				},
			},
		}, nil, nil)
		out = append(out, frame)

	case stepstudio.EventMessageDelta:
		var data stepstudio.MessageDeltaData
		_ = json.Unmarshal(ev.Data, &data)
		if t.bufferText || t.hasToolCalls {
			break
		}
		out = append(out, t.finishFrame(data.FinishReason, usageMapFrom(data.Usage))...)

	case stepstudio.EventMessageStop:
		// Studio follows message_stop with a terminal "final" event that
		// carries token usage. Wait for it instead of closing the stream now.
	case stepstudio.EventFinal:
		var data stepstudio.FinalData
		_ = json.Unmarshal(ev.Data, &data)
		out = append(out, t.FinalChunks("", usageMapFrom(data.Usage))...)
	case stepstudio.EventDone:
		out = append(out, t.FinalChunks("", nil)...)
	}

	return out
}

func usageMapFrom(usage *stepstudio.UsageInfo) map[string]any {
	if usage == nil {
		return nil
	}
	input := int(usage.TokensIn)
	output := int(usage.TokensOut)
	return map[string]any{
		"prompt_tokens":     input,
		"completion_tokens": output,
		"total_tokens":      input + output,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": int(usage.CachedTokens),
		},
	}
}

func (t *StreamTranslator) makeDeltaChunk(delta map[string]any, finishReason *string, usage map[string]any) []byte {
	choice := map[string]any{
		"index":         0,
		"delta":         delta,
		"finish_reason": finishReason,
	}
	frame := map[string]any{
		"id":      t.ID,
		"object":  "chat.completion.chunk",
		"created": t.Created,
		"model":   t.Model,
		"choices": []map[string]any{choice},
	}
	if usage != nil {
		frame["usage"] = usage
	}
	b, _ := json.Marshal(frame)
	return []byte(fmt.Sprintf("data: %s\n\n", string(b)))
}

func (t *StreamTranslator) FinalChunks(reason string, usage map[string]any) [][]byte {
	if t.SentFinish {
		return nil
	}
	var out [][]byte
	if !t.textStreamed && t.textBuffer.Len() > 0 {
		text := t.textBuffer.String()
		t.textBuffer.Reset()
		if call, ok := ParseToolCallText(text); ok {
			t.hasToolCalls = true
			toolCall := OpenAIToolCall(call.Name, call.Arguments)
			toolCall["index"] = 0
			out = append(out, t.makeDeltaChunk(map[string]any{
				"tool_calls": []map[string]any{toolCall},
			}, nil, nil))
		} else {
			out = append(out, t.makeDeltaChunk(map[string]any{"content": text}, nil, nil))
		}
	}
	out = append(out, t.finishFrame(reason, usage)...)
	return out
}

func (t *StreamTranslator) finishFrame(reason string, usage map[string]any) [][]byte {
	if t.SentFinish {
		return nil
	}
	if t.hasToolCalls {
		reason = "tool_calls"
	} else if strings.TrimSpace(reason) == "" {
		reason = "stop"
	} else {
		reason = strings.TrimPrefix(strings.ToLower(reason), "finish_reason_")
	}
	t.SentFinish = true
	return [][]byte{t.makeDeltaChunk(map[string]any{}, &reason, usage)}
}

func (t *StreamTranslator) FinalChunk(finishReason *string, usage map[string]any) []byte {
	reason := ""
	if finishReason != nil {
		reason = *finishReason
	}
	chunks := t.FinalChunks(reason, usage)
	if len(chunks) == 0 {
		return nil
	}
	return chunks[len(chunks)-1]
}
