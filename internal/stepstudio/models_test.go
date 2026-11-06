package stepstudio

import (
	"testing"
)

func TestGetModelInfo(t *testing.T) {
	m, ok := GetModelInfo("step-3.5-flash")
	if !ok {
		t.Fatal("expected step-3.5-flash to exist")
	}
	if !m.SupportsThinking {
		t.Error("expected step-3.5-flash to support thinking")
	}

	m2, ok := GetModelInfo("step2api/step-5-preview")
	if !ok || m2.ID != "step-5-preview" {
		t.Errorf("failed to match prefixed step-5-preview")
	}
}
