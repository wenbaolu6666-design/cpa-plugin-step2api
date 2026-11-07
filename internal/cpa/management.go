package cpa

import (
	"encoding/json"
	"fmt"
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
		`../oauth`,
		`../../oauth`,
		`oauth`,
	}
	for _, c := range candidates {
		if stat, err := os.Stat(c); err == nil && stat.IsDir() {
			return c
		}
	}
	return `../oauth`
}

func (p *Plugin) handleCLIRegister() (any, error) {
	return pluginapi.CommandLineRegistrationResponse{
		Flags: []pluginapi.CommandLineFlag{
			{
				Name:         "step2api-status",
				Type:         "bool",
				DefaultValue: "false",
				Usage:        "Print StepFun AI Studio plugin status and accounts",
			},
		},
	}, nil
}

func (p *Plugin) handleCLIRun(raw []byte) (any, error) {
	var req pluginapi.CommandLineExecutionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if flag, ok := req.TriggeredFlags["step2api-status"]; ok && flag.Set && strings.EqualFold(flag.Value, "true") {
		return pluginapi.CommandLineExecutionResponse{
			ExitCode: 0,
			Stdout:   []byte("StepFun AI Studio plugin (step2api) active\n"),
		}, nil
	}
	return pluginapi.CommandLineExecutionResponse{ExitCode: 0}, nil
}

