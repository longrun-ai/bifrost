package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/ltsp"
)

func (s *BifrostHTTPServer) initLTSP() error {
	path := os.Getenv("LTSP_GATEWAY_STATE")
	if path == "" {
		return nil
	}
	zone := time.Local
	if name := os.Getenv("LTSP_GATEWAY_TIMEZONE"); name != "" {
		var err error
		zone, err = time.LoadLocation(name)
		if err != nil {
			return err
		}
	}
	m, err := ltsp.Open(path, zone)
	if err != nil {
		return err
	}
	m.AccountsPage, err = s.UIContent.ReadFile("ui/ltsp/accounts.html")
	if err != nil {
		return err
	}
	m.Keys = func(provider string) []schemas.Key {
		c, e := s.Config.GetProviderConfigRaw(schemas.ModelProvider(provider))
		if e != nil {
			return nil
		}
		return c.Keys
	}
	probeClient := &http.Client{Timeout: 44 * time.Second, Transport: &http.Transport{Proxy: nil}}
	m.Probe = func(ctx context.Context, a ltsp.Account, id string) *schemas.BifrostError {
		fail := func(message string) *schemas.BifrostError {
			return &schemas.BifrostError{IsBifrostError: true, Type: schemas.Ptr(schemas.ProviderConnectionFailed), Error: &schemas.ErrorField{Message: message}}
		}
		origin := ltsp.ProbeURL(ctx)
		token, cleanup := m.ProbeToken(ctx)
		defer cleanup()
		if origin == "" || token == "" {
			return fail("probe context is unavailable")
		}
		path := "/v1/chat/completions"
		body := map[string]any{"model": a.Provider + "/" + a.ProbeModel, "messages": []any{map[string]any{"role": "user", "content": "Reply OK."}}, "max_completion_tokens": 16}
		if a.Provider == "openai" {
			path = "/v1/responses"
			body = map[string]any{"model": "openai/" + a.ProbeModel, "input": "Reply OK.", "max_output_tokens": 128}
		}
		raw, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, bytes.NewReader(raw))
		if err != nil {
			return fail("cannot create probe request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-BF-API-Key-ID", id)
		req.Header.Set("X-BF-Session-Affinity", "off")
		req.Header.Set("X-Stainless-Timeout", "40")
		req.Header.Set("X-LTSP-Probe", token)
		resp, err := probeClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return &schemas.BifrostError{IsBifrostError: true, Type: schemas.Ptr(schemas.RequestTimedOut), Error: &schemas.ErrorField{Message: "probe timed out"}}
			}
			return fail("cannot reach local gateway for probe")
		}
		defer resp.Body.Close()
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return fail("cannot read probe result")
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		var e schemas.BifrostError
		if json.Unmarshal(raw, &e) != nil || e.Error == nil {
			return &schemas.BifrostError{StatusCode: schemas.Ptr(resp.StatusCode), Error: &schemas.ErrorField{Message: http.StatusText(resp.StatusCode)}}
		}
		e.StatusCode = schemas.Ptr(resp.StatusCode)
		return &e
	}

	lib.LTSP = m
	s.ShellRewriter = m.Shell
	return nil
}
