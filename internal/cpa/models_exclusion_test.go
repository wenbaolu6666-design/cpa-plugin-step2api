package cpa

import (
	"encoding/json"
	"testing"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginabi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
)

func TestModelExclusionGlobal(t *testing.T) {
	p := NewPlugin(nil)

	req := pluginapi.StaticModelRequest{
		Host: pluginapi.HostConfigSummary{
			ExcludedModels: map[string][]string{
				ProviderID: {"step-3.5-flash", "step-audio-*"},
			},
		},
	}
	raw, _ := json.Marshal(req)

	res, err := p.HandleMethod(pluginabi.MethodModelStatic, raw)
	if err != nil {
		t.Fatalf("handleModelStatic failed: %v", err)
	}
	resp, ok := res.(pluginapi.ModelResponse)
	if !ok {
		t.Fatalf("expected ModelResponse, got %T", res)
	}

	for _, m := range resp.Models {
		if m.ID == "step-3.5-flash" || m.ID == "step-audio-tts" || m.ID == "step-audio-asr" {
			t.Errorf("model %s should have been excluded", m.ID)
		}
	}
}

func TestModelExclusionAccountAndDisabled(t *testing.T) {
	p := NewPlugin(nil)

	// 1. Account excluded models
	storageData := AuthFilePayload{
		AccountID:      "test-account",
		Disabled:       false,
		ExcludedModels: []string{"step-3.7-flash", "step-router-v1"},
	}
	rawStorage, _ := json.Marshal(storageData)

	req := pluginapi.AuthModelRequest{
		AuthID:      "test-account",
		StorageJSON: rawStorage,
	}
	raw, _ := json.Marshal(req)

	res, err := p.HandleMethod(pluginabi.MethodModelForAuth, raw)
	if err != nil {
		t.Fatalf("handleModelForAuth failed: %v", err)
	}
	resp, ok := res.(pluginapi.ModelResponse)
	if !ok {
		t.Fatalf("expected ModelResponse, got %T", res)
	}

	for _, m := range resp.Models {
		if m.ID == "step-3.7-flash" || m.ID == "step-router-v1" {
			t.Errorf("model %s should have been excluded for account", m.ID)
		}
	}

	// 2. Account disabled
	storageDataDisabled := AuthFilePayload{
		AccountID: "test-account",
		Disabled:  true,
	}
	rawStorageDisabled, _ := json.Marshal(storageDataDisabled)
	reqDisabled := pluginapi.AuthModelRequest{
		AuthID:      "test-account",
		StorageJSON: rawStorageDisabled,
	}
	rawDisabled, _ := json.Marshal(reqDisabled)

	resDis, errDis := p.HandleMethod(pluginabi.MethodModelForAuth, rawDisabled)
	if errDis != nil {
		t.Fatalf("handleModelForAuth disabled failed: %v", errDis)
	}
	respDis, okDis := resDis.(pluginapi.ModelResponse)
	if !okDis {
		t.Fatalf("expected ModelResponse, got %T", resDis)
	}
	if len(respDis.Models) != 0 {
		t.Errorf("disabled account must return 0 models, got %d", len(respDis.Models))
	}
}
