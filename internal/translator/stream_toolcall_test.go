package translator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
)

func TestStreamTranslatorConvertsTextJSONToToolCall(t *testing.T) {
	tr := NewStreamTranslator("step-5-preview")
	tr.SetTools([]any{map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}})
	for _, delta := range []string{`{"name":"get_weather",`, `"arguments":{"city":"北京"}}`} {
		chunks := tr.TranslateEvent(stepstudio.StudioEvent{
			Type: stepstudio.EventTextDelta,
			Data: []byte(`{"delta":` + quoteJSON(delta) + `}`),
		})
		if len(chunks) != 0 {
			t.Fatalf("tool JSON must be buffered, got %q", string(chunks[0]))
		}
	}

	chunks := tr.FinalChunks("stop", nil)
	if len(chunks) != 2 {
		t.Fatalf("final chunks = %d, want 2", len(chunks))
	}
	toolFrame := string(chunks[0])
	for _, want := range []string{`"tool_calls"`, `"name":"get_weather"`, `"arguments":"{\"city\":\"北京\"}"`} {
		if !strings.Contains(toolFrame, want) {
			t.Errorf("tool frame missing %s: %s", want, toolFrame)
		}
	}
	if strings.Contains(toolFrame, `"content":"{`) {
		t.Errorf("tool JSON leaked as content: %s", toolFrame)
	}
	if finish := string(chunks[1]); !strings.Contains(finish, `"finish_reason":"tool_calls"`) {
		t.Errorf("unexpected finish frame: %s", finish)
	}
}

func TestStreamTranslatorNormalizesNativeToolArguments(t *testing.T) {
	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "update_plan",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"plan": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"status": map[string]any{"type": "string"},
								"step":   map[string]any{"type": "string"},
							},
						},
					},
				},
			},
		},
	}}
	tr := NewStreamTranslator("step-5-preview")
	tr.SetTools(tools)

	start, _ := json.Marshal(map[string]any{
		"block_index": 0, "tool_call_id": "call_1", "tool_name": "update_plan", "arguments_json": "",
	})
	delta, _ := json.Marshal(map[string]any{
		"block_index": 0, "progress_text": `{"plan":"[{\"status\":\"in_progress\",\"step\":\"读取文件\"}]"}`,
	})
	stop, _ := json.Marshal(map[string]any{"block_index": 0})

	tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventToolCallStart, Data: start})
	tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventToolCallDelta, Data: delta})
	chunks := tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventContentBlockStop, Data: stop})
	if len(chunks) != 1 {
		t.Fatalf("expected one normalized tool chunk, got %d", len(chunks))
	}
	frame := string(chunks[0])
	if !strings.Contains(frame, `\"plan\":[{\"status\":\"in_progress\"`) {
		t.Fatalf("array argument was not normalized: %s", frame)
	}
}

func TestStreamTranslatorDropsPreToolUnavailableText(t *testing.T) {
	tr := NewStreamTranslator("step-5-preview")
	tr.SetTools([]any{map[string]any{"type": "function", "function": map[string]any{"name": "exec_command"}}})
	chunks := tr.TranslateEvent(stepstudio.StudioEvent{
		Type: stepstudio.EventTextDelta,
		Data: []byte(`{"delta":"` + "`exec_command` 这次调用不可用" + `"}`),
	})
	if len(chunks) != 0 {
		t.Fatalf("pre-tool unavailable text must be buffered: %q", chunks)
	}

	start, _ := json.Marshal(map[string]any{
		"block_index": 0, "tool_call_id": "call_1", "tool_name": "exec_command", "arguments_json": "",
	})
	delta, _ := json.Marshal(map[string]any{"block_index": 0, "progress_text": `{"cmd":"pwd"}`})
	stop, _ := json.Marshal(map[string]any{"block_index": 0})
	chunks = tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventToolCallStart, Data: start})
	chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventToolCallDelta, Data: delta})...)
	chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventContentBlockStop, Data: stop})...)
	for _, chunk := range chunks {
		if strings.Contains(string(chunk), "不可用") {
			t.Fatalf("pre-tool unavailable text leaked: %s", chunk)
		}
	}
}

