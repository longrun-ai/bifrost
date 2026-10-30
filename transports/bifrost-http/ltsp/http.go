package ltsp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type probeOriginKey struct{}

const AccessKeyName = "ltsp-lan-clients"

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

// Shell serves the account home at the root without changing workspace routes.
func (m *Manager) Shell(ctx *fasthttp.RequestCtx, body []byte) []byte {
	if string(ctx.Path()) == "/" {
		return m.AccountsPage
	}
	link := `<a href="/" target="_blank" rel="noopener noreferrer" style="position:fixed;right:24px;bottom:20px;z-index:9999;background:#315ac8;color:white;border-radius:9px;padding:9px 14px;font:14px system-ui;text-decoration:none">账号与客户端接入</a>`
	return []byte(strings.Replace(string(body), "</body>", link+"</body>", 1))
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
	type groupView struct {
		Account
		Balance    string    `json:"balance"`
		RetryStart time.Time `json:"retry_start"`
		RetryEnd   time.Time `json:"retry_end"`
	}
	m.mu.Lock()
	groups := make([]groupView, 0, len(m.accounts))
	now := m.clock()
	for _, a := range m.accounts {
		a.KeyIDs = append([]string{}, a.KeyIDs...)
		a.Busy = m.busy[a.ID]
		v := groupView{Account: a, Balance: "unknown"}
		for _, s := range m.schedules {
			if s.Provider == a.Provider {
				v.RetryStart, v.RetryEnd = RetryWindow(a.RefreshDate, a.Cycle, now, s)
			}
		}
		groups = append(groups, v)
	}
	schedules := append([]ProviderSchedule(nil), m.schedules...)
	m.mu.Unlock()
	reply(ctx, 200, map[string]any{"accounts": groups, "keys": keys, "provider_schedules": schedules, "access_key_name": AccessKeyName})
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
	if m.Keys != nil {
		owners := map[string]string{}
		for _, p := range []string{"openai", "anthropic"} {
			for _, k := range m.Keys(p) {
				owners[k.ID] = p
			}
		}
		for _, a := range s.Accounts {
			for _, id := range a.KeyIDs {
				if owner, known := owners[id]; known && owner != a.Provider {
					reply(ctx, 400, map[string]string{"error": "Bifrost 账号与账号组须属于同一上游"})
					return
				}
			}
		}
	}
	if e := m.Replace(s.Accounts, s.ProviderSchedules); e != nil {
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
