package cpa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginabi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
	"github.com/WenPaul/cpa-plugin-step2api/internal/translator"
	"github.com/google/uuid"
)

func dumpInvalidBodyDebug(model string, payloadBytes []byte, chatReq stepstudio.PlaygroundChatRequest) {
	type dump struct {
		Model      string `json:"model"`
		PayloadLen int    `json:"payload_len"`
		Payload    string `json:"payload"`
		Content    string `json:"content"`
		System     string `json:"system_prompt"`
	}
	payload := string(payloadBytes)
	if len(payload) > 200000 {
		payload = payload[:200000]
	}
	out := dump{
		Model:      model,
		PayloadLen: len(payloadBytes),
		Payload:    payload,
		Content:    chatReq.Content,
		System:     chatReq.SystemPrompt,
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(os.TempDir(), "step2api-invalid-body.json"), raw, 0o644)
}

func truncateTail(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

func isInvalidBodyError(err error) bool {
	apiErr, ok := err.(*stepstudio.APIError)
	if !ok || apiErr == nil {
		return false
	}
	return apiErr.StatusCode == http.StatusBadRequest && strings.Contains(strings.ToLower(apiErr.Message), "invalid request body")
}

func clampExtractedForStudio(cleanModel string, extracted *translator.ExtractedRequest) {
	if extracted == nil {
		return
	}
	limit := 64000
	if info, ok := stepstudio.GetModelInfo(cleanModel); ok && info.MaxTokens > 0 {
		limit = info.MaxTokens
	}
	if extracted.MaxTokens <= 0 || extracted.MaxTokens > limit {
		extracted.MaxTokens = limit
	}
	if extracted.Temperature < 0 {
		extracted.Temperature = 0
	}
	if extracted.Temperature > 2 {
		extracted.Temperature = 2
	}
}

func pluginErrorFromUpstreamErr(err error) *pluginabi.Error {
	apiErr, ok := err.(*stepstudio.APIError)
	if !ok || apiErr == nil {
		return nil
	}
	if apiErr.BizCode == "CENSORED" || strings.EqualFold(apiErr.RiskDecision, "BLOCK") {
		msg := "StepFun 内容审核拦截: " + apiErr.Message
		if apiErr.RiskDecision != "" {
			msg += " (risk_decision=" + apiErr.RiskDecision + ")"
		}
		return pluginabi.NewError("content_filter", msg, http.StatusBadRequest)
	}
	status := apiErr.StatusCode
	if status == 0 {
		status = http.StatusBadGateway
	}
	if status == http.StatusTooManyRequests {
		return pluginabi.NewError("upstream_rate_limited", "StepFun 上游限流/额度不足: "+apiErr.Message, http.StatusTooManyRequests)
	}
	return pluginabi.NewError("upstream_error", "StepFun 上游返回 http "+strconv.Itoa(status)+": "+apiErr.Message, status)
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcExecutorStreamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

func (p *Plugin) handleExecutorIdentifier() (any, error) {
	return rpcIdentifierResponse{
		Identifier:          ProviderID,
		Identifiers:         []string{"step2api", "stepstudio", "stepfun"},
		CredentialProviders: []string{"step2api", "stepstudio", "stepfun"},
	}, nil
}

func (p *Plugin) extractCredentials(storageJSON []byte, metadata map[string]any) (token, webID string) {
	if len(metadata) > 0 {
		if tok, ok := metadata["oasis_token"].(string); ok && strings.TrimSpace(tok) != "" {
			token = strings.TrimSpace(tok)
		}
		if w, ok := metadata["web_id"].(string); ok && strings.TrimSpace(w) != "" {
			webID = strings.TrimSpace(w)
		}
	}
	if len(storageJSON) > 0 {
		var payload AuthFilePayload
		if err := json.Unmarshal(storageJSON, &payload); err == nil {
			if token == "" && payload.OasisToken != "" {
				token = strings.TrimSpace(payload.OasisToken)
			}
			if webID == "" && payload.WebID != "" {
				webID = strings.TrimSpace(payload.WebID)
			}
		}
	}
	if token == "" || webID == "" {
		oauthDir := resolveOAuthDir()
		if files, err := filepath.Glob(filepath.Join(oauthDir, "step2api-*.json")); err == nil && len(files) > 0 {
			for _, f := range files {
				if data, err := os.ReadFile(f); err == nil {
					var item AuthFilePayload
					if err := json.Unmarshal(data, &item); err == nil {
						if token == "" && item.OasisToken != "" {
							token = item.OasisToken
						}
						if webID == "" && item.WebID != "" {
							webID = item.WebID
						}
					}
				}
			}
		}
	}
	if webID == "" && token != "" {
		webID = stepstudio.ExtractDeviceID(token)
	}
	return token, webID
}

func (p *Plugin) extractToken(storageJSON []byte, metadata map[string]any) string {
	tok, _ := p.extractCredentials(storageJSON, metadata)
	return tok
}

func uploadTurnImages(ctx context.Context, client *stepstudio.Client, refs []translator.ImageRef) ([]string, error) {
	if len(refs) == 0 {
		return []string{}, nil
	}
	assetIDs := make([]string, 0, len(refs))
	for idx, ref := range refs {
		data, mimeType, name, err := client.LoadImageData(ctx, ref.URL, ref.MIME, ref.Name)
		if err != nil {
			return nil, fmt.Errorf("image %d: %w", idx+1, err)
		}
		if strings.TrimSpace(mimeType) == "" {
			mimeType = http.DetectContentType(data)
		}
		width, height := 0, 0
		if cfg, _, errConfig := image.DecodeConfig(bytes.NewReader(data)); errConfig == nil {
			width, height = cfg.Width, cfg.Height
		}
		assetID, errUpload := client.UploadAsset(ctx, name, mimeType, data, width, height)
		if errUpload != nil {
			return nil, fmt.Errorf("image %d: %w", idx+1, errUpload)
		}
		assetIDs = append(assetIDs, assetID)
	}
	return assetIDs, nil
}

func (p *Plugin) handleExecutorExecuteStream(raw []byte) (any, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode executor.execute_stream request: %w", err)
	}

	token, webID := p.extractCredentials(req.StorageJSON, req.AuthMetadata)
	if token == "" {
		return nil, pluginabi.NewError("unauthorized", "no StepFun Oasis-Token found in credentials", http.StatusUnauthorized)
	}

	cleanModel := CleanModelName(req.Model, p.Config().ModelPrefix)
	extracted, err := translator.ParseOpenAIRequest(req.Payload, p.Config().DefaultThinking, p.Config().DefaultEffort)
	if err != nil {
		return nil, fmt.Errorf("parse request payload: %w", err)
	}
	clampExtractedForStudio(cleanModel, extracted)

	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Minute)
	client := stepstudio.NewClient(p.Config().BaseURL, token, webID, nil)
	assetIDs, err := uploadTurnImages(ctx, client, extracted.Images)
	if err != nil {
		cancel()
		return nil, pluginabi.NewError("image_upload_failed", "上传图片到 StepFun Studio 失败: "+err.Error(), http.StatusBadGateway)
	}

	convID, err := client.CreateConversation(ctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create upstream conversation failed: %w", err)
	}

	turnText := translator.RenderTranscript(extracted.Messages)
	if strings.TrimSpace(turnText) == "" {
		turnText = "…"
	}
	chatReq := stepstudio.PlaygroundChatRequest{
		Model: stepstudio.ChatModelPayload{
			ModelID: cleanModel,
			Config: stepstudio.ChatModelConfig{
				MaxTokens:   extracted.MaxTokens,
				Temperature: extracted.Temperature,
				Thinking:    extracted.Thinking,
				Effort:      extracted.Effort,
			},
		},
		Content:         turnText,
		SystemPrompt:    extracted.SystemPrompt,
		AssetIDs:        assetIDs,
		EnableWebSearch: extracted.EnableWebSearch,
	}

	eventsCh, errCh, err := client.StreamPlayground(ctx, convID, chatReq)
	if err != nil && isInvalidBodyError(err) {
		dumpInvalidBodyDebug(req.Model, req.Payload, chatReq)
		retryReq := chatReq
		retryReq.SystemPrompt = ""
		retryReq.EnableWebSearch = false
		eventsCh, errCh, err = client.StreamPlayground(ctx, convID, retryReq)
		if err != nil && isInvalidBodyError(err) {
			retryReq.Content = truncateTail(chatReq.Content, 30000)
			eventsCh, errCh, err = client.StreamPlayground(ctx, convID, retryReq)
		}
	}
	if err != nil {
		cancel()
		p.noteStudioUpstreamFailure(cleanModel, err)
		if pErr := pluginErrorFromUpstreamErr(err); pErr != nil {
			return nil, pErr
		}
		return nil, fmt.Errorf("start upstream playground stream failed: %w", err)
	}

	streamID := req.StreamID
	if strings.TrimSpace(streamID) == "" {
		streamID = uuid.NewString()
	}

	trans := translator.NewStreamTranslator(cleanModel)

	go func() {
		defer cancel()
		for ev := range eventsCh {
			chunks := trans.TranslateEvent(ev)
			for _, chunk := range chunks {
				_ = p.emitStreamChunk(streamID, chunk)
			}
		}
		if sErr := <-errCh; sErr != nil {
			p.noteStudioUpstreamFailure(cleanModel, sErr)
			pErr := pluginErrorFromUpstreamErr(sErr)
			if pErr == nil {
				pErr = pluginabi.NewError("upstream_stream_error", sErr.Error(), http.StatusBadGateway)
			}
			_ = p.closeStream(streamID, pErr)
			return
		}
		if !trans.SentFinish {
			for _, chunk := range trans.FinalChunks("stop", nil) {
				_ = p.emitStreamChunk(streamID, chunk)
			}
		}
		_ = p.closeStream(streamID, nil)
	}()

	headers := make(http.Header)
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Connection", "keep-alive")

	return rpcExecutorStreamResponse{
		Headers: headers,
	}, nil
}

