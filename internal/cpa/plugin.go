package cpa

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginabi"
)

type HostCallFunc func(method string, payload []byte) ([]byte, error)

type loginSessionState struct {
	CreatedAt time.Time
	StateID   string
	Done      bool
	Account   *AuthFilePayload
}

type Plugin struct {
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	config       PluginConfig
	hostCall     HostCallFunc
	loginStateMu sync.Mutex
	loginStates  map[string]*loginSessionState
}

func NewPlugin(hostCall HostCallFunc) *Plugin {
	ctx, cancel := context.WithCancel(context.Background())
	return &Plugin{
		ctx:         ctx,
		cancel:      cancel,
		hostCall:    hostCall,
		config:      DefaultPluginConfig(),
		loginStates: make(map[string]*loginSessionState),
	}
}

func (p *Plugin) SetHostCall(fn HostCallFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hostCall = fn
}

func (p *Plugin) Config() PluginConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

func (p *Plugin) HandleMethod(method string, raw []byte) (any, error) {
	switch method {
	case pluginabi.MethodPluginRegister:
		return p.handleRegister(raw)
	case pluginabi.MethodPluginReconfigure, "plugin.reload":
		return p.handleReload(raw)
	case pluginabi.MethodPluginQuiesce:
		return map[string]any{"quiesced": true}, nil
	case pluginabi.MethodPluginShutdown:
		p.cancel()
		return map[string]any{"shutdown": true}, nil

	case pluginabi.MethodAuthIdentifier:
		return p.handleAuthIdentifier()
	case pluginabi.MethodAuthParse:
		return p.handleAuthParse(raw)
	case pluginabi.MethodAuthLoginStart:
		return p.handleAuthLoginStart(raw)
	case pluginabi.MethodAuthLoginPoll:
		return p.handleAuthLoginPoll(raw)
	case pluginabi.MethodAuthRefresh:
		return p.handleAuthRefresh(raw)

	case pluginabi.MethodModelStatic:
		return p.handleModelStatic(raw)
	case pluginabi.MethodModelForAuth:
		return p.handleModelForAuth(raw)
	case pluginabi.MethodModelRoute:
		return p.handleModelRoute(raw)

	case pluginabi.MethodExecutorIdentifier:
		return p.handleExecutorIdentifier()
	case pluginabi.MethodExecutorExecute:
		return p.handleExecutorExecute(raw)
	case pluginabi.MethodExecutorExecuteStream:
		return p.handleExecutorExecuteStream(raw)
	case pluginabi.MethodExecutorCountTokens:
		return p.handleExecutorCountTokens(raw)

	case pluginabi.MethodQuotaIdentifier:
		return p.handleExecutorIdentifier()
	case pluginabi.MethodQuotaDescribe, pluginabi.MethodQuotaFetch:
		return p.handleQuotaStatus(raw)

	case pluginabi.MethodManagementRegister:
		return p.handleManagementRegister(raw)
	case pluginabi.MethodManagementHandle:
		return p.handleManagementHandle(raw)

	default:
		return nil, fmt.Errorf("unknown method: %s", method)
	}
}

func (p *Plugin) handleRegister(raw []byte) (any, error) {
	if len(raw) > 0 {
		var in struct {
			ConfigYAML []byte `json:"config_yaml"`
		}
		if err := json.Unmarshal(raw, &in); err == nil && len(in.ConfigYAML) > 0 {
			if cfg, err := ParsePluginConfig(in.ConfigYAML); err == nil {
				p.mu.Lock()
				p.config = cfg
				p.mu.Unlock()
			}
		}
	}

	return map[string]any{
		"schema_version": pluginabi.SchemaVersion,
		"metadata": map[string]any{
			"id":          PluginID,
			"name":        PluginName,
			"version":     Version,
			"author":      "WenPaul",
			"description": "StepFun AI Studio (studio.stepfun.com) native CLIProxyAPI plugin. Proxies StepFun Playground & Reasoning models into OpenAI/Claude/Responses compatible endpoints.",
		},
		"capabilities": map[string]any{
			"model_provider": true,
			"model_router":   true,
			"auth_provider":  true,
			"executor":       true,
			"quota_provider": true,
			"management_api": true,
		},
	}, nil
}

func (p *Plugin) handleReload(raw []byte) (any, error) {
	if len(raw) > 0 {
		var in struct {
			ConfigYAML []byte `json:"config_yaml"`
		}
		if err := json.Unmarshal(raw, &in); err == nil && len(in.ConfigYAML) > 0 {
			if cfg, err := ParsePluginConfig(in.ConfigYAML); err == nil {
				p.mu.Lock()
				p.config = cfg
				p.mu.Unlock()
			}
		}
	}
	return map[string]any{"reloaded": true}, nil
}

func (p *Plugin) callHost(method string, req any) ([]byte, error) {
	if p.hostCall == nil {
		return nil, fmt.Errorf("hostCall is nil")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	rawResp, err := p.hostCall(method, payload)
	if err != nil {
		return nil, err
	}
	return rawResp, nil
}

func (p *Plugin) emitStreamChunk(streamID string, payload []byte) error {
	req := map[string]any{
		"stream_id": streamID,
		"payload":   payload,
	}
	_, err := p.callHost(pluginabi.MethodHostStreamEmit, req)
	return err
}

func (p *Plugin) closeStream(streamID string, streamErr *pluginabi.Error) error {
	req := map[string]any{
		"stream_id": streamID,
	}
	if streamErr != nil {
		req["error"] = streamErr.Message
		req["error_detail"] = streamErr
	}
	_, err := p.callHost(pluginabi.MethodHostStreamClose, req)
	return err
}
