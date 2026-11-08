package cpa

import (
	"os"
	"path/filepath"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginabi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
	"github.com/WenPaul/cpa-plugin-step2api/internal/translator"
	"github.com/google/uuid"
)

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

	client := stepstudio.NewClient(p.Config().BaseURL, token, webID, nil)

	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Minute)
	convID, err := client.CreateConversation(ctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create upstream conversation failed: %w", err)
	}

	turnText := translator.RenderTranscript(extracted.Messages)
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
		AssetIDs:        []string{},
		EnableWebSearch: extracted.EnableWebSearch,
	}

	eventsCh, errCh, err := client.StreamPlayground(ctx, convID, chatReq)
	if err != nil {
		cancel()
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
			pErr := pluginabi.NewError("upstream_stream_error", sErr.Error(), http.StatusBadGateway)
			_ = p.closeStream(streamID, pErr)
			return
		}
		if !trans.SentFinish {
			stop := "stop"
			if chunk := trans.FinalChunk(&stop, nil); chunk != nil {
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

	client := stepstudio.NewClient(p.Config().BaseURL, token, webID, nil)
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Minute)
	defer cancel()

	convID, err := client.CreateConversation(ctx)
	if err != nil {
		return nil, err
	}

	turnText := translator.RenderTranscript(extracted.Messages)
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
		AssetIDs:        []string{},
		EnableWebSearch: extracted.EnableWebSearch,
	}

	eventsCh, errCh, err := client.StreamPlayground(ctx, convID, chatReq)
	if err != nil {
		return nil, err
	}

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder

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
		}
	}

	if sErr := <-errCh; sErr != nil {
		return nil, sErr
	}

	responseObj := map[string]any{
		"id":      "chatcmpl-" + uuid.NewString()[:8],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   cleanModel,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":              "assistant",
					"content":           contentBuilder.String(),
					"reasoning_content": reasoningBuilder.String(),
				},
				"finish_reason": "stop",
			},
		},
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
