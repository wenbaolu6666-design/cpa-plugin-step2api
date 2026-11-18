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

const (
	// Studio rejects TurnStreamBody payloads above 1 MiB (1,048,576 bytes) with
	// HTTP 400 "invalid request body". Keep a comfortable margin below 1 MiB.
	maxStudioRequestBodyBytes = 920 * 1024
	maxBaseSystemPromptBytes  = 220 * 1024
	// Draining remaining Studio events until "done" takes ~1.5s for rejected
	// client tool calls and avoids leaking Studio's 5-slot Redis concurrency
	// counter. Cap background drain at 20s for slow sandbox calls.
	upstreamBackgroundDrainTimeout = 20 * time.Second
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
		payload = translator.TruncateUTF8Head(payload, 200000)
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

func isInvalidBodyError(err error) bool {
	apiErr, ok := err.(*stepstudio.APIError)
	if !ok || apiErr == nil {
		return false
	}
	msg := strings.ToLower(apiErr.Message)
	return apiErr.StatusCode == http.StatusBadRequest &&
		(strings.Contains(msg, "invalid request body") || strings.Contains(msg, "invalid json body"))
}

func isConcurrencyLimitError(err error) bool {
	apiErr, ok := err.(*stepstudio.APIError)
	if !ok || apiErr == nil {
		return false
	}
	msg := strings.ToLower(apiErr.Message)
	return apiErr.StatusCode == http.StatusTooManyRequests && strings.Contains(msg, "concurrency")
}

func isSafetyReviewError(err error) bool {
	apiErr, ok := err.(*stepstudio.APIError)
	if !ok || apiErr == nil {
		return false
	}
	return strings.Contains(strings.ToLower(apiErr.Message), "safety review")
}

// splitModelAndEffortSuffix separates an optional parenthesized thinking suffix
// such as "step-5-preview(max)" into ("step-5-preview", "max").
func splitModelAndEffortSuffix(model string) (baseModel string, effortHint string) {
	baseModel = strings.TrimSpace(model)
	openIdx := strings.IndexByte(baseModel, '(')
	closeIdx := strings.LastIndexByte(baseModel, ')')
	if openIdx > 0 && closeIdx > openIdx {
		effortHint = strings.ToLower(strings.TrimSpace(baseModel[openIdx+1 : closeIdx]))
		baseModel = strings.TrimSpace(baseModel[:openIdx])
	}
	return baseModel, effortHint
}

func applyModelEffortHint(extracted *translator.ExtractedRequest, effortHint string) {
	if extracted == nil || effortHint == "" {
		return
	}
	switch effortHint {
	case "none", "off", "disabled":
		extracted.Thinking = false
		extracted.Effort = ""
	case "minimal", "low":
		extracted.Thinking = true
		extracted.Effort = "low"
	case "medium":
		extracted.Thinking = true
		extracted.Effort = "medium"
	case "high":
		extracted.Thinking = true
		extracted.Effort = "high"
	case "xhigh", "ultra", "max":
		extracted.Thinking = true
		extracted.Effort = "xhigh"
	}
}

// drainStudioStreamQuietly consumes remaining Studio SSE events in the
// background until the channel closes (on EventDone) or timeout elapses, then
// cancels ctx. Aborting the TCP connection before EventDone causes Studio's
// server to hold the Redis concurrency slot for minutes.
func drainStudioStreamQuietly(
	eventsCh <-chan stepstudio.StudioEvent,
	errCh <-chan error,
	cancel context.CancelFunc,
	maxWait time.Duration,
) {
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	defer cancel()
	for {
		select {
		case _, ok := <-eventsCh:
			if !ok {
				select {
				case <-errCh:
				default:
				}
				return
			}
		case <-timer.C:
			return
		}
	}
}

// fitPlaygroundChatRequest ensures the marshaled Studio request stays within
// maxBodyBytes on valid UTF-8 boundaries while NEVER discarding toolProtocol.
func fitPlaygroundChatRequest(
	convID string,
	chatReq stepstudio.PlaygroundChatRequest,
	baseSystem string,
	toolProtocol string,
	maxBodyBytes int,
) stepstudio.PlaygroundChatRequest {
	if maxBodyBytes <= 0 {
		maxBodyBytes = maxStudioRequestBodyBytes
	}

	buildSystem := func(sysBase string) string {
		sysBase = strings.TrimSpace(sysBase)
		if toolProtocol == "" {
			return sysBase
		}
		if sysBase == "" {
			return toolProtocol
		}
		return sysBase + "\n\n" + toolProtocol
	}

	curBase := baseSystem
	if len(curBase) > maxBaseSystemPromptBytes {
		curBase = translator.TruncateUTF8HeadTail(curBase, maxBaseSystemPromptBytes)
	}
	if toolProtocol != "" || baseSystem != "" {
		chatReq.SystemPrompt = buildSystem(curBase)
	}

	if len(chatReq.Content)*2+len(chatReq.SystemPrompt)*2+4096 <= maxBodyBytes {
		return chatReq
	}

	marshaledLen := func(candidate stepstudio.PlaygroundChatRequest) int {
		body, err := json.Marshal(stepstudio.TurnStreamBody{
			ConversationID: convID,
			Kind:           "chat",
			Chat:           candidate,
		})
		if err != nil {
			return maxBodyBytes + 1
		}
		return len(body)
	}

	curLen := marshaledLen(chatReq)
	if curLen <= maxBodyBytes {
		return chatReq
	}

	sysOnly := chatReq
	sysOnly.Content = "…"
	if marshaledLen(sysOnly) > maxBodyBytes/2 && curBase != "" {
		targetBase := maxBodyBytes / 4
		if targetBase < 4096 {
			targetBase = 0
		}
		if targetBase == 0 {
			curBase = ""
		} else {
			curBase = translator.TruncateUTF8HeadTail(curBase, targetBase)
		}
		chatReq.SystemPrompt = buildSystem(curBase)
	}

	origContent := chatReq.Content
	sysOverhead := marshaledLen(stepstudio.PlaygroundChatRequest{
		Model:           chatReq.Model,
		Content:         "",
		SystemPrompt:    chatReq.SystemPrompt,
		AssetIDs:        chatReq.AssetIDs,
		EnableWebSearch: chatReq.EnableWebSearch,
	})
	contentBudget := maxBodyBytes - sysOverhead - 2048
	if contentBudget < 2048 {
		contentBudget = 2048
	}

	for attempt := 0; attempt < 8; attempt++ {
		chatReq.Content = translator.TruncateUTF8HeadTail(origContent, contentBudget)
		if strings.TrimSpace(chatReq.Content) == "" {
			chatReq.Content = "…"
		}
		curLen = marshaledLen(chatReq)
		if curLen <= maxBodyBytes {
			return chatReq
		}
		contentBudget = contentBudget * 70 / 100
		if contentBudget < 1024 {
			chatReq.Content = translator.TruncateUTF8Tail(origContent, 1024)
			break
		}
	}
	return chatReq
}

func clampExtractedForStudio(cleanModel string, extracted *translator.ExtractedRequest) {
	if extracted == nil {
		return
	}
	limit := 64000
	if info, ok := stepstudio.GetModelInfo(cleanModel); ok {
		if info.MaxTokens > 0 {
			limit = info.MaxTokens
		}
		if !info.SupportsThinking {
			extracted.Thinking = false
			extracted.Effort = ""
		} else if extracted.Thinking && len(info.AllowedEfforts) > 0 {
			allowed := false
			for _, candidate := range info.AllowedEfforts {
				if strings.EqualFold(candidate, extracted.Effort) {
					extracted.Effort = candidate
					allowed = true
					break
				}
			}
			if !allowed {
				if strings.EqualFold(extracted.Effort, "xhigh") {
					extracted.Effort = info.AllowedEfforts[len(info.AllowedEfforts)-1]
				} else if info.DefaultEffort != "" {
					extracted.Effort = info.DefaultEffort
				} else {
					extracted.Effort = info.AllowedEfforts[0]
				}
			}
		}
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
	if isSafetyReviewError(err) {
		msg := "StepFun Studio 内容安全审核未通过（safety review failed），请稍后重试或改写措辞后重试。"
		safetyErr := pluginabi.NewError("upstream_safety_review", msg, http.StatusServiceUnavailable)
		safetyErr.Retryable = true
		return safetyErr
	}
	if isConcurrencyLimitError(err) {
		// Return 503 without quota keywords so CPA Core does not place the
		// Studio credential into a long quota cooldown on transient burst traffic.
		busyErr := pluginabi.NewError("upstream_busy", "StepFun Studio 并发通道繁忙 ("+apiErr.Message+")，请稍后重试", http.StatusServiceUnavailable)
		busyErr.Retryable = true
		return busyErr
	}
	if status == http.StatusTooManyRequests {
		return pluginabi.NewError("upstream_rate_limited", "StepFun 上游限流: "+apiErr.Message, http.StatusTooManyRequests)
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

func (p *Plugin) extractCredentialsWithSource(storageJSON []byte, metadata map[string]any) (token, webID, authFile string) {
	var accID string
	if len(metadata) > 0 {
		if id, ok := metadata["account_id"].(string); ok {
			accID = strings.TrimSpace(id)
		}
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
			if accID == "" && payload.AccountID != "" {
				accID = strings.TrimSpace(payload.AccountID)
			}
			if token == "" && payload.OasisToken != "" {
				token = strings.TrimSpace(payload.OasisToken)
			}
			if webID == "" && payload.WebID != "" {
				webID = strings.TrimSpace(payload.WebID)
			}
		}
	}
	oauthDir := resolveOAuthDir()
	if files, err := filepath.Glob(filepath.Join(oauthDir, "step2api-*.json")); err == nil && len(files) > 0 {
		for _, f := range files {
			data, errRead := os.ReadFile(f)
			if errRead != nil {
				continue
			}
			var item AuthFilePayload
			if json.Unmarshal(data, &item) != nil {
				continue
			}
			if (accID != "" && item.AccountID == accID) || (webID != "" && item.WebID == webID) || authFile == "" {
				if authFile == "" || (accID != "" && item.AccountID == accID) {
					authFile = f
				}
				if token == "" && item.OasisToken != "" {
					token = strings.TrimSpace(item.OasisToken)
				}
				if webID == "" && item.WebID != "" {
					webID = strings.TrimSpace(item.WebID)
				}
			}
		}
	}
	if webID == "" && token != "" {
		webID = stepstudio.ExtractDeviceID(token)
	}
	cacheKey := webID
	if cacheKey == "" {
		cacheKey = authFile
	}
	if cacheKey != "" {
		p.tokenCacheMu.RLock()
		cached := p.tokenCache[cacheKey]
		p.tokenCacheMu.RUnlock()
		if cached != "" {
			token = cached
		}
	}
	return token, webID, authFile
}

func (p *Plugin) rememberFreshToken(prevToken, newToken, webID, authFile string) {
	newToken = strings.TrimSpace(newToken)
	if newToken == "" || newToken == prevToken {
		return
	}
	cacheKey := webID
	if cacheKey == "" {
		cacheKey = authFile
	}
	if cacheKey != "" {
		p.tokenCacheMu.Lock()
		if p.tokenCache == nil {
			p.tokenCache = make(map[string]string)
		}
		p.tokenCache[cacheKey] = newToken
		p.tokenCacheMu.Unlock()
	}
	if authFile != "" {
		p.saveTokenToDisk(authFile, newToken, webID)
	}
}

func (p *Plugin) extractCredentials(storageJSON []byte, metadata map[string]any) (token, webID string) {
	token, webID, _ = p.extractCredentialsWithSource(storageJSON, metadata)
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

func streamWithConcurrencyRetry(
	ctx context.Context,
	client *stepstudio.Client,
	convID string,
	req stepstudio.PlaygroundChatRequest,
) (<-chan stepstudio.StudioEvent, <-chan error, error) {
	waits := []time.Duration{1500 * time.Millisecond, 2500 * time.Millisecond, 4 * time.Second, 6 * time.Second}
	eventsCh, errCh, err := client.StreamPlayground(ctx, convID, req)
	for i := 0; err != nil && isConcurrencyLimitError(err) && i < len(waits); i++ {
		timer := time.NewTimer(waits[i])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
		eventsCh, errCh, err = client.StreamPlayground(ctx, convID, req)
	}
	return eventsCh, errCh, err
}

func startPlaygroundWithFallback(
	ctx context.Context,
	client *stepstudio.Client,
	convID string,
	rawModel string,
	rawPayload []byte,
	baseReq stepstudio.PlaygroundChatRequest,
	extracted *translator.ExtractedRequest,
) (<-chan stepstudio.StudioEvent, <-chan error, error) {
	fitted := fitPlaygroundChatRequest(convID, baseReq, extracted.BaseSystem, extracted.ToolProtocol, maxStudioRequestBodyBytes)
	eventsCh, errCh, err := streamWithConcurrencyRetry(ctx, client, convID, fitted)
	if err != nil && isSafetyReviewError(err) {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(800 * time.Millisecond):
		}
		eventsCh, errCh, err = streamWithConcurrencyRetry(ctx, client, convID, fitted)
		if err != nil && isSafetyReviewError(err) {
			soft := extracted.SoftToolProtocol
			if soft == "" {
				soft = extracted.ToolProtocol
			}
			softReq := baseReq
			softReq.EnableWebSearch = false
			softReq = fitPlaygroundChatRequest(convID, softReq, "", soft, 300*1024)
			eventsCh, errCh, err = streamWithConcurrencyRetry(ctx, client, convID, softReq)
		}
	}
	if err != nil && isInvalidBodyError(err) {
		dumpInvalidBodyDebug(rawModel, rawPayload, fitted)
		retryReq := baseReq
		retryReq.EnableWebSearch = false
		retryReq = fitPlaygroundChatRequest(convID, retryReq, extracted.BaseSystem, extracted.ToolProtocol, 450*1024)
		eventsCh, errCh, err = streamWithConcurrencyRetry(ctx, client, convID, retryReq)
		if err != nil && isInvalidBodyError(err) {
			retryReq2 := baseReq
			retryReq2.EnableWebSearch = false
			retryReq2 = fitPlaygroundChatRequest(convID, retryReq2, "", extracted.ToolProtocol, 150*1024)
			eventsCh, errCh, err = streamWithConcurrencyRetry(ctx, client, convID, retryReq2)
		}
	}
	return eventsCh, errCh, err
}

func (p *Plugin) handleExecutorExecuteStream(raw []byte) (any, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode executor.execute_stream request: %w", err)
	}

	token, webID, authFile := p.extractCredentialsWithSource(req.StorageJSON, req.AuthMetadata)
	if token == "" {
		return nil, pluginabi.NewError("unauthorized", "no StepFun Oasis-Token found in credentials", http.StatusUnauthorized)
	}

	cleanModel, effortSuffix := splitModelAndEffortSuffix(CleanModelName(req.Model, p.Config().ModelPrefix))
	extracted, err := translator.ParseOpenAIRequest(req.Payload, p.Config().DefaultThinking, p.Config().DefaultEffort)
	if err != nil {
		return nil, fmt.Errorf("parse request payload: %w", err)
	}
	applyModelEffortHint(extracted, effortSuffix)
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
	p.rememberFreshToken(token, client.GetToken(), webID, authFile)

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

	eventsCh, errCh, err := startPlaygroundWithFallback(ctx, client, convID, req.Model, req.Payload, chatReq, extracted)
	if err != nil {
		cancel()
		if !isConcurrencyLimitError(err) {
			p.noteStudioUpstreamFailure(cleanModel, err)
		}
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
	trans.SetTools(extracted.Tools)

	go func() {
		earlyCut := false
		defer func() {
			if !earlyCut {
				cancel()
			}
		}()
		for ev := range eventsCh {
			chunks := trans.TranslateEvent(ev)
			for _, chunk := range chunks {
				_ = p.emitStreamChunk(streamID, chunk)
			}
			if trans.Done() {
				earlyCut = true
				_ = p.closeStream(streamID, nil)
				go drainStudioStreamQuietly(eventsCh, errCh, cancel, upstreamBackgroundDrainTimeout)
				return
			}
		}
		if sErr := <-errCh; sErr != nil {
			if !isConcurrencyLimitError(sErr) {
				p.noteStudioUpstreamFailure(cleanModel, sErr)
			}
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

	token, webID, authFile := p.extractCredentialsWithSource(req.StorageJSON, req.AuthMetadata)
	if token == "" {
		return nil, pluginabi.NewError("unauthorized", "no StepFun Oasis-Token found in credentials", http.StatusUnauthorized)
	}

	cleanModel, effortSuffix := splitModelAndEffortSuffix(CleanModelName(req.Model, p.Config().ModelPrefix))
	extracted, err := translator.ParseOpenAIRequest(req.Payload, p.Config().DefaultThinking, p.Config().DefaultEffort)
	if err != nil {
		return nil, err
	}
	applyModelEffortHint(extracted, effortSuffix)
	clampExtractedForStudio(cleanModel, extracted)

	client := stepstudio.NewClient(p.Config().BaseURL, token, webID, nil)
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Minute)
	earlyCut := false
	defer func() {
		if !earlyCut {
			cancel()
		}
	}()
	assetIDs, err := uploadTurnImages(ctx, client, extracted.Images)
	if err != nil {
		return nil, pluginabi.NewError("image_upload_failed", "上传图片到 StepFun Studio 失败: "+err.Error(), http.StatusBadGateway)
	}

	convID, err := client.CreateConversation(ctx)
	if err != nil {
		return nil, err
	}
	p.rememberFreshToken(token, client.GetToken(), webID, authFile)

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

	eventsCh, errCh, err := startPlaygroundWithFallback(ctx, client, convID, req.Model, req.Payload, chatReq, extracted)
	if err != nil {
		if !isConcurrencyLimitError(err) {
			p.noteStudioUpstreamFailure(cleanModel, err)
		}
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
	activeToolBlocks := map[int]bool{}
	suppressedBlocks := map[int]bool{}
	var droppedTools []string
	completedDeclaredTools := 0

eventLoop:
	for ev := range eventsCh {
		switch ev.Type {
		case stepstudio.EventContentBlockStart:
			var d stepstudio.ContentBlockStartData
			_ = json.Unmarshal(ev.Data, &d)
			if d.BlockType == "CONTENT_BLOCK_TYPE_TOOL_CALL" {
				activeToolBlocks[d.BlockIndex] = true
				break
			}
			if completedDeclaredTools > 0 && len(activeToolBlocks) == 0 {
				earlyCut = true
				go drainStudioStreamQuietly(eventsCh, errCh, cancel, upstreamBackgroundDrainTimeout)
				break eventLoop
			}
		case stepstudio.EventTextDelta:
			if completedDeclaredTools > 0 && len(activeToolBlocks) == 0 {
				earlyCut = true
				go drainStudioStreamQuietly(eventsCh, errCh, cancel, upstreamBackgroundDrainTimeout)
				break eventLoop
			}
			var d stepstudio.DeltaData
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				contentBuilder.WriteString(d.Delta)
			}
		case stepstudio.EventReasoningDelta:
			if completedDeclaredTools > 0 && len(activeToolBlocks) == 0 {
				earlyCut = true
				go drainStudioStreamQuietly(eventsCh, errCh, cancel, upstreamBackgroundDrainTimeout)
				break eventLoop
			}
			var d stepstudio.DeltaData
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				reasoningBuilder.WriteString(d.Delta)
			}
		case stepstudio.EventToolCallStart:
			var d stepstudio.ToolCallStartData
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				if d.ToolName != "" && !translator.ToolDeclared(extracted.Tools, d.ToolName) {
					delete(activeToolBlocks, d.BlockIndex)
					suppressedBlocks[d.BlockIndex] = true
					droppedTools = append(droppedTools, d.ToolName)
					break
				}
				activeToolBlocks[d.BlockIndex] = true
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
				if suppressedBlocks[d.BlockIndex] {
					break
				}
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
		case stepstudio.EventContentBlockStop:
			var d struct {
				BlockIndex int `json:"block_index"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			delete(activeToolBlocks, d.BlockIndex)
			if suppressedBlocks[d.BlockIndex] {
				delete(suppressedBlocks, d.BlockIndex)
				if completedDeclaredTools == 0 && len(activeToolBlocks) == 0 {
					earlyCut = true
					go drainStudioStreamQuietly(eventsCh, errCh, cancel, upstreamBackgroundDrainTimeout)
					break eventLoop
				}
				break
			}
			if _, isTool := nativeToolIndex[d.BlockIndex]; isTool {
				completedDeclaredTools++
			}
		case stepstudio.EventFinal:
			var d stepstudio.FinalData
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				finalUsage = d.Usage
			}
		}
	}

	if !earlyCut {
		if sErr := <-errCh; sErr != nil {
			if !isConcurrencyLimitError(sErr) {
				p.noteStudioUpstreamFailure(cleanModel, sErr)
			}
			if pErr := pluginErrorFromUpstreamErr(sErr); pErr != nil {
				return nil, pErr
			}
			return nil, sErr
		}
	}

	finishReason := "stop"
	content := contentBuilder.String()
	var toolCalls []map[string]any
	for _, call := range nativeToolCalls {
		if call.Name != "" && !translator.ToolDeclared(extracted.Tools, call.Name) {
			droppedTools = append(droppedTools, call.Name)
			continue
		}
		item := translator.OpenAIToolCall(call.Name, translator.NormalizeToolArguments(call.Name, call.Args, extracted.Tools))
		if call.ID != "" {
			item["id"] = call.ID
		}
		toolCalls = append(toolCalls, item)
	}
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
		content = ""
	} else if call, ok := translator.ParseToolCallTextWithTools(content, extracted.Tools); ok && translator.ToolDeclared(extracted.Tools, call.Name) {
		finishReason = "tool_calls"
		toolCalls = []map[string]any{translator.OpenAIToolCall(call.Name, call.Arguments)}
		content = ""
	} else if len(droppedTools) > 0 {
		seen := map[string]bool{}
		var unique []string
		for _, name := range droppedTools {
			if name != "" && !seen[name] {
				seen[name] = true
				unique = append(unique, name)
			}
		}
		finishReason = "stop"
		notice := fmt.Sprintf("（上游尝试调用内置工具 %s，本环境没有这些工具，该调用已忽略。请改用当前会话声明的工具重试。）", strings.Join(unique, ", "))
		if strings.TrimSpace(content) != "" {
			content = strings.TrimRight(content, "\r\n") + "\n\n" + notice
		} else {
			content = notice
		}
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
