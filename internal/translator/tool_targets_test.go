package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

func toolWithSchema(name string, params map[string]any) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":       name,
			"parameters": params,
		},
	}
}

func bashTools() []any {
	return []any{toolWithSchema("bash", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
		},
	})}
}

func argvShellTools() []any {
	return []any{toolWithSchema("shell", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	})}
}

func TestMapBuiltinPythonToBash(t *testing.T) {
	name, args, ok := MapBuiltinTool("python", `{"code":"print(1+1)"}`, bashTools())
	if !ok || name != "bash" {
		t.Fatalf("expected bash mapping, got %q %q %v", name, args, ok)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(args), &payload); err != nil {
		t.Fatalf("bad args %q: %v", args, err)
	}
	command, _ := payload["command"].(string)
	if !strings.HasPrefix(command, "python -c \"import base64;exec(") {
		t.Fatalf("unexpected portable command: %q", command)
	}
	if _, hasCmd := payload["cmd"]; hasCmd {
		t.Fatalf("bash must receive command, got %q", args)
	}
}

func TestMapBuiltinPythonToArgvShell(t *testing.T) {
	name, args, ok := MapBuiltinTool("python", `{"code":"print(1+1)"}`, argvShellTools())
	if !ok || name != "shell" {
		t.Fatalf("expected shell mapping, got %q %q %v", name, args, ok)
	}
	var payload struct {
		Command []string `json:"command"`
	}
	if err := json.Unmarshal([]byte(args), &payload); err != nil {
		t.Fatalf("bad args %q: %v", args, err)
	}
	if len(payload.Command) != 3 || payload.Command[0] != "python" || payload.Command[1] != "-c" || payload.Command[2] != "print(1+1)" {
		t.Fatalf("unexpected argv: %v", payload.Command)
	}
}

func TestMapBuiltinFileWriteToExecuteCommand(t *testing.T) {
	tools := []any{toolWithSchema("execute_command", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
		},
	})}
	name, args, ok := MapBuiltinTool("file_write", `{"path":"C:\\tmp\\a.txt","content":"hello"}`, tools)
	if !ok || name != "execute_command" {
		t.Fatalf("expected execute_command mapping, got %q %q %v", name, args, ok)
	}
	if !strings.Contains(args, "pathlib") || !strings.Contains(args, b64(`C:\tmp\a.txt`)) {
		t.Fatalf("path or writer missing: %s", args)
	}
}

func TestFallbackBuiltinToolUsesDeclaredShell(t *testing.T) {
	name, args, ok := FallbackBuiltinTool("document_parser", bashTools())
	if !ok || name != "bash" {
		t.Fatalf("expected bash fallback, got %q %q %v", name, args, ok)
	}
	if !strings.Contains(args, "document_parser") {
		t.Fatalf("fallback must name the builtin: %s", args)
	}
}

func TestMapBuiltinTodoToTodoWrite(t *testing.T) {
	tools := []any{toolWithSchema("todo_write", map[string]any{"type": "object"})}
	name, args, ok := MapBuiltinTool("todo", `{"todos":[{"content":"a","status":"done"}]}`, tools)
	if !ok || name != "todo_write" {
		t.Fatalf("expected todo_write mapping, got %q %q %v", name, args, ok)
	}
	if !strings.Contains(args, "completed") {
		t.Fatalf("unexpected plan args: %s", args)
	}
}

func TestResolveCommandTargetFromSchemaDescription(t *testing.T) {
	tools := []any{map[string]any{
		"type":        "function",
		"name":        "run_local",
		"description": "Run a shell command on the host",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"script": map[string]any{"type": "string"},
			},
		},
	}}
	name, args, ok := MapBuiltinTool("python", `{"code":"print(1)"}`, tools)
	if !ok || name != "run_local" {
		t.Fatalf("expected schema-detected mapping, got %q %q %v", name, args, ok)
	}
	if !strings.Contains(args, "\"script\"") {
		t.Fatalf("expected script argument: %s", args)
	}
}

func TestDroppedBuiltinNoticeListsDeclaredTools(t *testing.T) {
	notice := DroppedBuiltinNotice([]string{"python"}, []any{toolWithSchema("read_file", nil)})
	if !strings.Contains(notice, "python") || !strings.Contains(notice, "read_file") {
		t.Fatalf("notice must name builtin and declared tools: %s", notice)
	}
}