func TestStreamTranslatorStillStreamsNormalText(t *testing.T) {
	tr := NewStreamTranslator("step-5-preview")
	chunks := tr.TranslateEvent(stepstudio.StudioEvent{
		Type: stepstudio.EventTextDelta,
		Data: []byte(`{"delta":"你好"}`),
	})
	if len(chunks) != 1 || !strings.Contains(string(chunks[0]), `"content":"你好"`) {
		t.Fatalf("normal text should stream immediately: %q", chunks)
	}
}

func quoteJSON(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + replacer.Replace(value) + `"`
}

func TestStreamTranslatorDropsUndeclaredNativeToolCalls(t *testing.T) {
	declared := []any{map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "exec_command"},
	}}
	tr := NewStreamTranslator("step-5-preview")
	tr.SetTools(declared)

	start, _ := json.Marshal(map[string]any{
		"block_index": 0, "tool_call_id": "call_1", "tool_name": "python", "arguments_json": "",
	})
	delta, _ := json.Marshal(map[string]any{"block_index": 0, "progress_text": `{"code":"print(1)"}`})
	stop, _ := json.Marshal(map[string]any{"block_index": 0})

	var chunks [][]byte
	chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventToolCallStart, Data: start})...)
	chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventToolCallDelta, Data: delta})...)
	chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventContentBlockStop, Data: stop})...)
	chunks = append(chunks, tr.FinalChunks("stop", nil)...)

	var joined strings.Builder
	for _, chunk := range chunks {
		joined.Write(chunk)
	}
	all := joined.String()
	if strings.Contains(all, "tool_calls") {
		t.Fatalf("undeclared tool call leaked to client: %s", all)
	}
	if !strings.Contains(all, "python") {
		t.Fatalf("expected a notice naming the dropped tool: %s", all)
	}
}
func TestStreamTranslatorDropsUnavailableTextSplitAcrossDeltas(t *testing.T) {
	tr := NewStreamTranslator("step-5-preview")
	tr.SetTools([]any{map[string]any{"type": "function", "function": map[string]any{"name": "exec_command"}}})

	var chunks [][]byte
	for _, delta := range []string{"e", "xec_command", " 工具在当前运行时不可用。"} {
		data, _ := json.Marshal(map[string]any{"delta": delta})
		chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventTextDelta, Data: data})...)
	}

	start, _ := json.Marshal(map[string]any{
		"block_index": 0, "tool_call_id": "call_1", "tool_name": "exec_command", "arguments_json": "",
	})
	delta, _ := json.Marshal(map[string]any{"block_index": 0, "progress_text": `{"cmd":"dir"}`})
	stop, _ := json.Marshal(map[string]any{"block_index": 0})
	chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventToolCallStart, Data: start})...)
	chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventToolCallDelta, Data: delta})...)
	chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: stepstudio.EventContentBlockStop, Data: stop})...)
	chunks = append(chunks, tr.FinalChunks("stop", nil)...)

	for _, chunk := range chunks {
		if strings.Contains(string(chunk), "不可用") {
			t.Fatalf("unavailable text leaked: %s", chunk)
		}
	}
}
func TestStreamTranslatorCutsStudioPostToolServerLoopAfterPreamble(t *testing.T) {
	declared := []any{map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "exec_command"},
	}}
	tr := NewStreamTranslator("step-5-preview")
	tr.SetTools(declared)

	var chunks [][]byte
	emit := func(evType string, payload map[string]any) {
		raw, _ := json.Marshal(payload)
		chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: evType, Data: raw})...)
	}

	// Block 0: reasoning
	emit(stepstudio.EventContentBlockStart, map[string]any{"block_index": 0, "block_type": "CONTENT_BLOCK_TYPE_REASONING"})
	emit(stepstudio.EventReasoningDelta, map[string]any{"block_index": 0, "delta": "thinking step 1"})
	emit(stepstudio.EventContentBlockStop, map[string]any{"block_index": 0})
	// Block 1: preamble text (sets textStreamed = true)
	emit(stepstudio.EventContentBlockStart, map[string]any{"block_index": 1, "block_type": "CONTENT_BLOCK_TYPE_TEXT"})
	emit(stepstudio.EventTextDelta, map[string]any{"block_index": 1, "delta": "我先查看一下当前目录下的文件。"})
	emit(stepstudio.EventContentBlockStop, map[string]any{"block_index": 1})
	// Block 2: declared native tool call
	emit(stepstudio.EventContentBlockStart, map[string]any{"block_index": 2, "block_type": "CONTENT_BLOCK_TYPE_TOOL_CALL", "tool_call_id": "call_abc"})
	emit(stepstudio.EventToolCallStart, map[string]any{"block_index": 2, "tool_call_id": "call_abc", "tool_name": "exec_command", "arguments_json": ""})
	emit(stepstudio.EventToolCallDelta, map[string]any{"block_index": 2, "progress_text": `{"cmd":"dir"}`})
	emit(stepstudio.EventContentBlockStop, map[string]any{"block_index": 2})

	if tr.Done() {
		t.Fatal("should allow potential parallel tool_call block before closing")
	}

	// Block 3: Studio server-side harness rejects exec_command and starts Step 2 reasoning
	emit(stepstudio.EventContentBlockStart, map[string]any{"block_index": 3, "block_type": "CONTENT_BLOCK_TYPE_REASONING"})
	if !tr.CutDue() {
		t.Fatal("translator must mark Done() immediately when post-tool reasoning starts")
	}

	// Subsequent Studio Step 2 error text must be completely ignored
	emit(stepstudio.EventReasoningDelta, map[string]any{"block_index": 3, "delta": "This tool is not available"})
	emit(stepstudio.EventContentBlockStart, map[string]any{"block_index": 4, "block_type": "CONTENT_BLOCK_TYPE_TEXT"})
	emit(stepstudio.EventTextDelta, map[string]any{"block_index": 4, "delta": "抱歉，`exec_command` 工具在当前运行环境中不可用。"})

	chunks = append(chunks, tr.FinalChunks(tr.CutReason(), nil)...)
	var joined strings.Builder
	for _, c := range chunks {
		joined.Write(c)
	}
	all := joined.String()
	if !strings.Contains(all, "我先查看一下当前目录下的文件。") {
		t.Fatalf("expected preamble to be preserved: %s", all)
	}
	if !strings.Contains(all, `"index":0`) || strings.Contains(all, `"index":2`) {
		t.Fatalf("expected sequential 0-based tool_calls index, got: %s", all)
	}
	if !strings.Contains(all, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected finish_reason=tool_calls, got: %s", all)
	}
	if strings.Contains(all, "不可用") || strings.Contains(all, "not available") {
		t.Fatalf("post-tool server error leaked: %s", all)
	}
}

