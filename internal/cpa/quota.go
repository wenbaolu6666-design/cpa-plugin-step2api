package cpa

import (
	"encoding/json"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
)

func (p *Plugin) handleQuotaStatus(raw []byte) (any, error) {
	var req pluginapi.QuotaFetchRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	token := p.extractToken(req.StorageJSON, nil)
	if token == "" {
		return map[string]any{
			"status":  "missing_token",
			"message": "no Oasis-Token present",
		}, nil
	}

	access, refresh, _ := stepstudio.SplitToken(token)
	accessExp := stepstudio.GetTokenExpiration(access)
	refreshExp := stepstudio.GetTokenExpiration(refresh)

	var accessCountdown, refreshCountdown string
	if !accessExp.IsZero() {
		accessCountdown = time.Until(accessExp).Round(time.Second).String()
	}
	if !refreshExp.IsZero() {
		refreshCountdown = time.Until(refreshExp).Round(time.Hour).String()
	}

	return map[string]any{
		"provider":            ProviderID,
		"status":            "healthy",
		"access_exp":          accessExp.Format(time.RFC3339),
		"refresh_exp":         refreshExp.Format(time.RFC3339),
		"access_countdown":    accessCountdown,
		"refresh_countdown":   refreshCountdown,
		"needs_refresh":       stepstudio.NeedsRefresh(token, 180*time.Second),
		"available_models":    len(stepstudio.SupportedModels),
	}, nil
}
