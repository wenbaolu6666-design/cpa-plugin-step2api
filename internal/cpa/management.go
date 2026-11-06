package cpa

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
)

func resolveOAuthDir() string {
	candidates := []string{
		`D:\WenPaul\Documents\路由网关\EasyCLIProxyAPI\oauth`,
		`../oauth`,
		`../../oauth`,
		`oauth`,
	}
	for _, c := range candidates {
		if stat, err := os.Stat(c); err == nil && stat.IsDir() {
			return c
		}
	}
	return `D:\WenPaul\Documents\路由网关\EasyCLIProxyAPI\oauth`
}

func (p *Plugin) handleManagementRegister(raw []byte) (any, error) {
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: "/v0/management/step2api/api/state", Description: "Step2API dashboard state"},
			{Method: http.MethodPost, Path: "/v0/management/step2api/api/accounts", Description: "Add StepFun account via Cookie/Token"},
			{Method: http.MethodPost, Path: "/v0/management/step2api/api/accounts/test", Description: "Test StepFun account connectivity"},
			{Method: http.MethodPost, Path: "/v0/management/step2api/api/accounts/refresh", Description: "Refresh all StepFun account tokens"},
		},
		Resources: []pluginapi.ResourceRoute{
			{
				Path:        "/accounts",
				Menu:        "StepFun Studio",
				Description: "StepFun AI Studio 控制台与账号管理",
			},
		},
	}, nil
}

func (p *Plugin) handleManagementHandle(raw []byte) (any, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	rawPath := strings.ToLower(strings.TrimSpace(req.Path))
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	headers := make(http.Header)

	// 1. Serve HTML UI
	if strings.Contains(rawPath, "/accounts") && !strings.Contains(rawPath, "/api/") {
		headers.Set("Content-Type", "text/html; charset=utf-8")
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    headers,
			Body:       []byte(webUIDashboardHTML),
		}, nil
	}

	headers.Set("Content-Type", "application/json")
	oauthDir := resolveOAuthDir()

	// 2. GET /api/state
	if strings.Contains(rawPath, "/api/state") {
		var accountsList []map[string]any
		if files, err := filepath.Glob(filepath.Join(oauthDir, "step2api-*.json")); err == nil {
			for _, fpath := range files {
				if data, err := os.ReadFile(fpath); err == nil {
					var item AuthFilePayload
					if err := json.Unmarshal(data, &item); err == nil {
						tok := item.OasisToken
						access, refresh, _ := stepstudio.SplitToken(tok)
						accessExp := stepstudio.GetTokenExpiration(access)
						refreshExp := stepstudio.GetTokenExpiration(refresh)
						refDays := 0.0
						if !refreshExp.IsZero() {
							refDays = time.Until(refreshExp).Hours() / 24.0
						}
						accountsList = append(accountsList, map[string]any{
							"id":           item.AccountID,
							"name":         item.Name,
							"webid":        item.WebID,
							"token":        item.OasisToken,
							"refresh_days": refDays,
							"access_exp":   accessExp.Format(time.RFC3339),
							"disabled":     false,
						})
					}
				}
			}
		}

		var modelsList []map[string]any
		for _, m := range stepstudio.SupportedModels {
			modelsList = append(modelsList, map[string]any{
				"id":                m.ID,
				"display_name":      m.DisplayName,
				"multimodal":        m.SupportsVision,
				"max_output_tokens": m.MaxTokens,
				"thinking": map[string]any{
					"effort_options": m.AllowedEfforts,
				},
			})
		}

		res, _ := json.Marshal(map[string]any{
			"accounts": accountsList,
			"models":   modelsList,
			"mode":     "playground",
			"keys":     []any{},
			"usage":    []any{},
		})
		return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: res}, nil
	}

	// 3. POST /api/accounts (Add Account)
	if strings.Contains(rawPath, "/api/accounts") && method == http.MethodPost {
		var in struct {
			Name   string `json:"name"`
			Cookie string `json:"cookie"`
			Token  string `json:"token"`
			WebID  string `json:"webid"`
		}
		_ = json.Unmarshal(req.Body, &in)
		rawCookie := in.Cookie
		if rawCookie == "" {
			rawCookie = in.Token
		}
		tok, webid := stepstudio.ExtractCookieTokens(rawCookie)
		if in.WebID != "" {
			webid = in.WebID
		}
		if tok == "" {
			return pluginapi.ManagementResponse{StatusCode: http.StatusBadRequest, Headers: headers, Body: []byte(`{"detail":"未检测到 Oasis-Token，请检查 Cookie 格式"}`)}, nil
		}

		accName := strings.TrimSpace(in.Name)
		if accName == "" {
			if webid != "" {
				accName = "step-" + webid[:min(8, len(webid))]
			} else {
				accName = "step-default"
			}
		}

		payload := AuthFilePayload{
			Type:       "step2api",
			Provider:   ProviderID,
			AccountID:  accName,
			Name:       accName,
			OasisToken: tok,
			WebID:      webid,
			UpdatedAt:  time.Now().Format(time.RFC3339),
		}
		data, _ := json.MarshalIndent(payload, "", "  ")
		filePath := filepath.Join(oauthDir, "step2api-"+accName+".json")
		if err := os.WriteFile(filePath, data, 0644); err != nil {
			return pluginapi.ManagementResponse{StatusCode: http.StatusInternalServerError, Headers: headers, Body: []byte(`{"detail":"保存凭据文件失账"}`)}, nil
		}

		p.loginStateMu.Lock()
		for _, st := range p.loginStates {
			st.Done = true
			st.Account = &payload
		}
		p.loginStateMu.Unlock()

		res, _ := json.Marshal(map[string]any{
			"ok":      true,
			"id":      accName,
			"updated": false,
		})
		return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: res}, nil
	}

	// 4. DELETE /api/accounts/?
	if strings.Contains(rawPath, "/api/accounts/") && method == http.MethodDelete {
		accID := rawPath[strings.LastIndex(rawPath, "/")+1:]
		_filePath := filepath.Join(oauthDir, "step2api-"+accID+".json")
		_ = os.Remove(_filePath)
		return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: []byte(`{"ok":true}`)}, nil
	}

	// 5. POST /api/accounts/test
	if strings.Contains(rawPath, "/test") {
		return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: []byte(`{"ok":true,"re_enabled":false}`)}, nil
	}

	// 6. POST /api/accounts/refresh
	if strings.Contains(rawPath, "/refresh") {
		return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: []byte(`{"ok":true,"items":[{"ok":true}]}`)}, nil
	}

	return pluginapi.ManagementResponse{
		StatusCode: http.StatusNotFound,
		Headers:    headers,
		Body:       []byte(`{"error":"not_found"}`),
	}, nil
}
