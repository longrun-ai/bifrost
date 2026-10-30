package ltsp

import (
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// CacheBody adds only a missing request-level TTL on supported OpenAI Responses
// families. Legacy retention, explicit TTLs and content-level breakpoints survive.
func CacheBody(path string, body []byte) []byte {
	if !strings.HasSuffix(path, "/responses") {
		return body
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	var model string
	if json.Unmarshal(v["model"], &model) != nil {
		return body
	}
	parts := strings.SplitN(model, "/", 2)
	if len(parts) == 2 {
		if parts[0] != "openai" {
			return body
		}
		model = parts[1]
	}
	// Explicitly supported API model families. Extend only after official support.
	supported := false
	for _, prefix := range []string{"gpt-5.6", "gpt-5.7", "gpt-5.8", "gpt-5.9", "gpt-6"} {
		if model == prefix || strings.HasPrefix(model, prefix+"-") || strings.HasPrefix(model, prefix+".") {
			supported = true
		}
	}
	if !supported {
		return body
	}
	if _, ok := v["prompt_cache_retention"]; ok {
		return body
	}
	if _, ok := v["prompt_cache_ttl"]; ok {
		return body
	}
	options := map[string]json.RawMessage{}
	if raw, ok := v["prompt_cache_options"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &options) != nil || options == nil {
			return body
		}
	}
	if _, ok := options["ttl"]; ok {
		return body
	}
	options["ttl"] = json.RawMessage(`"12h"`)
	v["prompt_cache_options"], _ = json.Marshal(options)
	b, e := json.Marshal(v)
	if e != nil {
		return body
	}
	return b
}

func (m *Manager) Defaults(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		if !m.claimProbe(ctx) {
			return
		}
		if ctx.IsPost() {
			old := ctx.PostBody()
			body := CacheBody(string(ctx.Path()), old)
			if len(body) > 0 {
				ctx.Request.SetBody(body)
			}
		}
		next(ctx)
	}
}

// Native context conversion handles explicit session IDs and harness IDs first.
func IPSession(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) {
	bifrostCtx.SetValue(schemas.BifrostContextKeySessionID, "ltsp-ip:"+ctx.RemoteIP().String())
}
