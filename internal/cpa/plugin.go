package cpa

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginabi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
)

type HostCallFunc func(method string, payload []byte) ([]byte, error)

type loginSessionState struct {
	CreatedAt time.Time
	StateID   string
	Done      bool
	Account   *AuthFilePayload
}

type rpcRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  rpcCapabilities    `json:"capabilities"`
}

type rpcCapabilities struct {
	ModelProvider          bool                         `json:"model_provider"`
	AuthProvider           bool                         `json:"auth_provider"`
	Scheduler              bool                         `json:"scheduler"`
	ModelRouter            bool                         `json:"model_router"`
	Executor               bool                         `json:"executor"`
	ExecutorModelScope     pluginapi.ExecutorModelScope `json:"executor_model_scope,omitempty"`
	ExecutorInputFormats   []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats  []string                     `json:"executor_output_formats,omitempty"`
	RequestLifecyclePlugin bool                         `json:"request_lifecycle_plugin"`
	CommandLinePlugin      bool                         `json:"command_line_plugin"`
	ManagementAPI          bool                         `json:"management_api"`
	QuotaProvider          bool                         `json:"quota_provider"`
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
		return p.handleQuotaIdentifier()
	case pluginabi.MethodQuotaDescribe:
		return p.handleQuotaDescribe()
	case pluginabi.MethodQuotaFetch:
		return p.handleQuotaFetch(raw)

	case pluginabi.MethodCommandLineRegister:
		return p.handleCLIRegister()
	case pluginabi.MethodCommandLineExecute:
		return p.handleCLIRun(raw)

	case pluginabi.MethodManagementRegister:
		return p.handleManagementRegister(raw)
	case pluginabi.MethodManagementHandle:
		return p.handleManagementHandle(raw)
	case pluginabi.MethodRequestComplete:
		return map[string]any{"completed": true}, nil

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

	return rpcRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             PluginID,
			Version:          Version,
			Author:           "WenPaul",
			GitHubRepository: "https://github.com/wenbaolu6666-design/cpa-plugin-step2api",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "base_url",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "https://studio.stepfun.com",
					Description: "StepFun AI Studio base URL",
				},
				{
					Name:        "default_effort",
					Type:        pluginapi.ConfigFieldTypeEnum,
					Default:     "medium",
					EnumValues:  []string{"low", "medium", "high", "xhigh"},
					Description: "Default thinking effort level for reasoning models",
				},
			},
		},
		Capabilities: rpcCapabilities{
			ModelProvider:          true,
			ModelRouter:            true,
			AuthProvider:           true,
			Executor:               true,
			ExecutorModelScope:     pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:   []string{"chat-completions", "responses", "anthropic"},
			ExecutorOutputFormats:  []string{"chat-completions", "responses", "anthropic"},
			RequestLifecyclePlugin: true,
			CommandLinePlugin:      true,
			ManagementAPI:          true,
			QuotaProvider:          true,
		},
	}, nil
}

func (p *Plugin) handleReload(raw []byte) (any, error) {
	return p.handleRegister(raw)
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
