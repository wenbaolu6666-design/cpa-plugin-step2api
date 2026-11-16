package translator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
)

func TestStreamTranslatorConvertsTextJSONToToolCall(t *testing.T) {
	tr := NewStreamTranslator("step-5-preview")
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
