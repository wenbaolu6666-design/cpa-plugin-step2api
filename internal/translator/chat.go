package translator

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

const ToolProtocolTemplate = `This session uses tools that are executed by the client application on the user's own computer.

Rules:
- The only tools available in this session are the ones listed in "Available tools (JSON)" below.
- When the task requires running commands, reading or writing local files (including Windows paths such as C:\ and D:\), or operating the desktop, call the matching listed client tool instead of explaining limitations.
- If you do not emit a native tool_call block, emit a single JSON object (without markdown fences) in this shape: {"name": "<tool_name>", "arguments": {<arguments>}}
- The arguments field must be a JSON object; nested arrays, objects, numbers and booleans stay as native JSON values.
- After emitting tool calls, stop and wait for the client to return tool results in the next turn.
- If no listed tool is needed, answer the user directly as plain text.
%s

Available tools (JSON):
%s`

// SoftToolProtocolTemplate is a shorter neutral variant used when StepFun's
// content review rejects the regular protocol wording.
const SoftToolProtocolTemplate = `This session provides client-executed tools. Only the tools in "Available tools (JSON)" are available.

Call a listed tool when local commands or files are needed; otherwise answer directly.
Tool call format (when a native tool_call block is not used): {"name": "<tool_name>", "arguments": {...}}
%s

Available tools (JSON):
%s`

type ChatMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCalls  []any  `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ImageRef is an image that should be uploaded to Studio and attached to the
// current turn as an asset.
type ImageRef struct {
	URL  string
	MIME string
	Name string
}

type ExtractedRequest struct {
	Model            string
	Messages         []ChatMessage
	BaseSystem       string
	ToolProtocol     string
	SoftToolProtocol string
	SystemPrompt     string
	Tools            []any
	Images           []ImageRef
	Thinking         bool
	Effort           string
	Temperature      float64
	MaxTokens        int
	EnableWebSearch  bool
}

func ParseOpenAIRequest(payload []byte, defaultThinking bool, defaultEffort string) (*ExtractedRequest, error) {
	parsed := gjson.ParseBytes(payload)

	req := &ExtractedRequest{
		Model:       parsed.Get("model").String(),
		Thinking:    defaultThinking,
		Effort:      defaultEffort,
		Temperature: 0.7,
		MaxTokens:   20000,
	}

	if t := parsed.Get("temperature"); t.Exists() {
		req.Temperature = t.Float()
	}
	if maxT := parsed.Get("max_tokens"); maxT.Exists() {
		req.MaxTokens = int(maxT.Int())
	} else if maxC := parsed.Get("max_completion_tokens"); maxC.Exists() {
		req.MaxTokens = int(maxC.Int())
	}

	if effort := parsed.Get("reasoning_effort"); effort.Exists() {
		effStr := strings.ToLower(strings.TrimSpace(effort.String()))
		if effStr == "none" || effStr == "off" || effStr == "disabled" {
			req.Thinking = false
		} else {
			req.Thinking = true
			switch effStr {
			case "minimal", "low":
				req.Effort = "low"
			case "medium":
				req.Effort = "medium"
			case "high":
				req.Effort = "high"
			case "xhigh", "ultra", "max":
				req.Effort = "xhigh"
			default:
				req.Effort = "medium"
			}
		}
	}

	if ws := parsed.Get("web_search"); ws.Exists() {
		req.EnableWebSearch = ws.Bool()
	} else if ews := parsed.Get("enable_web_search"); ews.Exists() {
		req.EnableWebSearch = ws.Bool()
	}

	var systemParts []string
	messagesArray := parsed.Get("messages").Array()
	lastUserIndex := -1
	for _, m := range messagesArray {
		role := m.Get("role").String()
		content := ""
		var images []ImageRef
		if c := m.Get("content"); c.Type == gjson.String {
			content = c.String()
		} else if c.IsArray() {
			for _, part := range c.Array() {
				switch part.Get("type").String() {
				case "text", "input_text", "output_text":
					content += part.Get("text").String()
				case "image_url", "input_image":
					if ref, ok := imageRefFromPart(part); ok {
						images = append(images, ref)
					}
					content += "[image]"
				}
			}
		}

		if role == "system" || (role == "developer" && len(req.Messages) == 0) {
			if strings.TrimSpace(content) != "" {
				systemParts = append(systemParts, content)
			}
			continue
		}

		msg := ChatMessage{
			Role:       role,
			Content:    content,
			ToolCallID: m.Get("tool_call_id").String(),
		}
		if tc := m.Get("tool_calls"); tc.Exists() && tc.IsArray() {
			var calls []any
			_ = json.Unmarshal([]byte(tc.Raw), &calls)
			msg.ToolCalls = calls
		}
		req.Messages = append(req.Messages, msg)
		if role == "user" {
			lastUserIndex = len(req.Messages) - 1
			req.Images = images
		}
	}

	// Studio attaches assets to the current turn only; images from older turns
	// stay as [image] markers in the rendered transcript.
	if lastUserIndex >= 0 && len(req.Images) > 0 {
		req.Messages[lastUserIndex].Content = strings.ReplaceAll(req.Messages[lastUserIndex].Content, "[image]", "")
		if strings.TrimSpace(req.Messages[lastUserIndex].Content) == "" {
			req.Messages[lastUserIndex].Content = "请查看附图。"
		}
	}

	systemText := strings.Join(systemParts, "\n\n")
	req.BaseSystem = systemText

	if tools := parsed.Get("tools"); tools.Exists() && tools.IsArray() {
		var toolList []any
		_ = json.Unmarshal([]byte(tools.Raw), &toolList)
		req.Tools = toolList
	}

	if len(req.Tools) > 0 {
		toolsJSON, _ := json.MarshalIndent(req.Tools, "", "  ")
		protocol := fmt.Sprintf(ToolProtocolTemplate, toolChoiceDirective(parsed), string(toolsJSON))
		req.ToolProtocol = protocol
		req.SoftToolProtocol = fmt.Sprintf(SoftToolProtocolTemplate, toolChoiceDirective(parsed), string(toolsJSON))
		if systemText != "" {
			systemText += "\n\n" + protocol
		} else {
			systemText = protocol
		}
	}

	req.SystemPrompt = systemText
	return req, nil
}

func imageRefFromPart(part gjson.Result) (ImageRef, bool) {
	raw := part.Get("image_url")
	if !raw.Exists() {
		raw = part.Get("image")
	}
	if !raw.Exists() {
		return ImageRef{}, false
	}
	url := raw.String()
	if raw.IsObject() {
		url = raw.Get("url").String()
	}
	url = strings.TrimSpace(url)
	if url == "" {
		return ImageRef{}, false
	}
	ref := ImageRef{URL: url}
	if strings.HasPrefix(strings.ToLower(url), "data:") {
		header, _, _ := strings.Cut(url, ",")
		mediaType := strings.TrimPrefix(header, "data:")
		mediaType, _, _ = strings.Cut(mediaType, ";")
		ref.MIME = strings.TrimSpace(mediaType)
	}
	return ref, true
}

func toolChoiceDirective(parsed gjson.Result) string {
	choice := parsed.Get("tool_choice")
	if !choice.Exists() {
		return ""
	}
	if choice.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(choice.String())) {
		case "required", "any":
			return "- This turn MUST call at least one listed tool."
		case "none":
			return "- Do not call any tool this turn."
		}
	}
	if name := strings.TrimSpace(choice.Get("function.name").String()); name != "" {
		return fmt.Sprintf("- This turn MUST call the tool %q.", name)
	}
	return ""
}

func RenderTranscript(messages []ChatMessage) string {
	if len(messages) == 0 {
		return "Hello"
	}
	if len(messages) == 1 && messages[0].Role == "user" {
		return messages[0].Content
	}

	var blocks []string
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			blocks = append(blocks, "### User\n"+msg.Content)
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				tcBytes, _ := json.Marshal(msg.ToolCalls)
				blocks = append(blocks, "### Assistant (tool calls)\n"+string(tcBytes)+"\n"+msg.Content)
			} else {
				blocks = append(blocks, "### Assistant\n"+msg.Content)
			}
		case "tool":
			blocks = append(blocks, fmt.Sprintf("### Tool result (%s)\n%s", msg.ToolCallID, msg.Content))
		case "developer":
			blocks = append(blocks, "### System update\n"+msg.Content)
		default:
			blocks = append(blocks, fmt.Sprintf("### %s\n%s", msg.Role, msg.Content))
		}
	}
	blocks = append(blocks, "### Instruction\nContinue the conversation above; reply to the final user message.")
	return strings.Join(blocks, "\n\n")
}

// TruncateUTF8Head returns at most maxBytes from the start of value without
// splitting multi-byte UTF-8 runes.
func TruncateUTF8Head(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return strings.ToValidUTF8(value[:end], "")
}

// TruncateUTF8Tail returns at most maxBytes from the end of value without
// splitting multi-byte UTF-8 runes.
func TruncateUTF8Tail(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return strings.ToValidUTF8(value[start:], "")
}

// TruncateUTF8HeadTail keeps both the initial context (~15%) and the most
// recent conversation tail (~85%) within maxBytes on valid UTF-8 boundaries.
func TruncateUTF8HeadTail(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	const marker = "\n\n...[中间历史因长度限制已省略]...\n\n"
	if maxBytes <= len(marker)+64 {
		return TruncateUTF8Tail(value, maxBytes)
	}
	budget := maxBytes - len(marker)
	headBudget := budget * 15 / 100
	if headBudget > 24*1024 {
		headBudget = 24 * 1024
	}
	tailBudget := budget - headBudget
	head := TruncateUTF8Head(value, headBudget)
	tail := TruncateUTF8Tail(value, tailBudget)
	return head + marker + tail
}
