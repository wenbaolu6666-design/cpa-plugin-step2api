package cpa

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
)

func TestFitPlaygroundChatRequestPreservesToolProtocolUnder1MiB(t *testing.T) {
	baseSys := strings.Repeat("系统规范说明与工作区指令。\n", 3000)
	toolProto := "CRITICAL ENVIRONMENT OVERRIDE\nAvailable tools (JSON):\n[{\"name\":\"exec_command\"}]"
	hugeContent := "### User\n初始目标：核查本地文件\n\n" +
		strings.Repeat("### Tool result (call_1)\n{\"line\":\"番荔枝数据行\\\"quoted\\\"\"}\n\n", 25000) +
		"### User\n请继续执行最后一步核验"

	req := stepstudio.PlaygroundChatRequest{
		Model: stepstudio.ChatModelPayload{
			ModelID: "step-5-preview",
			Config:  stepstudio.ChatModelConfig{MaxTokens: 20000, Temperature: 0.7, Thinking: true, Effort: "xhigh"},
		},
		Content:      hugeContent,
		SystemPrompt: baseSys + "\n\n" + toolProto,
		AssetIDs:     []string{},
	}

	fitted := fitPlaygroundChatRequest("conv_123", req, baseSys, toolProto, maxStudioRequestBodyBytes)
	body, err := json.Marshal(stepstudio.TurnStreamBody{
		ConversationID: "conv_123",
		Kind:           "chat",
		Chat:           fitted,
	})
	if err != nil {
		t.Fatalf("marshal fitted request: %v", err)
	}
	if len(body) > maxStudioRequestBodyBytes {
		t.Fatalf("marshaled body %d exceeds budget %d", len(body), maxStudioRequestBodyBytes)
	}
	if !utf8.ValidString(fitted.Content) || !utf8.ValidString(fitted.SystemPrompt) {
		t.Fatal("fitted request contains invalid UTF-8")
	}
	if !strings.Contains(fitted.SystemPrompt, toolProto) {
		t.Fatal("ToolProtocol must never be dropped when fitting large requests")
	}
	if !strings.Contains(fitted.Content, "请继续执行最后一步核验") {
		t.Fatal("recent tail of conversation must be preserved")
	}
}

func TestSplitModelAndEffortSuffixAndErrorClassification(t *testing.T) {
	base, hint := splitModelAndEffortSuffix("step-5-preview(max)")
	if base != "step-5-preview" || hint != "max" {
		t.Fatalf("unexpected split: base=%q hint=%q", base, hint)
	}
	concErr := &stepstudio.APIError{
		StatusCode: 429,
		BizCode:    "STUDIO_RATE_LIMITED",
		Message:    "concurrency reached, current: 6, limit: 5",
	}
	if !isConcurrencyLimitError(concErr) {
		t.Fatal("expected concurrency error detection")
	}
	pErr := pluginErrorFromUpstreamErr(concErr)
	if pErr == nil || pErr.HTTPStatus != 429 || strings.Contains(pErr.Message, "额度") {
		t.Fatalf("concurrency error must return 503 without quota keyword: %+v", pErr)
	}

	evCh := make(chan stepstudio.StudioEvent, 2)
	errCh := make(chan error, 1)
	evCh <- stepstudio.StudioEvent{Type: stepstudio.EventDone}
	close(evCh)
	close(errCh)
	canceled := make(chan struct{})
	drainStudioStreamQuietly(evCh, errCh, func() { close(canceled) }, time.Second)
	select {
	case <-canceled:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected cancel callback after draining closed channel")
	}
}

func TestSafetyReviewErrorIsReportedAsRetryableBusy(t *testing.T) {
	safetyErr := &stepstudio.APIError{StatusCode: 500, Message: "safety review failed"}
	if !isSafetyReviewError(safetyErr) {
		t.Fatal("expected safety review detection")
	}
	pErr := pluginErrorFromUpstreamErr(safetyErr)
	if pErr == nil || pErr.Code != "upstream_safety_review" || !pErr.Retryable || pErr.HTTPStatus != 503 {
		t.Fatalf("unexpected mapping: %+v", pErr)
	}
}
