package translator

import (
	"strings"
	"testing"

	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
)

func TestStreamTranslatorConvertsTextJSONToToolCall(t *testing.T) {
	tr := NewStreamTranslator("step-5-preview")
	for _, delta := range []string{`{"name":"get_weather",`, `"arguments":{"city":"北京"}}`} {
		chunks := tr.TranslateEvent(stepstudio.StudioEvent{
			Type: stepstudio.EventTextDelta,
			Data: []byte(`{"delta":` + quoteJSON(delta) + `}`),
		})
		if len(chunks) != 0 {
			t.Fatalf("tool JSON must be buffered, got %q", string(chunks[0]))
		}
	}

	chunks := tr.FinalChunks("stop", nil)
	if len(chunks) != 2 {
		t.Fatalf("final chunks = %d, want 2", len(chunks))
	}
	toolFrame := string(chunks[0])
	for _, want := range []string{`"tool_calls"`, `"name":"get_weather"`, `"arguments":"{\"city\":\"北京\"}"`} {
		if !strings.Contains(toolFrame, want) {
			t.Errorf("tool frame missing %s: %s", want, toolFrame)
		}
	}
	if strings.Contains(toolFrame, `"content":"{`) {
		t.Errorf("tool JSON leaked as content: %s", toolFrame)
	}
	if finish := string(chunks[1]); !strings.Contains(finish, `"finish_reason":"tool_calls"`) {
		t.Errorf("unexpected finish frame: %s", finish)
	}
}

func TestStreamTranslatorStillStreamsNormalText(t *testing.T) {
	tr := NewStreamTranslator("step-5-preview")
	chunks := tr.TranslateEvent(stepstudio.StudioEvent{
		Type: stepstudio.EventTextDelta,
		Data: []byte(`{"delta":"你好"}`),
	})
	if len(chunks) != 1 || !strings.Contains(string(chunks[0]), `"content":"你好"`) {
		t.Fatalf("normal text should stream immediately: %q", chunks)
	}
}

func quoteJSON(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + replacer.Replace(value) + `"`
}
