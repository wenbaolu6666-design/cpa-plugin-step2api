package translator

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseOpenAIRequest(t *testing.T) {
	payload := []byte(`{
		"model": "step-3.5-flash",
		"messages": [
			{"role": "system", "content": "You are helpful"},
			{"role": "user", "content": "Hello"}
		],
		"reasoning_effort": "high"
	}`)

	req, err := ParseOpenAIRequest(payload, true, "medium")
	if err != nil {
		t.Fatalf("ParseOpenAIRequest failed: %v", err)
	}
	if req.Model != "step-3.5-flash" {
		t.Errorf("unexpected model: %s", req.Model)
	}
	if req.Effort != "high" || !req.Thinking {
		t.Errorf("unexpected thinking config: %v, %s", req.Thinking, req.Effort)
	}
	if req.SystemPrompt != "You are helpful" {
		t.Errorf("unexpected system prompt: %s", req.SystemPrompt)
	}

	transcript := RenderTranscript(req.Messages)
	if transcript != "Hello" {
		t.Errorf("unexpected transcript for single user message: %s", transcript)
	}
}

func TestRenderMultiTurnTranscript(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "First"},
		{Role: "assistant", Content: "Answer 1"},
		{Role: "user", Content: "Second"},
	}
	tr := RenderTranscript(msgs)
	if !strings.Contains(tr, "### User\nFirst") || !strings.Contains(tr, "### Assistant\nAnswer 1") {
		t.Errorf("unexpected multi-turn transcript: %s", tr)
	}
}

func TestParseOpenAIRequestExtractsLastUserImage(t *testing.T) {
	payload := []byte(`{
		"model": "step-5-preview",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "old"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}}
			]},
			{"role": "assistant", "content": "ok"},
			{"role": "user", "content": [
				{"type": "input_text", "text": "看图"},
				{"type": "input_image", "image_url": "https://example.com/a.png"}
			]}
		]
	}`)

	req, err := ParseOpenAIRequest(payload, true, "medium")
	if err != nil {
		t.Fatalf("ParseOpenAIRequest failed: %v", err)
	}
	if len(req.Images) != 1 || req.Images[0].URL != "https://example.com/a.png" {
		t.Fatalf("unexpected images: %#v", req.Images)
	}
	if req.Messages[2].Content != "看图" {
		t.Errorf("unexpected last user content: %q", req.Messages[2].Content)
	}
	if !strings.Contains(req.Messages[0].Content, "[image]") {
		t.Errorf("older image marker should be kept: %q", req.Messages[0].Content)
	}
}

func TestParseOpenAIRequestExtractsLeadingDeveloperRoleAndMapsMaxEffort(t *testing.T) {
	payload := []byte(`{
		"model": "step-5-preview",
		"reasoning_effort": "max",
		"messages": [
			{"role": "developer", "content": [{"type": "text", "text": "You are Codex on Windows."}]},
			{"role": "developer", "content": "AGENTS rules here."},
			{"role": "user", "content": "Check D:\\workspace"},
			{"role": "developer", "content": "Mode switched"}
		],
		"tools": [{"type": "function", "function": {"name": "exec_command"}}]
	}`)

	req, err := ParseOpenAIRequest(payload, true, "medium")
	if err != nil {
		t.Fatalf("ParseOpenAIRequest failed: %v", err)
	}
	if req.Effort != "xhigh" {
		t.Fatalf("expected effort xhigh for max, got %q", req.Effort)
	}
	if !strings.Contains(req.BaseSystem, "You are Codex on Windows.") || !strings.Contains(req.BaseSystem, "AGENTS rules here.") {
		t.Fatalf("leading developer messages should be in BaseSystem: %q", req.BaseSystem)
	}
	if !strings.Contains(req.ToolProtocol, "exec_command") || !strings.HasSuffix(req.SystemPrompt, req.ToolProtocol) {
		t.Fatalf("SystemPrompt should end with ToolProtocol")
	}
	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 non-leading messages, got %d", len(req.Messages))
	}
	tr := RenderTranscript(req.Messages)
	if strings.Contains(tr, "You are Codex on Windows.") {
		t.Fatalf("leading developer message should not duplicate into transcript")
	}
	if !strings.Contains(tr, "### System update\nMode switched") {
		t.Fatalf("mid-turn developer message should render as System update: %s", tr)
	}
}

func TestTruncateUTF8HeadTailNeverSplitsMultibyteRunes(t *testing.T) {
	sample := strings.Repeat("番荔枝属种质资源果实品质评价研究D:\\WenPaul\\Documents\\路由网关\n", 400)
	for _, budget := range []int{17, 65, 128, 511, 2049, 8193} {
		got := TruncateUTF8HeadTail(sample, budget)
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8 produced for budget=%d", budget)
		}
		if len(got) > budget {
			t.Fatalf("len(got)=%d exceeds budget=%d", len(got), budget)
		}
	}
}
