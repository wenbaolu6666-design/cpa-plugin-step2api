package translator

import (
	"encoding/json"
	"testing"
)

func TestParseToolCallTextWithToolsCoercesStringifiedArray(t *testing.T) {
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
	text := `{"name":"update_plan","arguments":{"plan":"[{\"status\":\"in_progress\",\"step\":\"读取笔记\"}]"}}`
	call, ok := ParseToolCallTextWithTools(text, tools)
	if !ok {
		t.Fatal("expected tool call")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	plan, ok := args["plan"].([]any)
	if !ok || len(plan) != 1 {
		t.Fatalf("plan was not coerced to array: %#v", args["plan"])
	}
}

func TestParseToolCallTextWithToolsCoercesStringifiedNumber(t *testing.T) {
	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "exec_command",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cmd":           map[string]any{"type": "string"},
					"yield_time_ms": map[string]any{"type": "integer"},
				},
			},
		},
	}}
	text := `{"name":"exec_command","arguments":{"cmd":"pwd","yield_time_ms":"15000"}}`
	call, ok := ParseToolCallTextWithTools(text, tools)
	if !ok {
		t.Fatal("expected tool call")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if number, ok := args["yield_time_ms"].(float64); !ok || number != 15000 {
		t.Fatalf("yield_time_ms was not coerced to number: %#v", args["yield_time_ms"])
	}
}

func TestParseToolCallTextWithToolsKeepsStringJSON(t *testing.T) {
	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "write_file",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{"type": "string"},
				},
			},
		},
	}}
	text := `{"name":"write_file","arguments":{"content":"{\"ok\":true}"}}`
	call, ok := ParseToolCallTextWithTools(text, tools)
	if !ok {
		t.Fatal("expected tool call")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if _, ok := args["content"].(string); !ok {
		t.Fatalf("string content was unexpectedly coerced: %#v", args["content"])
	}
}
