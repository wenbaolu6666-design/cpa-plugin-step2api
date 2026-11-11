package translator

import (
	"strings"
	"testing"
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
