package cpa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
)

type rpcStaticModelRequest struct {
	pluginapi.StaticModelRequest
}

type rpcAuthModelRequest struct {
	pluginapi.AuthModelRequest
}

func matchesPattern(model string, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	model = strings.ToLower(strings.TrimSpace(model))
	if pattern == "" || pattern == "null" {
		return false
	}
	if pattern == "*" || pattern == model {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(model, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

func getGlobalExcludedRules(hostExclusions map[string][]string) []string {
	var rules []string
	for _, key := range []string{ProviderID, "step2api", "stepstudio", "stepfun"} {
		if list, ok := hostExclusions[key]; ok {
			rules = append(rules, list...)
		}
	}
	return rules
}

type localAccountInfo struct {
	AccountID      string
	Disabled       bool
	ExcludedModels []string
}

func loadLocalAccounts() []localAccountInfo {
	oauthDir := resolveOAuthDir()
	files, err := filepath.Glob(filepath.Join(oauthDir, "step2api-*.json"))
	if err != nil || len(files) == 0 {
		return nil
	}
	var accounts []localAccountInfo
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var item AuthFilePayload
		if err := json.Unmarshal(data, &item); err == nil {
			accounts = append(accounts, localAccountInfo{
				AccountID:      item.AccountID,
				Disabled:       item.Disabled,
				ExcludedModels: item.ExcludedModels,
			})
		}
	}
	return accounts
}

func isModelGloballyOrAllAccountsExcluded(modelID string, globalRules []string, accounts []localAccountInfo) bool {
	clean := strings.ToLower(strings.TrimSpace(modelID))
	for _, r := range globalRules {
		if matchesPattern(clean, r) {
			return true
		}
	}
	if len(accounts) == 0 {
		return false
	}
	enabledCount := 0
	excludedCount := 0
	for _, acc := range accounts {
		if acc.Disabled {
			continue
		}
		enabledCount++
		accExcluded := false
		for _, p := range acc.ExcludedModels {
			if matchesPattern(clean, p) {
				accExcluded = true
				break
			}
		}
		if accExcluded {
			excludedCount++
		}
	}
	if enabledCount == 0 {
		return true
	}
	return excludedCount == enabledCount
}

func (p *Plugin) handleModelStatic(raw []byte) (any, error) {
	var req rpcStaticModelRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	globalRules := getGlobalExcludedRules(req.Host.ExcludedModels)
	accounts := loadLocalAccounts()

	if len(accounts) > 0 {
		allDisabled := true
		for _, acc := range accounts {
			if !acc.Disabled {
				allDisabled = false
				break
			}
		}
		if allDisabled {
			return pluginapi.ModelResponse{
				Provider: ProviderID,
				Models:   []pluginapi.ModelInfo{},
			}, nil
		}
	}

	rawModels := stepstudio.BuildPluginStaticModels()
	var filtered []pluginapi.ModelInfo
	for _, m := range rawModels {
		if !isModelGloballyOrAllAccountsExcluded(m.ID, globalRules, accounts) {
			filtered = append(filtered, m)
		}
	}

	return pluginapi.ModelResponse{
		Provider: ProviderID,
		Models:   filtered,
	}, nil
}

func (p *Plugin) handleModelForAuth(raw []byte) (any, error) {
	var req rpcAuthModelRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	var payload AuthFilePayload
	if len(req.StorageJSON) > 0 {
		_ = json.Unmarshal(req.StorageJSON, &payload)
	}

	isDisabled := payload.Disabled
	if dis, ok := req.Metadata["disabled"].(bool); ok && dis {
		isDisabled = true
	}
	if isDisabled {
		return pluginapi.ModelResponse{
			Provider: ProviderID,
			Models:   []pluginapi.ModelInfo{},
		}, nil
	}

	globalRules := getGlobalExcludedRules(req.Host.ExcludedModels)
	var accRules []string
	accRules = append(accRules, globalRules...)
	accRules = append(accRules, payload.ExcludedModels...)

	rawModels := stepstudio.BuildPluginStaticModels()
	var filtered []pluginapi.ModelInfo
	for _, m := range rawModels {
		excluded := false
		for _, r := range accRules {
			if matchesPattern(m.ID, r) {
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, m)
		}
	}

	return pluginapi.ModelResponse{
		Provider: ProviderID,
		Models:   filtered,
	}, nil
}

func (p *Plugin) handleModelRoute(raw []byte) (any, error) {
	var req pluginapi.ModelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	modelLower := strings.ToLower(strings.TrimSpace(req.RequestedModel))
	prefixLower := strings.ToLower(strings.TrimSpace(p.Config().ModelPrefix))

	accounts := loadLocalAccounts()
	if len(accounts) > 0 {
		allDisabled := true
		for _, acc := range accounts {
			if !acc.Disabled {
				allDisabled = false
				break
			}
		}
		if allDisabled {
			return pluginapi.ModelRouteResponse{Handled: false}, nil
		}
	}

	cleanModel := CleanModelName(modelLower, prefixLower)
	if isModelGloballyOrAllAccountsExcluded(cleanModel, nil, accounts) {
		return pluginapi.ModelRouteResponse{Handled: false}, nil
	}

	if prefixLower != "" && strings.HasPrefix(modelLower, prefixLower) {
		return pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetSelf,
			Target:     PluginID,
		}, nil
	}

	if strings.HasPrefix(modelLower, "step2api/") || strings.HasPrefix(modelLower, "stepstudio/") {
		return pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetSelf,
			Target:     PluginID,
		}, nil
	}

	if p.Config().RouteUnprefixedModels {
		_, known := stepstudio.GetModelInfo(cleanModel)
		if known || strings.HasPrefix(cleanModel, "step-") {
			// Take over an unprefixed step model only with positive evidence that the
			// other channel cannot serve it right now, or when no other provider
			// publishes the model at all. A healthy Step Plan keeps the traffic.
			hasOtherProvider := false
			for _, provider := range req.AvailableProviders {
				if !isOwnProviderKey(provider) {
					hasOtherProvider = true
					break
				}
			}
			if p.modelBlocked(cleanModel) || (!hasOtherProvider && !p.otherProviderServesModelUsably(cleanModel)) {
				return pluginapi.ModelRouteResponse{
					Handled:    true,
					TargetKind: pluginapi.ModelRouteTargetSelf,
					Target:     PluginID,
				}, nil
			}
			return pluginapi.ModelRouteResponse{Handled: false}, nil
		}
	}

	return pluginapi.ModelRouteResponse{
		Handled: false,
	}, nil
}
