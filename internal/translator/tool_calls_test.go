package translator

import "testing"

func TestParseToolCallText(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "compact", text: `{"name":"get_weather","arguments":{"city":"北京"}}`},
		{name: "fenced", text: "```json\n{\"name\":\"get_weather\",\"arguments\":{\"city\":\"北京\"}}\n```"},
		{name: "openai_shape", text: `{"function":{"name":"get_weather","arguments":{"city":"北京"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call, ok := ParseToolCallText(tc.text)
			if !ok {
				t.Fatalf("ParseToolCallText failed for %q", tc.text)
			}
			if call.Name != "get_weather" || call.Arguments != `{"city":"北京"}` {
				t.Fatalf("unexpected call: %+v", call)
			}
		})
	}
}

func TestParseToolCallTextRejectsProse(t *testing.T) {
	if _, ok := ParseToolCallText("当前环境无法调用工具，请稍后再试。"); ok {
		t.Fatal("prose must not be parsed as a tool call")
	}
	if _, ok := ParseToolCallText(`{"name":"get_weather","arguments":`); ok {
		t.Fatal("incomplete JSON must not be parsed as a tool call")
	}
}
