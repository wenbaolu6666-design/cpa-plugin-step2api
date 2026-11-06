package cpa

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
)

func (p *Plugin) handleManagementRegister(raw []byte) (any, error) {
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{
				Method:      http.MethodGet,
				Path:        "/v0/management/step2api/status",
				Description: "StepFun AI Studio status",
			},
		},
		Resources: []pluginapi.ResourceRoute{
			{
				Path:        "accounts",
				Menu:        "StepFun Studio",
				Description: "StepFun AI Studio accounts & tokens",
			},
		},
	}, nil
}

func (p *Plugin) handleManagementHandle(raw []byte) (any, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	path := strings.ToLower(strings.TrimSpace(req.Path))
	headers := make(http.Header)

	if strings.Contains(path, "accounts") {
		headers.Set("Content-Type", "text/html; charset=utf-8")
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    headers,
			Body:       []byte(IndexHTML),
		}, nil
	}

	headers.Set("Content-Type", "application/json")
	res, _ := json.Marshal(map[string]any{
		"provider": ProviderID,
		"status":   "active",
	})
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    headers,
		Body:       res,
	}, nil
}
