package translator

import (
	"encoding/json"
	"fmt"
	"sort"
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
	tools        []any
	toolBuffers  map[int]*strings.Builder
	toolNames    map[int]string
	suppressed   map[int]string
	dropped      []string
}

func NewStreamTranslator(model string) *StreamTranslator {
	return &StreamTranslator{
		ID:      "chatcmpl-" + uuid.NewString()[:8],
		Created: time.Now().Unix(),
		Model:   model,
	}
}

// SetTools supplies the tool schemas used to repair stringified argument
// values before they are sent to the client.
func (t *StreamTranslator) SetTools(tools []any) {
	t.tools = tools
}

// hasTool reports whether the client declared the named tool. Studio's own
// agent may call tools that exist only inside the Studio product
// (document_parser, python, todo, file_write); forwarding those to a client
// that never declared them makes the client reject the call.
func (t *StreamTranslator) hasTool(name string) bool {
	return ToolDeclared(t.tools, name)
}

// ToolDeclared reports whether a client-facing tool list contains the name.
func ToolDeclared(tools []any, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if fn, ok := tool["function"].(map[string]any); ok {
			if got, _ := fn["name"].(string); strings.EqualFold(strings.TrimSpace(got), name) {
				return true
			}
		}
		if got, _ := tool["name"].(string); strings.EqualFold(strings.TrimSpace(got), name) {
			return true
		}
	}
	return false
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
		if shouldBufferText(probe) {
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
		if len(t.tools) > 0 && !t.hasTool(data.ToolName) {
			if t.suppressed == nil {
				t.suppressed = map[int]string{}
			}
			t.suppressed[data.BlockIndex] = data.ToolName
			t.dropped = append(t.dropped, data.ToolName)
			break
		}
		t.hasToolCalls = true
		t.textBuffer.Reset()
		t.bufferText = false
		t.ensureToolBuffers()
		if r := t.RoleChunk(); r != nil {
			out = append(out, r)
		}
		t.toolBuffers[data.BlockIndex] = &strings.Builder{}
		t.toolBuffers[data.BlockIndex].WriteString(data.ArgumentsJSON)
		t.toolNames[data.BlockIndex] = data.ToolName
		frame := t.makeDeltaChunk(map[string]any{
			"tool_calls": []map[string]any{
				{
					"index": data.BlockIndex,
					"id":    data.ToolCallID,
					"type":  "function",
					"function": map[string]any{
						"name":      data.ToolName,
						"arguments": "",
					},
				},
			},
		}, nil, nil)
		out = append(out, frame)

	case stepstudio.EventToolCallDelta:
		var data stepstudio.ToolCallDeltaData
		_ = json.Unmarshal(ev.Data, &data)
		if _, ok := t.suppressed[data.BlockIndex]; ok {
			break
		}
		t.ensureToolBuffers()
		if _, ok := t.toolBuffers[data.BlockIndex]; !ok {
			t.toolBuffers[data.BlockIndex] = &strings.Builder{}
		}
		text := data.Delta
		if text == "" {
			text = data.ProgressText
		}
		t.toolBuffers[data.BlockIndex].WriteString(text)

	case stepstudio.EventContentBlockStop:
		var data struct {
			BlockIndex int `json:"block_index"`
		}
		_ = json.Unmarshal(ev.Data, &data)
		if _, ok := t.suppressed[data.BlockIndex]; ok {
			delete(t.suppressed, data.BlockIndex)
			break
		}
		out = append(out, t.flushToolBuffer(data.BlockIndex)...)

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

func shouldBufferText(text string) bool {
	if text == "" {
		return false
	}
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "```") || strings.HasPrefix(text, "`") {
		return true
	}
	for _, prefix := range []string{"exec_command", "update_plan", "write_stdin", "apply_patch", "工具", "tool", "Tool"} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
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
	out = append(out, t.flushAllToolBuffers()...)
	if !t.textStreamed && t.textBuffer.Len() > 0 {
		text := t.textBuffer.String()
		t.textBuffer.Reset()
		if call, ok := ParseToolCallTextWithTools(text, t.tools); ok && (len(t.tools) == 0 || t.hasTool(call.Name)) {
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
	if len(t.dropped) > 0 && !t.hasToolCalls && !t.textStreamed && t.textBuffer.Len() == 0 {
		seen := map[string]bool{}
		var names []string
		for _, name := range t.dropped {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
		notice := fmt.Sprintf("（上游尝试调用内置工具 %s，本环境没有这些工具，该调用已忽略。请重试，或改用其他模型。）", strings.Join(names, ", "))
		out = append(out, t.makeDeltaChunk(map[string]any{"content": notice}, nil, nil))
	}
	out = append(out, t.finishFrame(reason, usage)...)
	return out
}

func (t *StreamTranslator) ensureToolBuffers() {
	if t.toolBuffers == nil {
		t.toolBuffers = map[int]*strings.Builder{}
		t.toolNames = map[int]string{}
	}
}

func (t *StreamTranslator) flushToolBuffer(index int) [][]byte {
	buffer, ok := t.toolBuffers[index]
	if !ok {
		return nil
	}
	delete(t.toolBuffers, index)
	arguments := NormalizeToolArguments(t.toolNames[index], buffer.String(), t.tools)
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	return [][]byte{t.makeDeltaChunk(map[string]any{
		"tool_calls": []map[string]any{
			{
				"index": index,
				"function": map[string]any{
					"arguments": arguments,
				},
			},
		},
	}, nil, nil)}
}

func (t *StreamTranslator) flushAllToolBuffers() [][]byte {
	if len(t.toolBuffers) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(t.toolBuffers))
	for index := range t.toolBuffers {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	var out [][]byte
	for _, index := range indexes {
		out = append(out, t.flushToolBuffer(index)...)
	}
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
