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
	Type       string `json:"type"`
	Provider   string `json:"provider,omitempty"`
	AccountID  string `json:"account_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Phone      string `json:"phone,omitempty"`
	OasisToken string `json:"oasis_token,omitempty"`
	WebID      string `json:"web_id,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type rpcIdentifierResponse struct {
	Identifier          string   `json:"identifier"`
	Identifiers         []string `json:"identifiers,omitempty"`
	CredentialProviders []string `json:"credential_providers,omitempty"`
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
		Identifiers:         []string{"step2api", "stepstudio", "stepfun"},
		CredentialProviders: []string{"step2api", "stepstudio", "stepfun"},
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
		token = strings.Trim(string(req.RawJSON), "\" \t\r\n")
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
		"account_id":  accID,
		"oasis_token": token,
		"web_id":      payload.WebID,
		"provider":    ProviderID,
	}

	return pluginapi.AuthParseResponse{
		Handled: true,
		Auth: pluginapi.AuthData{
			Provider: ProviderID,
			ID:       accID,
			Label:    "StepFun Studio (" + accID + ")",
			Metadata: metadata,
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

	loginURL := "https://studio.stepfun.com/"
	return pluginapi.AuthLoginStartResponse{
		Provider:  ProviderID,
		URL:      loginURL,
		State:    stateID,
		ExpiresAt: time.Now().Add(15 * time.Minute),
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
					"account_id":  session.Account.AccountID,
					"oasis_token": session.Account.OasisToken,
					"web_id":      session.Account.WebID,
				},
			},
		}, nil
	}

	return pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusPending,
		Message: "等待登录中...",
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
