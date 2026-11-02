package ltsp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fasthttp/router"
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
	var v map[string]json.RawMessage
	if err := json.Unmarshal(ctx.Response.Body(), &v); err != nil {
		t.Fatal(err)
	}
	if _, global := v["balance"]; global {
		t.Fatal("balance still belongs to the gateway instead of a group")
	}
	if !strings.Contains(string(v["provider_schedules"]), `"cutoff":"20:00"`) {
		t.Fatal("required provider defaults missing")
	}
}

func TestAccountAPIPreventsCrossProviderAssignment(t *testing.T) {
	m := manager(t)
	m.Keys = func(p string) []schemas.Key { return []schemas.Key{{ID: p + "-key", Name: p}} }
	a := m.Snapshot()
	a[0].KeyIDs = []string{"anthropic-key"}
	b, err := json.Marshal(diskState{Accounts: a, ProviderSchedules: m.Schedules()})
	if err != nil {
		t.Fatal(err)
	}
	var req fasthttp.RequestCtx
	req.Request.Header.SetContentType("application/json")
	req.Request.SetBody(b)
	m.put(&req)
	if req.Response.StatusCode() != 400 {
		t.Fatal("cross-provider assignment accepted")
	}
	if m.Snapshot()[0].KeyIDs[0] != "k1" {
		t.Fatal("rejected assignment changed policy")
	}
}

func TestShellKeepsAccountHomeAndLinksSeparate(t *testing.T) {
	m := manager(t)
	m.AccountsPage = []byte("<html>account home</html>")
	var req fasthttp.RequestCtx
	req.Request.SetRequestURI("/")
	if got := m.Shell(&req, []byte("<body>native dashboard</body>")); len(got) != 0 || req.Response.StatusCode() != 302 || string(req.Response.Header.Peek("Location")) != "/ltsp/groups" {
		t.Fatal("root did not redirect to account groups")
	}
	req.Request.SetRequestURI("/workspace/dashboard")
	got := string(m.Shell(&req, []byte("<body>native dashboard</body>")))
	if !strings.Contains(got, "native dashboard") || !strings.Contains(got, `href="/" target="_blank" rel="noopener noreferrer"`) {
		t.Fatal("workspace page or separate home link was lost")
	}
}

func TestAccountTabRoutesServeTheSharedPage(t *testing.T) {
	m := manager(t)
	m.AccountsPage = []byte("<html>account tab shell</html>")
	r := router.New()
	m.Register(r)
	for _, path := range []string{"/ltsp/client", "/ltsp/groups", "/ltsp/accounts", "/ltsp/upstreams"} {
		t.Run(path, func(t *testing.T) {
			var req fasthttp.RequestCtx
			req.Request.SetRequestURI(path)
			req.Request.Header.SetMethod("GET")
			r.Handler(&req)
			if req.Response.StatusCode() != 200 || string(req.Response.Body()) != string(m.AccountsPage) {
				t.Fatalf("direct tab URL cannot refresh: %s %d %s", path, req.Response.StatusCode(), req.Response.Body())
			}
		})
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
