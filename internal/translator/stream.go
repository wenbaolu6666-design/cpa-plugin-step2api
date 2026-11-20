package translator

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
	"github.com/google/uuid"
)

type StreamTranslator struct {
	ID                 string
	Created            int64
	Model              string
	SentRole           bool
	SentFinish         bool
	MessageID          string
	textBuffer         strings.Builder
	textStreamed       bool
	bufferText         bool
	hasToolCalls       bool
	tools              []any
	toolBuffers        map[int]*strings.Builder
	toolNames          map[int]string
	toolIndices        map[int]int
	nextToolIdx        int
	activeToolBlocks   map[int]bool
	completedToolCalls int
	suppressed         map[int]string
	mappedBuiltin      map[int]string
	toolIds            map[int]string
	dropped            []string
	cutReason          string
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

// Done reports whether a terminal chunk has already been emitted so the caller
// can immediately abort any subsequent server-side Studio agent loop turns.
func (t *StreamTranslator) Done() bool {
	return t.SentFinish
}

// CutDue reports that the client-facing turn is complete but the terminal
// frame has not been emitted yet, so the caller can capture usage first.
func (t *StreamTranslator) CutDue() bool {
	return t.cutReason != "" && !t.SentFinish
}

// CutReason returns the finish reason recorded at the cut point.
func (t *StreamTranslator) CutReason() string {
	return t.cutReason
}

// hasTool reports whether the client declared the named tool. Studio's own
// agent may call tools that exist only inside the Studio product
// (document_parser, python, todo, file_write); forwarding those to a client
// that never declared them makes the client reject the call.
func (t *StreamTranslator) hasTool(name string) bool {
	return ToolDeclared(t.tools, name)
}

// IsMappableBuiltin reports whether a built-in tool can be rewritten into a
// declared client tool (argument parsing happens when deltas arrive).
// MappedTarget reports the declared client tool that should receive a built-in
// Studio tool call, if any.
func MappedTarget(name string, tools []any) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "python", "file_write", "file_read", "read_file", "document_parser":
		if ToolDeclared(tools, "exec_command") {
			return "exec_command", true
		}
	case "todo", "todos", "todo_write":
		if ToolDeclared(tools, "update_plan") {
			return "update_plan", true
		}
	}
	return "", false
}

// IsMappableBuiltin reports whether a built-in tool can be rewritten into a
// declared client tool (argument parsing happens when deltas arrive).
func IsMappableBuiltin(name string, tools []any) bool {
	_, ok := MappedTarget(name, tools)
	return ok
}

// MapBuiltinTool rewrites a Studio built-in tool call into an equivalent
// client-declared tool call so the turn can continue instead of ending.
func MapBuiltinTool(name, args string, tools []any) (string, string, bool) {
	target, ok := MappedTarget(name, tools)
	if !ok {
		return "", "", false
	}
	var payload map[string]any
	if json.Unmarshal([]byte(args), &payload) != nil {
		return "", "", false
	}
	switch target {
	case "exec_command":
		cmd, okCmd := builtinToCommand(strings.ToLower(strings.TrimSpace(name)), payload)
		if !okCmd {
			return "", "", false
		}
		out, err := json.Marshal(map[string]any{"cmd": cmd})
		return target, string(out), err == nil
	case "update_plan":
		plan := builtinToPlan(payload)
		if len(plan) == 0 {
			return "", "", false
		}
		out, err := json.Marshal(map[string]any{"plan": plan})
		return target, string(out), err == nil
	}
	return "", "", false
}

func builtinToCommand(name string, payload map[string]any) (string, bool) {
	encode := func(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) }
	switch name {
	case "python":
		code := firstString(payload, "code", "source", "script")
		if strings.TrimSpace(code) == "" {
			return "", false
		}
		return "$c=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + encode(code) + "')); $c | python -", true
	case "file_write":
		path := firstString(payload, "path", "file_path", "filename", "file")
		content := firstString(payload, "content", "text", "data")
		if strings.TrimSpace(path) == "" {
			return "", false
		}
		return "$p=" + psQuote(path) + "; $c=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + encode(content) + "')); [IO.File]::WriteAllText($p,$c,(New-Object Text.UTF8Encoding($false)))", true
	default:
		path := firstString(payload, "path", "file_path", "filename", "file")
		if strings.TrimSpace(path) != "" {
			return "Get-Content -LiteralPath " + psQuote(path) + " -Raw", true
		}
		if url := firstString(payload, "url", "link"); strings.TrimSpace(url) != "" {
			return "Invoke-WebRequest -Uri " + psQuote(url) + " -UseBasicParsing | Select-Object -ExpandProperty Content", true
		}
	}
	return "", false
}

