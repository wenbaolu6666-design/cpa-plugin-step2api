package stepstudio

import (
	"testing"
)

func TestSplitAndCombineToken(t *testing.T) {
	raw := "access_part...refresh_part"
	a, r, err := SplitToken(raw)
	if err != nil {
		t.Fatalf("SplitToken failed: %v", err)
	}
	if a != "access_part" || r != "refresh_part" {
		t.Errorf("unexpected parts: %s, %s", a, r)
	}

	combined := CombineToken(a, r)
	if combined != raw {
		t.Errorf("unexpected combined: %s", combined)
	}
}
