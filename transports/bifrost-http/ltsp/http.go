package ltsp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type probeOriginKey struct{}

func ProbeURL(ctx context.Context) string {
	url, _ := ctx.Value(probeOriginKey{}).(string)
	return url
}

// The probe uses the native HTTP admission/logging pipeline. Its single-use
// marker grants only the exact account/key recovery exception, never credentials.
func (m *Manager) ProbeToken(ctx context.Context) (string, func()) {
	p, ok := ctx.Value(probeKey{}).(probeIdentity)
	if !ok {
		return "", func() {}
	}
	token := rand.Text()
	m.mu.Lock()
	m.probeTokens[token] = p
	m.mu.Unlock()
	return token, func() { m.mu.Lock(); delete(m.probeTokens, token); m.mu.Unlock() }
}

func (m *Manager) claimProbe(ctx *fasthttp.RequestCtx) bool {
	token := string(ctx.Request.Header.Peek("X-LTSP-Probe"))
	if token == "" {
		return true
	}
	ctx.Request.Header.Del("X-LTSP-Probe")
	m.mu.Lock()
	p, ok := m.probeTokens[token]
	delete(m.probeTokens, token)
	m.mu.Unlock()
	if !ok {
		reply(ctx, 400, map[string]string{"error": "invalid internal probe marker"})
		return false
	}
	ctx.SetUserValue(probeKey{}, p)
	return true
}

func (m *Manager) AttachProbe(ctx *fasthttp.RequestCtx, target *schemas.BifrostContext) {
	if p, ok := ctx.UserValue(probeKey{}).(probeIdentity); ok {
		target.SetValue(probeKey{}, p)
	}
}

func (m *Manager) Register(r *router.Router, middleware ...schemas.BifrostHTTPMiddleware) {
	chain := func(h fasthttp.RequestHandler) fasthttp.RequestHandler {
		for i := len(middleware) - 1; i >= 0; i-- {
			h = middleware[i](h)
		}
		return h
	}
	r.GET("/ltsp/accounts", chain(func(ctx *fasthttp.RequestCtx) {
		ctx.SetContentType("text/html; charset=utf-8")
		ctx.SetBody(m.AccountsPage)
	}))
	r.GET("/api/ltsp/accounts", chain(m.get))
	r.PUT("/api/ltsp/accounts", chain(m.put))
	r.POST("/api/ltsp/accounts/{id}/probe", chain(m.probe))
}

func reply(ctx *fasthttp.RequestCtx, status int, v any) {
	b, e := json.Marshal(v)
	if e != nil {
		ctx.Error("encoding failed", 500)
		return
	}
	ctx.SetStatusCode(status)
	ctx.SetContentType("application/json")
	ctx.SetBody(b)
}

// LAN management deliberately has no login. Browser mutations still require
// same-origin JSON requests, so visiting an unrelated website cannot manage keys.
func sameOrigin(ctx *fasthttp.RequestCtx) bool {
	origin := string(ctx.Request.Header.Peek("Origin"))
	if origin != "" && origin != "http://"+string(ctx.Host()) && origin != "https://"+string(ctx.Host()) {
		reply(ctx, 403, map[string]string{"error": "拒绝跨站管理请求"})
		return false
	}
	if !strings.HasPrefix(string(ctx.Request.Header.ContentType()), "application/json") {
		reply(ctx, 415, map[string]string{"error": "需要 application/json"})
		return false
	}
	if string(ctx.Request.Header.Peek("Sec-Fetch-Site")) == "cross-site" {
		reply(ctx, 403, map[string]string{"error": "拒绝跨站管理请求"})
		return false
	}
	return true
}

func (m *Manager) get(ctx *fasthttp.RequestCtx) {
	type keyView struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	keys := map[string][]keyView{}
	if m.Keys != nil {
		for _, p := range []string{"openai", "anthropic"} {
			for _, k := range m.Keys(p) {
				keys[p] = append(keys[p], keyView{k.ID, k.Name, k.Enabled == nil || *k.Enabled})
			}
		}
	}
	reply(ctx, 200, map[string]any{"accounts": m.Snapshot(), "keys": keys, "timezone": m.zone.String(), "balance": "unknown"})
}

func (m *Manager) put(ctx *fasthttp.RequestCtx) {
	if !sameOrigin(ctx) {
		return
	}
	if len(ctx.PostBody()) > 64*1024 {
		reply(ctx, 413, map[string]string{"error": "配置过大"})
		return
	}
	var s diskState
	if e := json.Unmarshal(ctx.PostBody(), &s); e != nil {
		reply(ctx, 400, map[string]string{"error": "无效 JSON"})
		return
	}
	if e := m.Replace(s.Accounts); e != nil {
		reply(ctx, 400, map[string]string{"error": e.Error()})
		return
	}
	m.get(ctx)
}

func (m *Manager) probe(ctx *fasthttp.RequestCtx) {
	if !sameOrigin(ctx) {
		return
	}
	id, _ := ctx.UserValue("id").(string)
	a, e := m.ProbeNow(context.WithValue(ctx, probeOriginKey{}, "http://"+ctx.LocalAddr().String()), id)
	if e != nil {
		reply(ctx, 409, map[string]string{"error": e.Error()})
		return
	}
	// HTTP 200 means the probe completed, not that upstream funding succeeded.
	reply(ctx, 200, map[string]any{"account": a, "recovered": !a.Exhausted && a.LastResult == "调用成功，额度可用", "result": a.LastResult})
}
