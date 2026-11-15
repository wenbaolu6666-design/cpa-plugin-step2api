package translator

import (
	"bytes"
	"encoding/json"
	"strconv"
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
	return ParseToolCallTextWithTools(text, nil)
}

// ParseToolCallTextWithTools is ParseToolCallText plus schema-aware coercion of
// stringified numbers, objects and arrays in the tool arguments.
func ParseToolCallTextWithTools(text string, tools []any) (ParsedToolCall, bool) {
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
	arguments = NormalizeToolArguments(name, arguments, tools)
	return ParsedToolCall{Name: name, Arguments: arguments}, true
}

// NormalizeToolArguments coerces values that the model encoded as JSON strings
// back to the types declared by the tool schema. Unknown or string-typed fields
// are left untouched.
func NormalizeToolArguments(name, arguments string, tools []any) string {
	parameters := toolParameters(tools, name)
	if parameters == nil || strings.TrimSpace(arguments) == "" {
		return arguments
	}
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return arguments
	}
	coerced := coerceJSONValue(object, parameters)
	encoded, err := marshalJSON(coerced)
	if err != nil {
		return arguments
	}
	return encoded
}

func toolParameters(tools []any, name string) map[string]any {
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := tool["function"].(map[string]any)
		gotName, _ := fn["name"].(string)
		if gotName == "" {
			gotName, _ = tool["name"].(string)
		}
		if gotName != name {
			continue
		}
		if params, ok := fn["parameters"].(map[string]any); ok {
			return params
		}
		if params, ok := tool["parameters"].(map[string]any); ok {
			return params
		}
	}
	return nil
}

func coerceJSONValue(value any, schema map[string]any) any {
	schema = effectiveSchema(schema)
	switch typed := value.(type) {
	case string:
		return coerceString(typed, schema)
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		for key, item := range typed {
			property, _ := properties[key].(map[string]any)
			if property == nil {
				continue
			}
			typed[key] = coerceJSONValue(item, property)
		}
		return typed
	case []any:
		items, _ := schema["items"].(map[string]any)
		if items == nil {
			return typed
		}
		for index, item := range typed {
			typed[index] = coerceJSONValue(item, items)
		}
		return typed
	default:
		return value
	}
}

func coerceString(value string, schema map[string]any) any {
	trimmed := strings.TrimSpace(value)
	switch schemaType(schema) {
	case "object":
		if strings.HasPrefix(trimmed, "{") {
			var object map[string]any
			if json.Unmarshal([]byte(trimmed), &object) == nil {
				return coerceJSONValue(object, schema)
			}
		}
	case "array":
		if strings.HasPrefix(trimmed, "[") {
			var array []any
			if json.Unmarshal([]byte(trimmed), &array) == nil {
				return coerceJSONValue(array, schema)
			}
		}
	case "integer":
		if number, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return number
		}
		if number, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return int64(number)
		}
	case "number":
		if number, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return number
		}
	case "boolean":
		if boolean, err := strconv.ParseBool(trimmed); err == nil {
			return boolean
		}
	}
	return value
}

func effectiveSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	if schemaType(schema) != "" {
		return schema
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		options, _ := schema[key].([]any)
		for _, option := range options {
			if candidate, ok := option.(map[string]any); ok && schemaType(candidate) != "" {
				return candidate
			}
		}
	}
	return schema
}

func schemaType(schema map[string]any) string {
	if schema == nil {
		return ""
	}
	switch typed := schema["type"].(type) {
	case string:
		return typed
	case []any:
		for _, item := range typed {
			if name, ok := item.(string); ok && name != "null" {
				return name
			}
		}
	}
	return ""
}

func marshalJSON(value any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSpace(buffer.String()), nil
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
