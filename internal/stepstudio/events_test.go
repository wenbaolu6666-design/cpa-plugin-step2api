package stepstudio

import (
	"encoding/json"
	"testing"
)

func TestFinalDataParsesStringUsage(t *testing.T) {
	var data FinalData
	err := json.Unmarshal([]byte(`{"message_id":"m1","usage":{"tokens_in":"1431","tokens_out":"64","cached_tokens":"1024"}}`), &data)
	if err != nil {
		t.Fatalf("unmarshal final data: %v", err)
	}
	if data.Usage == nil {
		t.Fatal("usage is nil")
	}
	if data.Usage.TokensIn != 1431 || data.Usage.TokensOut != 64 || data.Usage.CachedTokens != 1024 {
		t.Fatalf("unexpected usage: %+v", data.Usage)
	}
}
