package cpa

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginabi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
)

type hostRPCEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}

func unwrapHostResult(raw []byte) json.RawMessage {
	var env hostRPCEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Result) > 0 {
		return env.Result
	}
	return raw
}

type hostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

func (p *Plugin) hostAuthList() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := p.callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if err != nil {
		return nil, err
	}
	var resp hostAuthListResponse
	if err := json.Unmarshal(unwrapHostResult(raw), &resp); err != nil {
		return nil, err
	}
	return resp.Files, nil
}

func (p *Plugin) hostAuthModels() ([]pluginapi.HostAuthModels, error) {
	raw, err := p.callHost(pluginabi.MethodHostAuthModels, pluginapi.HostAuthModelsRequest{})
	if err != nil {
		return nil, err
	}
	var resp pluginapi.HostAuthModelsResponse
	if err := json.Unmarshal(unwrapHostResult(raw), &resp); err != nil {
		return nil, err
	}
	return resp.Credentials, nil
}

func isOwnProviderKey(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "step2api", "stepstudio", "stepfun":
		return true
	}
	return false
}

func hostAuthUsable(entry pluginapi.HostAuthFileEntry) bool {
	if entry.Disabled || entry.Unavailable {
		return false
	}
	if !entry.NextRetryAfter.IsZero() && entry.NextRetryAfter.After(time.Now()) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(entry.Status)) {
	case "", "active", "healthy", "ok", "ready":
		return true
	}
	return false
}

// otherProviderServesModelUsably reports whether a credential outside this plugin
// currently serves model and is usable right now. It only reports true on positive
// evidence so a failing host callback never causes this plugin to hijack traffic.
func (p *Plugin) otherProviderServesModelUsably(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	credentials, err := p.hostAuthModels()
	if err != nil {
		return false
	}
	auths, _ := p.hostAuthList()
	byIndex := make(map[string]pluginapi.HostAuthFileEntry, len(auths))
	byID := make(map[string]pluginapi.HostAuthFileEntry, len(auths))
	for _, a := range auths {
		if idx := strings.TrimSpace(a.AuthIndex); idx != "" {
			byIndex[idx] = a
		}
		if id := strings.TrimSpace(a.ID); id != "" {
			byID[id] = a
		}
	}
	for _, cred := range credentials {
		if isOwnProviderKey(cred.Provider) {
			continue
		}
		serves := false
		for _, m := range cred.Models {
			candidate := strings.ToLower(strings.TrimSpace(m))
			if candidate == model {
				serves = true
				break
			}
			if strings.HasSuffix(candidate, "/"+model) {
				serves = true
				break
			}
		}
		if !serves {
			continue
		}
		entry, ok := byIndex[strings.TrimSpace(cred.AuthIndex)]
		if !ok {
			entry, ok = byID[strings.TrimSpace(cred.ID)]
		}
		if !ok {
			continue
		}
		if hostAuthUsable(entry) {
			return true
		}
	}
	return false
}
