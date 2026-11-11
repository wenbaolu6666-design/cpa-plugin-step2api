package translator

import (
	"strings"
	"testing"

	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
)

func TestFinalEventEmitsUsage(t *testing.T) {
	tr := NewStreamTranslator("step-5-preview")
	chunks := tr.TranslateEvent(stepstudio.StudioEvent{
		Type: stepstudio.EventFinal,
		Data: []byte(`{"message_id":"m1","usage":{"tokens_in":"1431","tokens_out":"64","cached_tokens":"1024"}}`),
	})
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	frame := string(chunks[0])
	for _, want := range []string{`"prompt_tokens":1431`, `"completion_tokens":64`, `"cached_tokens":1024`, `"finish_reason":"stop"`} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %s: %s", want, frame)
		}
	}
}