func TestStreamTranslatorSupportsParallelDeclaredToolCallsWithSequentialIndices(t *testing.T) {
	declared := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "update_plan"}},
		map[string]any{"type": "function", "function": map[string]any{"name": "exec_command"}},
	}
	tr := NewStreamTranslator("step-5-preview")
	tr.SetTools(declared)

	var chunks [][]byte
	emit := func(evType string, payload map[string]any) {
		raw, _ := json.Marshal(payload)
		chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: evType, Data: raw})...)
	}

	emit(stepstudio.EventContentBlockStart, map[string]any{"block_index": 2, "block_type": "CONTENT_BLOCK_TYPE_TOOL_CALL"})
	emit(stepstudio.EventToolCallStart, map[string]any{"block_index": 2, "tool_call_id": "call_plan", "tool_name": "update_plan", "arguments_json": ""})
	emit(stepstudio.EventToolCallDelta, map[string]any{"block_index": 2, "progress_text": `{"step":"a"}`})
	emit(stepstudio.EventContentBlockStop, map[string]any{"block_index": 2})

	emit(stepstudio.EventContentBlockStart, map[string]any{"block_index": 3, "block_type": "CONTENT_BLOCK_TYPE_TOOL_CALL"})
	emit(stepstudio.EventToolCallStart, map[string]any{"block_index": 3, "tool_call_id": "call_exec", "tool_name": "exec_command", "arguments_json": ""})
	emit(stepstudio.EventToolCallDelta, map[string]any{"block_index": 3, "progress_text": `{"cmd":"pwd"}`})
	emit(stepstudio.EventContentBlockStop, map[string]any{"block_index": 3})

	emit(stepstudio.EventContentBlockStart, map[string]any{"block_index": 5, "block_type": "CONTENT_BLOCK_TYPE_REASONING"})

	var joined strings.Builder
	for _, c := range chunks {
		joined.Write(c)
	}
	all := joined.String()
	if !strings.Contains(all, `"index":0`) || !strings.Contains(all, `"index":1`) {
		t.Fatalf("expected both parallel tool calls with indices 0 and 1: %s", all)
	}
	if !tr.CutDue() {
		t.Fatal("expected Done() after parallel tool calls finish and reasoning starts")
	}
}

