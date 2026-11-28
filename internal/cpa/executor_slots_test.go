package cpa

import (
	"context"
	"testing"
	"time"
)

func TestAcquireStudioSlotWaitsThenFails(t *testing.T) {
	plugin := NewPlugin(nil)
	plugin.config.StudioMaxConcurrency = 1
	plugin.config.StudioSlotWaitSeconds = 1

	release, err := plugin.acquireStudioSlot(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	start := time.Now()
	if _, err := plugin.acquireStudioSlot(context.Background()); err == nil {
		t.Fatal("expected timeout while the only slot is held")
	}
	if waited := time.Since(start); waited < 900*time.Millisecond {
		t.Fatalf("expected the second acquire to wait, returned after %s", waited)
	}

	release()
	release2, err := plugin.acquireStudioSlot(context.Background())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}

func TestStudioSlotChannelRebuildsOnConfigChange(t *testing.T) {
	plugin := NewPlugin(nil)
	plugin.config.StudioMaxConcurrency = 2
	first := plugin.studioSlotChannel()
	if cap(first) != 2 {
		t.Fatalf("cap = %d, want 2", cap(first))
	}
	plugin.config.StudioMaxConcurrency = 3
	second := plugin.studioSlotChannel()
	if cap(second) != 3 {
		t.Fatalf("cap after reload = %d, want 3", cap(second))
	}
	if first == second {
		t.Fatal("expected a new channel after the limit changed")
	}
}
