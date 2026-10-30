package ltsp

import (
	"encoding/json"
	"github.com/maximhq/bifrost/core/schemas"
	"testing"
)

func TestOpenAICacheUsageRoundtripAndMerge(t *testing.T) {
	var d schemas.ResponsesResponseInputTokens
	if e := json.Unmarshal([]byte(`{"cached_tokens":10,"cache_write_tokens":8,"cache_write_tokens_details":{"ttl_30m_tokens":3,"ttl_12h_tokens":5}}`), &d); e != nil {
		t.Fatal(e)
	}
	if d.CachedReadTokens != 10 || d.CachedWriteTokens != 8 || d.CachedWriteTokenDetails.TTL12hTokens != 5 {
		t.Fatalf("%+v", d)
	}
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	var wire struct {
		Details schemas.OpenAICacheWriteTokenDetails `json:"cache_write_tokens_details"`
	}
	if e = json.Unmarshal(b, &wire); e != nil || wire.Details.TTL12hTokens != 5 || wire.Details.TTL30mTokens != 3 {
		t.Fatal(string(b), e)
	}
	usage := &schemas.BifrostLLMUsage{PromptTokens: 20, PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedWriteTokens: 8, CachedWriteTokenDetails: d.CachedWriteTokenDetails}}
	merged := schemas.MergeBifrostLLMUsage(usage, usage)
	if merged.PromptTokensDetails.CachedWriteTokenDetails.TTL12hTokens != 10 || merged.PromptTokensDetails.CachedWriteTokenDetails.TTL30mTokens != 6 {
		t.Fatal(merged)
	}
	var marker schemas.PromptCacheBreakpoint
	json.Unmarshal([]byte(`{"mode":"explicit","ttl":"30m"}`), &marker)
	if marker.TTL == nil || *marker.TTL != "30m" {
		t.Fatal("per-block TTL lost")
	}
}
