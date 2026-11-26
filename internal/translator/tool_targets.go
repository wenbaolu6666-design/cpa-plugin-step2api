package translator

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// shellFlavor describes the quoting rules of the shell that a client tool
// hands the command to. Codex wraps exec_command in PowerShell, so commands
// for it must not contain double quotes or dollar signs; other clients are
// assumed to accept a portable one-liner (cmd, PowerShell and POSIX shells).
type shellFlavor int

const (
	shellPortable shellFlavor = iota
	shellPowerShell
)

// commandTarget is the client-declared tool that executes local commands.
type commandTarget struct {
	Name     string
	ArgKey   string
	ArgArray bool
	Flavor   shellFlavor
}

// commandToolPreference is ordered by how well the argument shape is known.
var commandToolPreference = []string{
	"exec_command",
	"bash",
	"shell",
	"execute_command",
	"run_command",
	"run_terminal_cmd",
	"run_terminal_command",
	"run_shell_command",
	"execute_shell",
	"run_in_terminal",
	"terminal",
	"powershell",
	"pwsh",
	"cmd",
	"exec",
}

var commandToolFlavor = map[string]shellFlavor{
	"exec_command":         shellPowerShell,
	"powershell":           shellPowerShell,
	"pwsh":                 shellPowerShell,
	"bash":                 shellPortable,
	"shell":                shellPortable,
	"execute_command":      shellPortable,
	"run_command":          shellPortable,
	"run_terminal_cmd":     shellPortable,
	"run_terminal_command": shellPortable,
	"run_shell_command":    shellPortable,
	"execute_shell":        shellPortable,
	"run_in_terminal":      shellPortable,
	"terminal":             shellPortable,
	"cmd":                  shellPortable,
	"exec":                 shellPortable,
}

// planToolPreference lists client plan/todo tools that can receive a Studio
// built-in todo call.
var planToolPreference = []string{
	"update_plan",
	"update_todos",
	"todo_write",
	"write_todos",
	"todos",
	"plan",
}

var shellArgKeys = []string{"command", "cmd", "script", "command_line", "shell_command", "code"}

var shellNameHints = []string{"shell", "bash", "terminal", "command", "exec", "powershell", "pwsh", "cmd"}

var nonShellNameHints = []string{"plan", "todo", "read", "write", "edit", "patch", "search", "image", "browser", "web", "fetch"}

func isCommandBuiltin(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "python", "file_write", "file_read", "read_file", "document_parser":
		return true
	}
	return false
}

func isPlanBuiltin(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "todo", "todos", "todo_write":
		return true
	}
	return false
}

