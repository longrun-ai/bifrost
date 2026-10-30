package ltsp

import (
	"encoding/json"
	"testing"
)

func TestCacheMissingValueOnly(t *testing.T) {
	for _, c := range []struct {
		path, body string
		want       string
	}{
		{"/v1/responses", `{"model":"openai/gpt-6.1-sol","input":"Hi"}`, "12h"},
		{"/openai/v1/responses", `{"model":"gpt-5.6","prompt_cache_options":{"mode":"explicit"}}`, "12h"},
		{"/v1/responses", `{"model":"gpt-5.6","prompt_cache_options":{"ttl":"30m","mode":"explicit"}}`, "30m"},
		{"/v1/responses", `{"model":"gpt-5.6","prompt_cache_retention":"24h"}`, ""},
		{"/v1/responses", `{"model":"anthropic/claude-test"}`, ""},
		{"/v1/responses", `{"model":"gpt-5.5"}`, ""},
		{"/v1/chat/completions", `{"model":"openai/gpt-6.1-sol"}`, ""},
	} {
		var v struct {
			Options struct {
				TTL  string `json:"ttl"`
				Mode string `json:"mode"`
			} `json:"prompt_cache_options"`
		}
		got := CacheBody(c.path, []byte(c.body))
		if e := json.Unmarshal(got, &v); e != nil {
			t.Fatal(e)
		}
		if v.Options.TTL != c.want {
			t.Errorf("%s => %s", c.body, got)
		}
		if c.want == "" && string(got) != c.body {
			t.Error("unrelated payload modified")
		}
	}
	body := []byte(`{"model":"gpt-5.6","prompt_cache_options":{"mode":"explicit"},"input":[{"role":"user","content":[{"type":"input_text","text":"prefix","prompt_cache_breakpoint":{"mode":"explicit","ttl":"30m"}}]}]}`)
	var before, after map[string]any
	json.Unmarshal(body, &before)
	json.Unmarshal(CacheBody("/v1/responses", body), &after)
	b, _ := json.Marshal(before["input"])
	a, _ := json.Marshal(after["input"])
	if string(a) != string(b) || after["prompt_cache_options"].(map[string]any)["mode"] != "explicit" {
		t.Fatal("explicit policy was lost")
	}
}

func BenchmarkCacheBody(b *testing.B) {
	for _, tc := range []struct{ name, path, body string }{
		{"unrelated", "/v1/chat/completions", `{"model":"openai/gpt-6.1-sol","input":"Hi"}`},
		{"default", "/v1/responses", `{"model":"openai/gpt-6.1-sol","input":"Hi"}`},
		{"explicit", "/v1/responses", `{"model":"openai/gpt-6.1-sol","prompt_cache_options":{"ttl":"30m"},"input":"Hi"}`},
	} {
		b.Run(tc.name, func(b *testing.B) {
			body := []byte(tc.body)
			b.ReportAllocs()
			for b.Loop() {
				CacheBody(tc.path, body)
			}
		})
	}
}
