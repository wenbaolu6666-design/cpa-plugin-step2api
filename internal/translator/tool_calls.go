package translator

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// ParsedToolCall is the normalized form of a tool call emitted as JSON text.
type ParsedToolCall struct {
	Name      string
	Arguments string
}

// ParseToolCallText accepts the compact protocol used by the Studio prompt
// bridge and the common OpenAI-shaped variants. It returns false for prose or
// incomplete JSON so callers can preserve normal text responses.
func ParseToolCallText(text string) (ParsedToolCall, bool) {
	candidate := stripCodeFence(strings.TrimSpace(text))
	start := strings.Index(candidate, "{")
	end := strings.LastIndex(candidate, "}")
	if start < 0 || end <= start {
		return ParsedToolCall{}, false
	}
	candidate = candidate[start : end+1]

	var payload struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Function  *struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
		ToolCalls []struct {
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(candidate), &payload); err != nil {
		return ParsedToolCall{}, false
	}

	name := strings.TrimSpace(payload.Name)
	args := payload.Arguments
	if payload.Function != nil {
		if name == "" {
			name = strings.TrimSpace(payload.Function.Name)
		}
		if len(args) == 0 {
			args = payload.Function.Arguments
		}
	}
	if name == "" && len(payload.ToolCalls) == 1 {
		name = strings.TrimSpace(payload.ToolCalls[0].Function.Name)
		args = payload.ToolCalls[0].Function.Arguments
	}
	if name == "" {
		return ParsedToolCall{}, false
	}

	arguments := strings.TrimSpace(string(args))
	if arguments == "" || arguments == "null" {
		arguments = "{}"
	} else if strings.HasPrefix(arguments, "\"") {
		var decoded string
		if err := json.Unmarshal(args, &decoded); err == nil {
			arguments = strings.TrimSpace(decoded)
		}
	}
	if arguments == "" {
		arguments = "{}"
	}
	if !json.Valid([]byte(arguments)) {
		return ParsedToolCall{}, false
	}
	return ParsedToolCall{Name: name, Arguments: arguments}, true
}

// OpenAIToolCall builds one OpenAI-compatible tool call object.
func OpenAIToolCall(name, arguments string) map[string]any {
	return map[string]any{
		"id":   "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24],
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}
}

func stripCodeFence(value string) string {
	if !strings.HasPrefix(value, "```") {
		return value
	}
	lines := strings.Split(value, "\n")
	if len(lines) >= 2 && strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		lines = lines[1:]
	}
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "```" {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
