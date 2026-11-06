package stepstudio

import (
	"strings"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
)

type ModelInfo struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"display_name"`
	Description       string   `json:"description"`
	MaxTokens         int      `json:"max_tokens"`
	ContextWindow     int      `json:"context_window"`
	SupportsThinking  bool    `json:"supports_thinking"`
	DefaultEffort     string   `json:"default_effort"`
	AllowedEfforts    []string `json:"allowed_efforts"`
	SupportsVision    bool    `json:"supports_vision"`
	SupportsTools     bool    `json:"supports_tools"`
	SupportsWebSearch bool    `json:"supports_web_search"`
}

var SupportedModels = map[string]ModelInfo{
	"step-3.5-flash": {
		ID:                "step-3.5-flash",
		DisplayName:       "Step 3.5 Flash",
		Description:       "StepFun 快速高性价比模型，支持长上下文与思考模式",
		MaxTokens:         64000,
		ContextWindow:     256000,
		SupportsThinking:  true,
		DefaultEffort:     "medium",
		AllowedEfforts:    []string{"low", "medium", "high"},
		SupportsVision:    true,
		SupportsTools:     true,
		SupportsWebSearch: true,
	},
	"step-3.7-flash": {
		ID:                "step-3.7-flash",
		DisplayName:       "Step 3.7 Flash",
		Description:       "StepFun 最新极速思考模型，极低首字延迟",
		MaxTokens:         64000,
		ContextWindow:     256000,
		SupportsThinking:  true,
		DefaultEffort:     "medium",
		AllowedEfforts:    []string{"low", "medium", "high", "xhigh"},
		SupportsVision:    true,
		SupportsTools:     true,
		SupportsWebSearch: true,
	},
	"step-5-preview": {
		ID:                "step-5-preview",
		DisplayName:       "Step 5 Preview",
		Description:       "StepFun 旗舰级高阶推理与逻辑模型",
		MaxTokens:         64000,
		ContextWindow:     256000,
		SupportsThinking:  true,
		DefaultEffort:     "high",
		AllowedEfforts:    []string{"low", "medium", "high", "xhigh"},
		SupportsVision:    true,
		SupportsTools:     true,
		SupportsWebSearch: true,
	},
	"step-router-v1": {
		ID:                "step-router-v1",
		DisplayName:       "Step Router v1",
		Description:       "StepFun 智能路由调度模型",
		MaxTokens:         8192,
		ContextWindow:     128000,
		SupportsThinking:  false,
		SupportsVision:    false,
		SupportsTools:     false,
		SupportsWebSearch: false,
	},
	"step-audio-tts": {
		ID:                "step-audio-tts",
		DisplayName:       "Step Audio TTS",
		Description:       "StepFun 语音合成模型",
		MaxTokens:         4096,
		ContextWindow:     32000,
		SupportsThinking:  false,
		SupportsVision:    false,
		SupportsTools:     false,
		SupportsWebSearch: false,
	},
	"step-audio-asr": {
		ID:                "step-audio-asr",
		DisplayName:       "Step Audio ASR",
		Description:       "StepFun 语音听写与转录模型",
		MaxTokens:         4096,
		ContextWindow:     32000,
		SupportsThinking:  false,
		SupportsVision:    false,
		SupportsTools:     false,
		SupportsWebSearch: false,
	},
}

func GetModelInfo(modelName string) (ModelInfo, bool) {
	name := strings.TrimSpace(strings.ToLower(modelName))
	name = strings.TrimPrefix(name, "step2api/")
	name = strings.TrimPrefix(name, "stepstudio/")
	name = strings.TrimPrefix(name, "step/")

	info, ok := SupportedModels[name]
	if ok {
		return info, true
	}
	for id, m := range SupportedModels {
		if strings.EqualFold(id, name) {
			return m, true
		}
	}
	return ModelInfo{}, false
}

func CleanModelName(modelName string) string {
	name := strings.TrimSpace(strings.ToLower(modelName))
	name = strings.TrimPrefix(name, "step2api/")
	name = strings.TrimPrefix(name, "stepstudio/")
	name = strings.TrimPrefix(name, "step/")
	if _, ok := SupportedModels[name]; ok {
		return name
	}
	return name
}

func BuildPluginStaticModels() []pluginapi.ModelInfo {
	var list []pluginapi.ModelInfo
	for _, m := range SupportedModels {
		list = append(list, pluginapi.ModelInfo{
			ID:     m.ID,
			Object: "model",
			OwnedBy: "stepfun",
		})
	}
	return list
}
