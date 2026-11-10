package cpa

import (
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	PluginID   = "step2api"
	PluginName = "Step2API"
	ProviderID = "step2api"
	Version    = "1.0.18"
)

type PluginConfig struct {
	Enabled               bool   `yaml:"enabled"`
	BaseURL               string `yaml:"base_url"`
	Proxy                 string `yaml:"proxy"`
	ModelPrefix           string `yaml:"model_prefix"`
	RouteUnprefixedModels bool   `yaml:"route_unprefixed_models"`
	DefaultThinking       bool   `yaml:"default_thinking"`
	DefaultEffort         string `yaml:"default_effort"`
	SyncAuthFiles         bool   `yaml:"sync_auth_files"`
	DataDir               string `yaml:"data_dir"`
}

func DefaultPluginConfig() PluginConfig {
	return PluginConfig{
		Enabled:               true,
		BaseURL:               "https://studio.stepfun.com",
		ModelPrefix:           "step2api/",
		RouteUnprefixedModels: true,
		DefaultThinking:       true,
		DefaultEffort:         "medium",
		SyncAuthFiles:         true,
		DataDir:               "~/.step2api",
	}
}

func ParsePluginConfig(rawYAML []byte) (PluginConfig, error) {
	cfg := DefaultPluginConfig()
	if len(rawYAML) == 0 {
		return cfg, nil
	}

	var root struct {
		Plugins struct {
			Configs struct {
				Step2API PluginConfig `yaml:"step2api"`
			} `yaml:"configs"`
		} `yaml:"plugins"`
	}

	if err := yaml.Unmarshal(rawYAML, &root); err == nil && root.Plugins.Configs.Step2API.BaseURL != "" {
		c := root.Plugins.Configs.Step2API
		if c.ModelPrefix == "" {
			c.ModelPrefix = cfg.ModelPrefix
		}
		if c.DefaultEffort == "" {
			c.DefaultEffort = cfg.DefaultEffort
		}
		return c, nil
	}

	_ = yaml.Unmarshal(rawYAML, &cfg)
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://studio.stepfun.com"
	}
	return cfg, nil
}

func CleanModelName(model, prefix string) string {
	m := strings.TrimSpace(model)
	if prefix != "" && strings.HasPrefix(m, prefix) {
		m = strings.TrimPrefix(m, prefix)
	}
	m = strings.TrimPrefix(m, "step2api/")
	m = strings.TrimPrefix(m, "stepstudio/")
	m = strings.TrimPrefix(m, "step/")
	return m
}
