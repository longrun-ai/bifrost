package ltsp

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestProbeMarkerIsSingleUseAndBoundToAccountKey(t *testing.T) {
	m := manager(t)
	c := context.WithValue(context.Background(), probeKey{}, probeIdentity{"one", "k1"})
	token, done := m.ProbeToken(c)
	defer done()
	var req fasthttp.RequestCtx
	req.Request.Header.Set("X-LTSP-Probe", token)
	if !m.claimProbe(&req) || len(req.Request.Header.Peek("X-LTSP-Probe")) != 0 {
		t.Fatal("probe marker was not consumed")
	}
	b := ctx()
	m.AttachProbe(&req, b)
	if !m.SingleAttempt(b) || !m.Begin(b, "openai", schemas.Key{ID: "k1"}) || m.Begin(b, "openai", schemas.Key{ID: "k2"}) {
		t.Fatal("probe escaped exact key")
	}
	var replay fasthttp.RequestCtx
	replay.Request.Header.Set("X-LTSP-Probe", token)
	if m.claimProbe(&replay) {
		t.Fatal("probe token replay accepted")
	}
}

func TestLANManagementRejectsCrossSiteMutation(t *testing.T) {
	for _, c := range []struct {
		origin, content, site string
		want                  bool
	}{
		{"http://172.16.100.254:8080", "application/json", "same-origin", true},
		{"https://unrelated.example", "application/json", "cross-site", false},
		{"", "application/x-www-form-urlencoded", "", false},
		{"", "application/json", "cross-site", false},
		{"", "application/json", "", true},
	} {
		var ctx fasthttp.RequestCtx
		ctx.Request.SetHost("172.16.100.254:8080")
		ctx.Request.Header.Set("Origin", c.origin)
		ctx.Request.Header.SetContentType(c.content)
		ctx.Request.Header.Set("Sec-Fetch-Site", c.site)
		if sameOrigin(&ctx) != c.want {
			t.Errorf("%+v", c)
		}
	}
}

func TestAccountAPIOnlyReturnsKeyReferences(t *testing.T) {
	m := manager(t)
	m.Keys = func(string) []schemas.Key {
		return []schemas.Key{{ID: "k1", Name: "Account key", Value: *schemas.NewSecretVar("never-expose-this-secret")}}
	}
	var ctx fasthttp.RequestCtx
	m.get(&ctx)
	b := string(ctx.Response.Body())
	if strings.Contains(b, "never-expose-this-secret") || !strings.Contains(b, `"balance":"unknown"`) {
		t.Fatal(b)
	}
}

func TestShellKeepsAccountHomeAndLinksSeparate(t *testing.T) {
	m := manager(t)
	m.AccountsPage = []byte("<html>account home</html>")
	var req fasthttp.RequestCtx
	req.Request.SetRequestURI("/")
	if got := m.Shell(&req, []byte("<body>native dashboard</body>")); string(got) != string(m.AccountsPage) {
		t.Fatal("root did not serve account home")
	}
	req.Request.SetRequestURI("/workspace/dashboard")
	got := string(m.Shell(&req, []byte("<body>native dashboard</body>")))
	if !strings.Contains(got, "native dashboard") || !strings.Contains(got, `href="/" target="_blank" rel="noopener noreferrer"`) {
		t.Fatal("workspace page or separate home link was lost")
	}
}

func BenchmarkHomeShell(b *testing.B) {
	m := &Manager{AccountsPage: []byte("account home")}
	var req fasthttp.RequestCtx
	req.Request.SetRequestURI("/")
	shell := []byte("<body>native dashboard</body>")
	b.ReportAllocs()
	for b.Loop() {
		m.Shell(&req, shell)
	}
}