func toolFunctionName(tool map[string]any) string {
	if fn, ok := tool["function"].(map[string]any); ok {
		if name, _ := fn["name"].(string); strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	if name, _ := tool["name"].(string); strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return ""
}

func toolFunctionDescription(tool map[string]any) string {
	var parts []string
	if text, _ := tool["description"].(string); strings.TrimSpace(text) != "" {
		parts = append(parts, text)
	}
	if fn, ok := tool["function"].(map[string]any); ok {
		if text, _ := fn["description"].(string); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func toolFunctionParams(tool map[string]any) map[string]any {
	if fn, ok := tool["function"].(map[string]any); ok {
		if params, ok := fn["parameters"].(map[string]any); ok {
			return params
		}
	}
	if params, ok := tool["parameters"].(map[string]any); ok {
		return params
	}
	if params, ok := tool["input_schema"].(map[string]any); ok {
		return params
	}
	return nil
}

func toolFunctionProperties(tool map[string]any) map[string]any {
	properties, _ := toolFunctionParams(tool)["properties"].(map[string]any)
	return properties
}

// schemaCommandArg picks the argument that carries the command text and reports
// whether the schema expects an argv array instead of a single line.
func schemaCommandArg(tool map[string]any, preferred []string) (string, bool, bool) {
	properties := toolFunctionProperties(tool)
	if properties == nil {
		return "", false, false
	}
	for _, key := range preferred {
		property, _ := properties[key].(map[string]any)
		if property == nil {
			continue
		}
		return key, schemaType(property) == "array", true
	}
	return "", false, false
}

func commandTargetFromTool(tool map[string]any, lowerName string) (commandTarget, bool) {
	name := toolFunctionName(tool)
	if name == "" {
		return commandTarget{}, false
	}
	preferred := shellArgKeys
	if lowerName == "exec_command" {
		preferred = []string{"cmd", "command", "script"}
	}
	flavor, known := commandToolFlavor[lowerName]
	if !known {
		flavor = shellPortable
	}
	key, isArray, okArg := schemaCommandArg(tool, preferred)
	if !okArg {
		if !known {
			return commandTarget{}, false
		}
		key = "command"
		if lowerName == "exec_command" {
			key = "cmd"
		}
		return commandTarget{Name: name, ArgKey: key, Flavor: flavor}, true
	}
	return commandTarget{Name: name, ArgKey: key, ArgArray: isArray, Flavor: flavor}, true
}

func looksLikeShellTool(lowerName string, tool map[string]any) bool {
	for _, hint := range nonShellNameHints {
		if strings.Contains(lowerName, hint) {
			return false
		}
	}
	haystack := lowerName + " " + toolFunctionDescription(tool)
	matched := false
	for _, hint := range shellNameHints {
		if strings.Contains(haystack, hint) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	if _, _, okArg := schemaCommandArg(tool, shellArgKeys); !okArg {
		return false
	}
	return true
}

// resolveCommandTarget finds the declared tool that can run a local command.
func resolveCommandTarget(tools []any) (commandTarget, bool) {
	byName := map[string]map[string]any{}
	var generic []map[string]any
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := toolFunctionName(tool)
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		if _, exists := byName[lower]; !exists {
			byName[lower] = tool
		}
		generic = append(generic, tool)
	}
	for _, name := range commandToolPreference {
		tool, ok := byName[name]
		if !ok {
			continue
		}
		if target, okTarget := commandTargetFromTool(tool, name); okTarget {
			return target, true
		}
	}
	for _, tool := range generic {
		lower := strings.ToLower(toolFunctionName(tool))
		if !looksLikeShellTool(lower, tool) {
			continue
		}
		if target, okTarget := commandTargetFromTool(tool, lower); okTarget {
			return target, true
		}
	}
	return commandTarget{}, false
}

// resolvePlanTarget finds the declared plan/todo tool.
func resolvePlanTarget(tools []any) (string, bool) {
	byName := map[string]string{}
	var declared []string
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := toolFunctionName(tool)
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		if _, exists := byName[lower]; !exists {
			byName[lower] = name
			declared = append(declared, lower)
		}
	}
	for _, name := range planToolPreference {
		if original, ok := byName[name]; ok {
			return original, true
		}
	}
	for _, lower := range declared {
		if strings.Contains(lower, "plan") || strings.Contains(lower, "todo") {
			return byName[lower], true
		}
	}
	return "", false
}

// DeclaredToolNames lists client tool names for diagnostics.
func DeclaredToolNames(tools []any) []string {
	var names []string
	seen := map[string]bool{}
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := toolFunctionName(tool)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func encodeCommandArgs(target commandTarget, shellCommand string, argv []string) (string, bool) {
	var payload map[string]any
	if target.ArgArray {
		if len(argv) == 0 {
			return "", false
		}
		payload = map[string]any{target.ArgKey: argv}
	} else {
		if strings.TrimSpace(shellCommand) == "" {
			return "", false
		}
		payload = map[string]any{target.ArgKey: shellCommand}
	}
	out, err := json.Marshal(payload)
	return string(out), err == nil
}

func b64(text string) string {
	return base64.StdEncoding.EncodeToString([]byte(text))
}

func pythonWriteScript(path, content string) string {
	return fmt.Sprintf("import base64,pathlib;pathlib.Path(base64.b64decode('%s').decode('utf-8')).write_bytes(base64.b64decode('%s'))", b64(path), b64(content))
}

func pythonReadScript(path string) string {
	return fmt.Sprintf("import base64,sys;sys.stdout.write(open(base64.b64decode('%s').decode('utf-8'),encoding='utf-8',errors='replace').read())", b64(path))
}

func pythonFetchScript(url string) string {
	return fmt.Sprintf("import base64,sys,urllib.request;sys.stdout.write(urllib.request.urlopen(base64.b64decode('%s').decode('utf-8')).read().decode('utf-8','replace'))", b64(url))
}

// buildBuiltinInvocation renders a Studio built-in call for the target tool.
// It returns the single-line command and, when the tool wants argv, the argv
// form that needs no shell quoting at all.
func buildBuiltinInvocation(target commandTarget, name string, payload map[string]any) (string, []string, bool) {
	switch name {
	case "python":
		code := firstString(payload, "code", "source", "script")
		if strings.TrimSpace(code) == "" {
			return "", nil, false
		}
		if target.Flavor == shellPowerShell {
			shell := "$c=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + b64(code) + "')); $c | python -"
			return shell, []string{"python", "-c", code}, true
		}
		shell := "python -c \"import base64;exec(base64.b64decode('" + b64(code) + "').decode('utf-8'))\""
		return shell, []string{"python", "-c", code}, true
	case "file_write":
		path := firstToolPath(payload)
		content := firstText(payload, "content", "file_text", "text", "contents", "data", "body", "value", "new_str")
		if strings.TrimSpace(path) == "" {
			return "", nil, false
		}
		argv := []string{"python", "-c", pythonWriteScript(path, content)}
		if target.Flavor == shellPowerShell {
			shell := "$p=" + psQuote(path) + "; $c=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + b64(content) + "')); [IO.File]::WriteAllText($p,$c,(New-Object Text.UTF8Encoding($false)))"
			return shell, argv, true
		}
		return "python -c \"" + pythonWriteScript(path, content) + "\"", argv, true
	default:
		path := firstToolPath(payload)
		if strings.TrimSpace(path) != "" {
			if target.Flavor == shellPowerShell {
				return "Get-Content -LiteralPath " + psQuote(path) + " -Raw", []string{"python", "-c", pythonReadScript(path)}, true
			}
			return "python -c \"" + pythonReadScript(path) + "\"", []string{"python", "-c", pythonReadScript(path)}, true
		}
		if url := firstString(payload, "url", "link"); strings.TrimSpace(url) != "" {
			if target.Flavor == shellPowerShell {
				return "Invoke-WebRequest -Uri " + psQuote(url) + " -UseBasicParsing | Select-Object -ExpandProperty Content", []string{"python", "-c", pythonFetchScript(url)}, true
			}
			return "python -c \"" + pythonFetchScript(url) + "\"", []string{"python", "-c", pythonFetchScript(url)}, true
		}
	}
	return "", nil, false
}

// buildFallbackInvocation renders a harmless diagnostic command for the target.
func buildFallbackInvocation(target commandTarget, builtin string) (string, []string, bool) {
	message := fmt.Sprintf("Built-in tool %s is unavailable in this client. Retry with one of the declared tools.", sanitizeToolName(builtin))
	if target.Flavor == shellPowerShell {
		return "echo " + psQuote(message), []string{"echo", message}, true
	}
	return "echo " + message, []string{"echo", message}, true
}

func sanitizeToolName(name string) string {
	var out strings.Builder
	for _, char := range strings.TrimSpace(name) {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
			out.WriteRune(char)
		case char == '_' || char == '-' || char == '.' || char == '/':
			out.WriteRune(char)
		case char == ' ':
			out.WriteRune('_')
		}
	}
	if out.Len() == 0 {
		return "unknown"
	}
	return out.String()
}

// DroppedBuiltinNotice explains why a Studio built-in call was ignored.
func DroppedBuiltinNotice(names []string, tools []any) string {
	builtins := strings.Join(names, ", ")
	declared := DeclaredToolNames(tools)
	if len(declared) == 0 {
		return fmt.Sprintf("（上游调用了内置工具 %s，但本次请求没有声明任何工具，该调用已忽略。）", builtins)
	}
	return fmt.Sprintf("（上游调用了内置工具 %s，当前客户端声明的工具（%s）里没有可执行命令的工具，该调用已忽略。请在本客户端启用 shell/命令执行类工具后重试。）", builtins, strings.Join(declared, ", "))
}
