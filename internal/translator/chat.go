package translator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

const ToolProtocolTemplate = `You can call tools. A tool call MUST be emitted as a single JSON object and nothing else, exactly in this shape:

{"name": "<tool_name>", "arguments": {<arguments>}}

Rules:
- The ONLY tools that exist are the ones listed below. Never call anything else.
- Emit at most one tool call per turn.
- Never wrap the JSON in markdown fences and never add prose around it.
- When tool results are supplied, use them to answer the user normally.
- If no tool from the list is needed, answer the user directly as plain text.

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
	Model           string
	Messages        []ChatMessage
	SystemPrompt    string
	Tools           []any
	Images          []ImageRef
	Thinking        bool
	Effort          string
	Temperature     float64
	MaxTokens       int
	EnableWebSearch bool
}

func ParseOpenAIRequest(payload []byte, defaultThinking bool, defaultEffort string) (*ExtractedRequest, error) {
	parsed := gjson.ParseBytes(payload)

	req := &ExtractedRequest{
		Model:       parsed.Get("model").String(),
		Thinking:    defaultThinking,
		Effort:      defaultEffort,
		Temperature: 0.7,
		MaxTokens:   4096,
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
		effStr := strings.ToLower(effort.String())
		if effStr == "none" || effStr == "off" || effStr == "disabled" {
			req.Thinking = false
		} else {
			req.Thinking = true
			switch effStr {
			case "low", "medium", "high", "xhigh":
				req.Effort = effStr
			default:
				req.Effort = "medium"
			}
		}
	}

	if ws := parsed.Get("web_search"); ws.Exists() {
		req.EnableWebSearch = ws.Bool()
	} else if ews := parsed.Get("enable_web_search"); ews.Exists() {
		req.EnableWebSearch = ews.Bool()
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

		if role == "system" {
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

	if tools := parsed.Get("tools"); tools.Exists() && tools.IsArray() {
		var toolList []any
		_ = json.Unmarshal([]byte(tools.Raw), &toolList)
		req.Tools = toolList
	}

	if len(req.Tools) > 0 {
		toolsJSON, _ := json.MarshalIndent(req.Tools, "", "  ")
		protocol := fmt.Sprintf(ToolProtocolTemplate, string(toolsJSON))
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
		default:
			blocks = append(blocks, fmt.Sprintf("### %s\n%s", msg.Role, msg.Content))
		}
	}
	blocks = append(blocks, "### Instruction\nContinue the conversation above; reply to the final user message.")
	return strings.Join(blocks, "\n\n")
}