func builtinToPlan(payload map[string]any) []map[string]any {
	var items []any
	for _, key := range []string{"todos", "items", "tasks", "plan", "steps"} {
		if arr, ok := payload[key].([]any); ok {
			items = arr
			break
		}
	}
	var plan []map[string]any
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		step := firstString(m, "step", "content", "title", "text", "task", "name")
		if strings.TrimSpace(step) == "" {
			continue
		}
		plan = append(plan, map[string]any{
			"step":   step,
			"status": normalizeTodoStatus(firstString(m, "status", "state")),
		})
	}
	return plan
}

func normalizeTodoStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "done", "finished":
		return "completed"
	case "in_progress", "in-progress", "active", "doing", "running":
		return "in_progress"
	default:
		return "pending"
	}
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func psQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
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

func (t *StreamTranslator) shouldStopAfterCompletedToolCalls() bool {
	return t.completedToolCalls > 0 && len(t.activeToolBlocks) == 0
}

func (t *StreamTranslator) TranslateEvent(ev stepstudio.StudioEvent) [][]byte {
	if t.SentFinish || t.cutReason != "" {
		return nil
	}
	var out [][]byte

	switch ev.Type {
	case stepstudio.EventMessageStart:
		if t.shouldStopAfterCompletedToolCalls() {
			t.cutReason = "tool_calls"
			return nil
		}
		var data stepstudio.MessageStartData
		_ = json.Unmarshal(ev.Data, &data)
		t.MessageID = data.MessageID
		if r := t.RoleChunk(); r != nil {
			out = append(out, r)
		}

	case stepstudio.EventContentBlockStart:
		var data stepstudio.ContentBlockStartData
		_ = json.Unmarshal(ev.Data, &data)
		if data.BlockType == "CONTENT_BLOCK_TYPE_TOOL_CALL" {
			if t.activeToolBlocks == nil {
				t.activeToolBlocks = map[int]bool{}
			}
			t.activeToolBlocks[data.BlockIndex] = true
			if r := t.RoleChunk(); r != nil {
				out = append(out, r)
			}
			break
		}
		// Once a declared client tool call has finished its block, Studio's
		// server-side agent harness rejects the unknown tool internally and
		// immediately opens a new reasoning/text block inside the same SSE
		// stream. Cut the stream right here so the client executes the tool
		// instead of receiving Studio's bogus "tool unavailable" turn.
		if t.shouldStopAfterCompletedToolCalls() {
			t.cutReason = "tool_calls"
			return nil
		}
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
		if t.shouldStopAfterCompletedToolCalls() {
			t.cutReason = "tool_calls"
			return nil
		}
		var data stepstudio.DeltaData
		_ = json.Unmarshal(ev.Data, &data)
		if data.Delta != "" {
			frame := t.makeDeltaChunk(map[string]any{
				"reasoning_content": data.Delta,
			}, nil, nil)
			out = append(out, frame)
		}

	case stepstudio.EventTextDelta:
		if t.shouldStopAfterCompletedToolCalls() {
			t.cutReason = "tool_calls"
			return nil
		}
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
		if t.bufferText {
			if shouldBufferText(probe) || couldBeToolPrefix(probe) {
				break
			}
			t.bufferText = false
			t.textStreamed = true
			buffered := t.textBuffer.String()
			t.textBuffer.Reset()
			out = append(out, t.makeDeltaChunk(map[string]any{"content": buffered}, nil, nil))
			break
		}
		if shouldBufferText(probe) || couldBeToolPrefix(probe) {
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
		if !t.hasTool(data.ToolName) {
			if IsMappableBuiltin(data.ToolName, t.tools) {
				if t.mappedBuiltin == nil {
					t.mappedBuiltin = map[int]string{}
				}
				t.mappedBuiltin[data.BlockIndex] = data.ToolName
				if target, okTarget := MappedTarget(data.ToolName, t.tools); okTarget {
					data.ToolName = target
				}
			} else {
				delete(t.activeToolBlocks, data.BlockIndex)
				if t.suppressed == nil {
					t.suppressed = map[int]string{}
				}
				t.suppressed[data.BlockIndex] = data.ToolName
				t.dropped = append(t.dropped, data.ToolName)
				break
			}
		}
		if t.activeToolBlocks == nil {
			t.activeToolBlocks = map[int]bool{}
		}
		t.activeToolBlocks[data.BlockIndex] = true
		if _, isMapped := t.mappedBuiltin[data.BlockIndex]; !isMapped {
			t.hasToolCalls = true
		}
		t.textBuffer.Reset()
		t.bufferText = false
		t.ensureToolBuffers()
		if r := t.RoleChunk(); r != nil {
			out = append(out, r)
		}
		openAIIndex, exists := t.toolIndices[data.BlockIndex]
		if !exists {
			openAIIndex = t.nextToolIdx
			t.nextToolIdx++
			t.toolIndices[data.BlockIndex] = openAIIndex
		}
		t.toolBuffers[data.BlockIndex] = &strings.Builder{}
		t.toolBuffers[data.BlockIndex].WriteString(data.ArgumentsJSON)
		t.toolNames[data.BlockIndex] = data.ToolName
		if t.toolIds == nil {
			t.toolIds = map[int]string{}
		}
		t.toolIds[data.BlockIndex] = data.ToolCallID
		if _, isMapped := t.mappedBuiltin[data.BlockIndex]; !isMapped {
			frame := t.makeDeltaChunk(map[string]any{
				"tool_calls": []map[string]any{
					{
						"index": openAIIndex,
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
		}

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
		delete(t.activeToolBlocks, data.BlockIndex)
		if _, ok := t.suppressed[data.BlockIndex]; ok {
			delete(t.suppressed, data.BlockIndex)
			// Abort immediately before Studio executes its server-side sandbox
			// (/home/jovyan python, document_parser, etc.) and pollutes the turn.
			if !t.hasToolCalls && len(t.activeToolBlocks) == 0 {
				t.cutReason = "stop"
				return nil
			}
			break
		}
		if _, isTool := t.toolBuffers[data.BlockIndex]; isTool {
			out = append(out, t.flushToolBuffer(data.BlockIndex)...)
			t.completedToolCalls++
		}

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

func couldBeToolPrefix(text string) bool {
	for _, name := range []string{"exec_command", "update_plan", "write_stdin", "apply_patch", "view_image", "工具", "tool", "Tool"} {
		if strings.HasPrefix(name, text) || strings.HasPrefix(text, name) {
			return true
		}
	}
	return false
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
		if t.hasToolCalls {
			// Speculative text buffered prior to or around native tool calls is dropped.
		} else if call, ok := ParseToolCallTextWithTools(text, t.tools); ok && t.hasTool(call.Name) {
			t.hasToolCalls = true
			toolCall := OpenAIToolCall(call.Name, call.Arguments)
			toolCall["index"] = t.nextToolIdx
			t.nextToolIdx++
			out = append(out, t.makeDeltaChunk(map[string]any{
				"tool_calls": []map[string]any{toolCall},
			}, nil, nil))
		} else if call, ok := ParseToolCallText(text); ok && call.Name != "" && !t.hasTool(call.Name) {
			t.dropped = append(t.dropped, call.Name)
		} else {
			out = append(out, t.makeDeltaChunk(map[string]any{"content": text}, nil, nil))
		}
	}
	if len(t.dropped) > 0 && !t.hasToolCalls && t.textBuffer.Len() == 0 {
		seen := map[string]bool{}
		var names []string
		for _, name := range t.dropped {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
		if len(names) > 0 {
			notice := fmt.Sprintf("（上游尝试调用内置工具 %s，本环境没有这些工具，该调用已忽略。请改用当前会话声明的工具重试。）", strings.Join(names, ", "))
			if t.textStreamed {
				notice = "\n\n" + notice
			}
			out = append(out, t.makeDeltaChunk(map[string]any{"content": notice}, nil, nil))
		}
	}
	out = append(out, t.finishFrame(reason, usage)...)
	return out
}

func (t *StreamTranslator) ensureToolBuffers() {
	if t.toolBuffers == nil {
		t.toolBuffers = map[int]*strings.Builder{}
		t.toolNames = map[int]string{}
		t.toolIndices = map[int]int{}
	}
}

func (t *StreamTranslator) flushToolBuffer(blockIndex int) [][]byte {
	buffer, ok := t.toolBuffers[blockIndex]
	if !ok {
		return nil
	}
	delete(t.toolBuffers, blockIndex)
	rawArgs := buffer.String()
	mapped := false
	if original, okMap := t.mappedBuiltin[blockIndex]; okMap {
		_, mappedArgs, okConv := MapBuiltinTool(original, rawArgs, t.tools)
		if !okConv {
			t.dropped = append(t.dropped, original)
			return nil
		}
		rawArgs = mappedArgs
		mapped = true
	}
	arguments := NormalizeToolArguments(t.toolNames[blockIndex], rawArgs, t.tools)
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	openAIIndex := blockIndex
	if idx, exists := t.toolIndices[blockIndex]; exists {
		openAIIndex = idx
	}
	call := map[string]any{
		"index": openAIIndex,
		"function": map[string]any{
			"arguments": arguments,
		},
	}
	if mapped {
		call["id"] = t.toolIds[blockIndex]
		call["type"] = "function"
		call["function"].(map[string]any)["name"] = t.toolNames[blockIndex]
		t.hasToolCalls = true
	}
	return [][]byte{t.makeDeltaChunk(map[string]any{"tool_calls": []map[string]any{call}}, nil, nil)}
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

// UsageMap exposes the OpenAI-style usage map for a Studio usage record.
func UsageMap(usage *stepstudio.UsageInfo) map[string]any {
	return usageMapFrom(usage)
}
