package cpa

import (
	"encoding/json"
	"strings"
	"testing"
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
