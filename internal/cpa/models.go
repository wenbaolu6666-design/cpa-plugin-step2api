package cpa

import (
	"encoding/json"
	"strings"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
)

func (p *Plugin) handleModelStatic(raw []byte) (any, error) {
	models := stepstudio.BuildPluginStaticModels()
	return pluginapi.ModelResponse{
		Provider: ProviderID,
		Models:   models,
	}, nil
}

func (p *Plugin) handleModelForAuth(raw []byte) (any, error) {
	return p.handleModelStatic(raw)
}

func (p *Plugin) handleModelRoute(raw []byte) (any, error) {
	var req pluginapi.ModelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	modelLower := strings.ToLower(strings.TrimSpace(req.RequestedModel))
	prefixLower := strings.ToLower(strings.TrimSpace(p.Config().ModelPrefix))

	if prefixLower != "" && strings.HasPrefix(modelLower, prefixLower) {
		return pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetExecutor,
			Target:     ProviderID,
		}, nil
}

	if strings.HasPrefix(modelLower, "step2api/") || strings.HasPrefix(modelLower, "stepstudio/") {
		return pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetExecutor,
			Target:     ProviderID,
		}, nil
}

	if p.Config().RouteUnprefixedModels {
		if _, ok := stepstudio.GetModelInfo(modelLower); ok {
			return pluginapi.ModelRouteResponse{
				Handled:    true,
				TargetKind: pluginapi.ModelRouteTargetExecutor,
				Target:     ProviderID,
			}, nil
		}
		if strings.HasPrefix(modelLower, "step-") {
			return pluginapi.ModelRouteResponse{
				Handled:    true,
				TargetKind: pluginapi.ModelRouteTargetExecutor,
				Target:     ProviderID,
			}, nil
		}
	}

	return pluginapi.ModelRouteResponse{
		Handled: false,
	}, nil
}