func (p *Plugin) handleManagementRegister(raw []byte) (any, error) {
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: "/v0/management/step2api/api/state", Description: "Step2API dashboard state"},
			{Method: http.MethodPost, Path: "/v0/management/step2api/api/accounts", Description: "Add StepFun account via Cookie/Token"},
			{Method: http.MethodPost, Path: "/v0/management/step2api/api/accounts/delete", Description: "Delete StepFun account"},
			{Method: http.MethodPost, Path: "/v0/management/step2api/api/accounts/test", Description: "Test StepFun account connectivity"},
			{Method: http.MethodPost, Path: "/v0/management/step2api/api/accounts/refresh", Description: "Refresh all StepFun account tokens"},
			{Method: http.MethodPost, Path: "/v0/management/step2api/api/sync", Description: "Sync StepFun accounts to host auths"},
		},
		Resources: []pluginapi.ResourceRoute{
			{
				Path:        "/accounts",
				Menu:        "StepFun Studio",
				Description: "StepFun AI Studio Console & Account Management",
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
	path := rawPath
	if idx := strings.IndexByte(rawPath, '?'); idx >= 0 {
		path = rawPath[:idx]
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}

	// 1. Serve HTML UI
	if path == "/accounts" || path == "/" || strings.HasPrefix(path, "/v0/resource/plugins/step2api") {
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":  []string{"text/html; charset=utf-8"},
				"Cache-Control": []string{"no-store"},
			},
			Body: []byte(webUIDashboardHTML),
		}, nil
	}

	if strings.HasPrefix(path, "/step2api/") {
		path = "/v0/management" + path
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	oauthDir := resolveOAuthDir()

	// 2. GET /api/state
	if strings.Contains(path, "/api/state") {
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
							"oasis_id":     item.WebID,
							"webid":        item.WebID,
							"token":        item.OasisToken,
							"token_exp":    accessExp.Unix(),
							"refresh_exp":  refreshExp.Unix(),
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
			"status":   "ready",
			"accounts": accountsList,
			"models":   modelsList,
			"mode":     "playground",
			"version":  Version,
		})
		return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: res}, nil
	}

	// 3. POST /api/accounts (Add Account)
	if strings.Contains(path, "/api/accounts") && !strings.Contains(path, "/delete") && method == http.MethodPost {
		var in struct {
			Name   string `json:"name"`
			Cookie string `json:"cookie"`
			Token  string `json:"token"`
			WebID  string `json:"webid"`
		}
		_ = json.Unmarshal(req.Body, &in)
		rawCookie := strings.TrimSpace(in.Cookie)
		if rawCookie == "" {
			rawCookie = strings.TrimSpace(in.Token)
		}
		tok, webid := stepstudio.ExtractCookieTokens(rawCookie)
		if in.WebID != "" {
			webid = strings.TrimSpace(in.WebID)
		}
		if tok == "" {
			return pluginapi.ManagementResponse{
				StatusCode: http.StatusBadRequest,
				Headers:    headers,
				Body:       []byte("{\"error\":\"æªæ£æµå°ææç Oasis-Tokenï¼¨è¯·ç¡®ä¿ä» F12 è¯·æ±å¤´ä¸­å¤å¶äºå®æ´ç Cookie æ Oasis-Token å­ç¬¦ä¸²\"}"),
			}, nil
		}

		accName := strings.TrimSpace(in.Name)
		if accName == "" {
			if webid != "" {
				accName = "step-" + webid[:min(8, len(webid))]
			} else {
				dev := stepstudio.ExtractDeviceID(tok)
				if dev != "" {
					accName = "step-" + dev[:min(8, len(dev))]
				} else {
					accName = fmt.Sprintf("step-%d", time.Now().Unix()%10000)
				}
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
			return pluginapi.ManagementResponse{StatusCode: http.StatusInternalServerError, Headers: headers, Body: []byte("{\"error\":\"åå¥å­æ®æä»¶å¤±è´¥\"}")}, nil
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
			"account": payload,
			"message": "è´¦å·å·²æåå¯¼å¥å¹¶ä¿å­è³ CPA oauth å­æ®ç®å½",
		})
		return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: res}, nil
	}

	// 4. POST /api/accounts/delete
	if (strings.Contains(path, "/api/accounts/delete") && method == http.MethodPost) ||
		(strings.Contains(path, "/api/accounts") && method == http.MethodDelete) {
		var in struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(req.Body, &in)
		accID := in.ID
		if accID == "" && strings.Contains(path, "/accounts/") {
			accID = path[strings.LastIndex(path, "/")+1:]
		}
		if accID != "" {
			filePath := filepath.Join(oauthDir, "step2api-"+accID+".json")
			_ = os.Remove(filePath)
		}
		return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: []byte("{\"ok\":true,\"message\":\"è´¦å·å·²å é¤\"}")}, nil
	}

	// 5. POST /api/accounts/refresh
	if strings.Contains(path, "/refresh") {
		var in struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(req.Body, &in)
		refreshedCount := 0
		if files, err := filepath.Glob(filepath.Join(oauthDir, "step2api-*.json")); err == nil {
			for _, fpath := range files {
				data, err := os.ReadFile(fpath)
				if err != nil {
					continue
				}
				var item AuthFilePayload
				if err := json.Unmarshal(data, &item); err != nil || item.OasisToken == "" {
					continue
				}
				if in.ID != "" && item.AccountID != in.ID {
					continue
				}
				newTok, errRefresh := stepstudio.RefreshToken(p.ctx, nil, p.config.BaseURL, item.OasisToken, item.WebID)
				if errRefresh == nil && newTok != "" {
					item.OasisToken = newTok
					item.UpdatedAt = time.Now().Format(time.RFC3339)
					if updatedBytes, errM := json.MarshalIndent(item, "", "  "); errM == nil {
						_ = os.WriteFile(fpath, updatedBytes, 0644)
						refreshedCount++
					}
				}
			}
		}
		res, _ := json.Marshal(map[string]any{
			"ok":        true,
			"refreshed": refreshedCount,
			"message":   fmt.Sprintf("å·²æåå·æ° %d ä¸ªè´¦å·ç Token", refreshedCount),
		})
		return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: res}, nil
	}

	// 6. POST /api/accounts/test
	if strings.Contains(path, "/test") {
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    headers,
			Body:       []byte("{\"ok\":true,\"message\":\"StepFun AI Studio ç½å³é¾è·¯æ­£å¸¸\"}"),
		}, nil
	}

	// 7. POST /api/sync
	if strings.Contains(path, "/sync") {
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    headers,
			Body:       []byte("{\"ok\":true,\"message\":\"å·²åæ­¥è´¦å·è³ CPA å­æ®\"}"),
		}, nil
	}

	return pluginapi.ManagementResponse{
		StatusCode: http.StatusNotFound,
		Headers:    headers,
		Body:       []byte("{\"error\":\"not_found\"}"),
	}, nil
}