func (p *Plugin) handleExecutorExecute(raw []byte) (any, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	token, webID := p.extractCredentials(req.StorageJSON, req.AuthMetadata)
	if token == "" {
		return nil, pluginabi.NewError("unauthorized", "no StepFun Oasis-Token found in credentials", http.StatusUnauthorized)
	}

	cleanModel := CleanModelName(req.Model, p.Config().ModelPrefix)
	extracted, err := translator.ParseOpenAIRequest(req.Payload, p.Config().DefaultThinking, p.Config().DefaultEffort)
	if err != nil {
		return nil, err
	}
	clampExtractedForStudio(cleanModel, extracted)

	client := stepstudio.NewClient(p.Config().BaseURL, token, webID, nil)
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Minute)
	defer cancel()
	assetIDs, err := uploadTurnImages(ctx, client, extracted.Images)
	if err != nil {
		return nil, pluginabi.NewError("image_upload_failed", "上传图片到 StepFun Studio 失败: "+err.Error(), http.StatusBadGateway)
	}

	convID, err := client.CreateConversation(ctx)
	if err != nil {
		return nil, err
	}

	turnText := translator.RenderTranscript(extracted.Messages)
	if strings.TrimSpace(turnText) == "" {
		turnText = "…"
	}
	chatReq := stepstudio.PlaygroundChatRequest{
		Model: stepstudio.ChatModelPayload{
			ModelID: cleanModel,
			Config: stepstudio.ChatModelConfig{
				MaxTokens:   extracted.MaxTokens,
				Temperature: extracted.Temperature,
				Thinking:    extracted.Thinking,
				Effort:      extracted.Effort,
			},
		},
		Content:         turnText,
		SystemPrompt:    extracted.SystemPrompt,
		AssetIDs:        assetIDs,
		EnableWebSearch: extracted.EnableWebSearch,
	}

	eventsCh, errCh, err := client.StreamPlayground(ctx, convID, chatReq)
	if err != nil && isInvalidBodyError(err) {
		dumpInvalidBodyDebug(req.Model, req.Payload, chatReq)
		retryReq := chatReq
		retryReq.SystemPrompt = ""
		retryReq.EnableWebSearch = false
		eventsCh, errCh, err = client.StreamPlayground(ctx, convID, retryReq)
		if err != nil && isInvalidBodyError(err) {
			retryReq.Content = truncateTail(chatReq.Content, 30000)
			eventsCh, errCh, err = client.StreamPlayground(ctx, convID, retryReq)
		}
	}
	if err != nil {
		p.noteStudioUpstreamFailure(cleanModel, err)
		if pErr := pluginErrorFromUpstreamErr(err); pErr != nil {
			return nil, pErr
		}
		return nil, err
	}

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var finalUsage *stepstudio.UsageInfo
	type nativeToolCall struct {
		ID   string
		Name string
		Args string
	}
	var nativeToolCalls []nativeToolCall
	nativeToolIndex := map[int]int{}

	for ev := range eventsCh {
		switch ev.Type {
		case stepstudio.EventTextDelta:
			var d stepstudio.DeltaData
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				contentBuilder.WriteString(d.Delta)
			}
		case stepstudio.EventReasoningDelta:
			var d stepstudio.DeltaData
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				reasoningBuilder.WriteString(d.Delta)
			}
		case stepstudio.EventToolCallStart:
			var d stepstudio.ToolCallStartData
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				pos, exists := nativeToolIndex[d.BlockIndex]
				if !exists {
					pos = len(nativeToolCalls)
					nativeToolIndex[d.BlockIndex] = pos
					nativeToolCalls = append(nativeToolCalls, nativeToolCall{})
				}
				nativeToolCalls[pos].ID = d.ToolCallID
				nativeToolCalls[pos].Name = d.ToolName
				nativeToolCalls[pos].Args = d.ArgumentsJSON
			}
		case stepstudio.EventToolCallDelta:
			var d stepstudio.ToolCallDeltaData
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				pos, exists := nativeToolIndex[d.BlockIndex]
				if !exists {
					pos = len(nativeToolCalls)
					nativeToolIndex[d.BlockIndex] = pos
					nativeToolCalls = append(nativeToolCalls, nativeToolCall{})
				}
				delta := d.Delta
				if delta == "" {
					delta = d.ProgressText
				}
				nativeToolCalls[pos].Args += delta
			}
		case stepstudio.EventFinal:
			var d stepstudio.FinalData
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				finalUsage = d.Usage
			}
		}
	}

	if sErr := <-errCh; sErr != nil {
		p.noteStudioUpstreamFailure(cleanModel, sErr)
		if pErr := pluginErrorFromUpstreamErr(sErr); pErr != nil {
			return nil, pErr
		}
		return nil, sErr
	}

	finishReason := "stop"
	content := contentBuilder.String()
	var toolCalls []map[string]any
	if len(nativeToolCalls) > 0 {
		finishReason = "tool_calls"
		for _, call := range nativeToolCalls {
			item := translator.OpenAIToolCall(call.Name, call.Args)
			if call.ID != "" {
				item["id"] = call.ID
			}
			toolCalls = append(toolCalls, item)
		}
		content = ""
	} else if call, ok := translator.ParseToolCallText(content); ok {
		finishReason = "tool_calls"
		toolCalls = []map[string]any{translator.OpenAIToolCall(call.Name, call.Arguments)}
		content = ""
	}

	message := map[string]any{
		"role":              "assistant",
		"content":           content,
		"reasoning_content": reasoningBuilder.String(),
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	responseObj := map[string]any{
		"id":      "chatcmpl-" + uuid.NewString()[:8],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   cleanModel,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
	}
	if finalUsage != nil {
		input := int(finalUsage.TokensIn)
		output := int(finalUsage.TokensOut)
		responseObj["usage"] = map[string]any{
			"prompt_tokens":     input,
			"completion_tokens": output,
			"total_tokens":      input + output,
			"prompt_tokens_details": map[string]any{
				"cached_tokens": int(finalUsage.CachedTokens),
			},
		}
	}
	bodyBytes, _ := json.Marshal(responseObj)

	return pluginapi.ExecutorResponse{
		Payload: bodyBytes,
		Headers: make(http.Header),
	}, nil
}

func (p *Plugin) handleExecutorCountTokens(raw []byte) (any, error) {
	return map[string]any{
		"tokens": 0,
	}, nil
}
