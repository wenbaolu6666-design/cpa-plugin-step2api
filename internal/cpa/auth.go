package cpa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
	"github.com/google/uuid"
)

type AuthFilePayload struct {
	Type           string   `json:"type"`
	Provider       string   `json:"provider,omitempty"`
	OAuthProvider  string   `json:"oauth_provider,omitempty"`
	AccountID      string   `json:"account_id,omitempty"`
	Name           string   `json:"name,omitempty"`
	Phone          string   `json:"phone,omitempty"`
	OasisToken     string   `json:"oasis_token,omitempty"`
	WebID          string   `json:"web_id,omitempty"`
	Disabled       bool     `json:"disabled,omitempty"`
	ExcludedModels []string `json:"excluded_models,omitempty"`
	UpdatedAt      string   `json:"updated_at,omitempty"`
}

type rpcIdentifierResponse struct {
	Identifier          string                        `json:"identifier"`
	Identifiers         []string                      `json:"identifiers,omitempty"`
	CredentialProviders []string                      `json:"credential_providers,omitempty"`
	Entries             []pluginapi.AuthProviderEntry `json:"entries,omitempty"`
}

type rpcAuthLoginStartRequest struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthLoginPollRequest struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthRefreshRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func (p *Plugin) handleAuthIdentifier() (any, error) {
	return rpcIdentifierResponse{
		Identifier:          ProviderID,
		Identifiers:         []string{ProviderID},
		CredentialProviders: []string{"step2api", "stepstudio", "stepfun"},
		Entries: []pluginapi.AuthProviderEntry{
			{
				Identifier: ProviderID,
				Name:       "Step2API",
				Logo:       "https://www.stepfun.com/step_favicon.svg",
			},
		},
	}, nil
}

func (p *Plugin) handleAuthParse(raw []byte) (any, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode auth.parse request: %w", err)
	}

	var payload AuthFilePayload
	if len(req.RawJSON) > 0 {
		_ = json.Unmarshal(req.RawJSON, &payload)
	}

	token := strings.TrimSpace(payload.OasisToken)
	if token == "" && len(req.RawJSON) > 0 {
		token, _ = stepstudio.ExtractCookieTokens(string(req.RawJSON))
	}

	accID := payload.AccountID
	if accID == "" {
		accID = payload.Phone
	}
	if accID == "" {
		accID = payload.Name
	}
	if accID == "" && token != "" {
		dev := stepstudio.ExtractDeviceID(token)
		if dev != "" {
			accID = "step-" + dev[:min(8, len(dev))]
		} else {
			accID = "step-account"
		}
	}

	metadata := map[string]any{
		"account_id":     accID,
		"oasis_token":    token,
		"web_id":         payload.WebID,
		"provider":       ProviderID,
		"oauth_provider": ProviderID,
		"auth_kind":      "oauth",
	}

	return pluginapi.AuthParseResponse{
		Handled: true,
		Auth: pluginapi.AuthData{
			Provider: ProviderID,
			ID:       accID,
			Label:    "StepFun Studio (" + accID + ")",
			Metadata: metadata,
			Attributes: map[string]string{
				"oauth_provider": ProviderID,
			},
		},
	}, nil
}

func (p *Plugin) handleAuthLoginStart(raw []byte) (any, error) {
	var req rpcAuthLoginStartRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	stateID := uuid.NewString()
	p.loginStateMu.Lock()
	p.loginStates[stateID] = &loginSessionState{
		CreatedAt: time.Now(),
		StateID:   stateID,
	}
	p.loginStateMu.Unlock()

	loginURL := fmt.Sprintf("http://127.0.0.1:8317/v0/resource/plugins/step2api/accounts?state=%s", stateID)
	// OAuth 入口本身不弹窗：按规范只返回账号页地址，由用户在该页点击
	// 「官方登录（无痕窗口）」或「添加账号」时才启动浏览器。
	return pluginapi.AuthLoginStartResponse{
		Provider:  ProviderID,
		URL:       loginURL,
		State:     stateID,
		ExpiresAt: time.Now().Add(15 * time.Minute),
		Metadata: map[string]any{
			"mode": "panel",
			"hint": "在账号页点击「官方登录（无痕窗口）」开始授权",
		},
	}, nil
}

func (p *Plugin) handleAuthLoginPoll(raw []byte) (any, error) {
	var req rpcAuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	p.loginStateMu.Lock()
	session, exists := p.loginStates[req.State]
	p.loginStateMu.Unlock()

	if !exists || session == nil {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "login session expired or not found",
		}, nil
	}

	if session.Done && session.Account != nil {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusSuccess,
			Message: "StepFun AI Studio 账号登录成功: " + session.Account.AccountID,
			Auth: pluginapi.AuthData{
				Provider: ProviderID,
				ID:       session.Account.AccountID,
				Label:    "StepFun Studio (" + session.Account.AccountID + ")",
				Metadata: map[string]any{
					"account_id":     session.Account.AccountID,
					"oasis_token":    session.Account.OasisToken,
					"web_id":         session.Account.WebID,
					"oauth_provider": ProviderID,
					"auth_kind":      "oauth",
				},
			},
		}, nil
	}

	// 只有本轮登录真正完成才算成功：历史账号文件存在不代表本次登录成功，
	// 否则控制台会一直显示“已完成”而不是“已有 N 个账号”。
	if current := p.browserLoginSnapshot(); current != nil && current.StartedAt.After(session.CreatedAt) {
		switch current.Status {
		case browserLoginError:
			return pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusError,
				Message: current.Message,
			}, nil
		case browserLoginCancelled:
			return pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusError,
				Message: "登录已取消",
			}, nil
		}
	}
	return pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusPending,
		Message: "等待导入 Cookie 或 Oasis-Token...",
	}, nil
}

func (p *Plugin) handleAuthRefresh(raw []byte) (any, error) {
	var req rpcAuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	token := p.extractToken(req.StorageJSON, req.Metadata)
	if token == "" {
		return nil, fmt.Errorf("no oasis_token in auth metadata or storage")
	}

	newTok, err := stepstudio.RefreshToken(context.Background(), nil, p.Config().BaseURL, token, "")
	if err != nil {
		return nil, fmt.Errorf("refresh token failed: %w", err)
	}

	meta := req.Metadata
	if meta == nil {
		meta = make(map[string]any)
	}
	meta["oasis_token"] = newTok
	meta["oauth_provider"] = ProviderID

	return pluginapi.AuthRefreshResponse{
		Auth: pluginapi.AuthData{
			Provider: ProviderID,
			ID:       req.AuthID,
			Label:    "StepFun Studio (" + req.AuthID + ")",
			Metadata: meta,
		},
		NextRefreshAfter: time.Now().Add(1 * time.Hour),
	}, nil
}