func TestStreamTranslatorAbortsImmediatelyOnUndeclaredBuiltinToolEvenAfterPreamble(t *testing.T) {
	declared := []any{map[string]any{"type": "function", "function": map[string]any{"name": "exec_command"}}}
	tr := NewStreamTranslator("step-5-preview")
	tr.SetTools(declared)

	var chunks [][]byte
	emit := func(evType string, payload map[string]any) {
		raw, _ := json.Marshal(payload)
		chunks = append(chunks, tr.TranslateEvent(stepstudio.StudioEvent{Type: evType, Data: raw})...)
	}

	emit(stepstudio.EventContentBlockStart, map[string]any{"block_index": 1, "block_type": "CONTENT_BLOCK_TYPE_TEXT"})
	emit(stepstudio.EventTextDelta, map[string]any{"block_index": 1, "delta": "我先确认目录。"})
	emit(stepstudio.EventContentBlockStop, map[string]any{"block_index": 1})

	emit(stepstudio.EventContentBlockStart, map[string]any{"block_index": 2, "block_type": "CONTENT_BLOCK_TYPE_TOOL_CALL"})
	emit(stepstudio.EventToolCallStart, map[string]any{"block_index": 2, "tool_call_id": "call_py", "tool_name": "python", "arguments_json": ""})
	emit(stepstudio.EventToolCallDelta, map[string]any{"block_index": 2, "progress_text": `{"code":"import os"}`})
	emit(stepstudio.EventContentBlockStop, map[string]any{"block_index": 2})

	if !tr.CutDue() {
		t.Fatal("expected Done() immediately on undeclared tool ContentBlockStop before /home/jovyan runs")
	}

	// Simulate /home/jovyan post-execution message that must be ignored
	emit(stepstudio.EventContentBlockStart, map[string]any{"block_index": 4, "block_type": "CONTENT_BLOCK_TYPE_TEXT"})
	emit(stepstudio.EventTextDelta, map[string]any{"block_index": 4, "delta": "当前处于 /home/jovyan Linux 沙箱"})

	chunks = append(chunks, tr.FinalChunks(tr.CutReason(), nil)...)
	var joined strings.Builder
	for _, c := range chunks {
		joined.Write(c)
	}
	all := joined.String()
	if !strings.Contains(all, "python") {
		t.Fatalf("expected notice naming python even after preamble: %s", all)
	}
	if strings.Contains(all, "/home/jovyan") {
		t.Fatalf("/home/jovyan output leaked: %s", all)
	}
}
