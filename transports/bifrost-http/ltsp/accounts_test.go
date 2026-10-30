package ltsp

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func manager(t *testing.T) *Manager {
	t.Helper()
	m, e := Open(filepath.Join(t.TempDir(), "accounts.json"), time.UTC)
	if e != nil {
		t.Fatal(e)
	}
	m.clock = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	if e = m.Replace([]Account{{ID: "one", Name: "Account", Provider: "openai", KeyIDs: []string{"k1", "k2"}, RefreshDate: "2026-10-29", Cycle: "monthly", ProbeModel: "test-model"}}); e != nil {
		t.Fatal(e)
	}
	m.Keys = func(string) []schemas.Key { return []schemas.Key{{ID: "k1", Models: schemas.WhiteList{"*"}}} }
	return m
}
func quota() *schemas.BifrostError {
	return &schemas.BifrostError{Type: schemas.Ptr("credit_balance_exhausted"), Error: &schemas.ErrorField{Code: schemas.Ptr("credit_balance_exhausted"), Message: "No credits"}}
}
func ctx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func BenchmarkAccountAttempt(b *testing.B) {
	m := &Manager{zone: time.UTC, clock: time.Now, accounts: []Account{{ID: "a", Provider: "openai", KeyIDs: []string{"k"}}}}
	c := ctx()
	key := schemas.Key{ID: "k"}
	b.ReportAllocs()
	for b.Loop() {
		m.Begin(c, schemas.OpenAI, key)
		m.End(c, schemas.OpenAI, key, nil)
	}
}

func TestWindowCalendar(t *testing.T) {
	z, _ := time.LoadLocation("America/Los_Angeles")
	for _, c := range []struct {
		anchor, cycle, day string
		want               bool
	}{
		{"2026-10-29", "monthly", "2026-10-27", false}, {"2026-10-29", "monthly", "2026-10-28", true}, {"2026-10-29", "monthly", "2026-10-30", true}, {"2026-10-29", "monthly", "2026-10-31", false},
		{"2026-01-31", "monthly", "2026-03-01", true}, {"2026-01-31", "monthly", "2026-03-29", false}, {"2024-02-29", "annual", "2027-02-28", true}, {"2024-02-29", "annual", "2028-03-01", true},
		{"2026-11-01", "monthly", "2026-10-31", true}, {"2026-11-01", "monthly", "2026-11-03", false}, {"2026-12-01", "monthly", "2026-10-31", false},
	} {
		d, _ := time.ParseInLocation("2006-01-02", c.day, z)
		if got := Window(c.anchor, c.cycle, d, z); got != c.want {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}

func TestOnlyExplicitCreditErrorsPause(t *testing.T) {
	for _, c := range []struct {
		provider string
		e        *schemas.BifrostError
		want     bool
	}{
		{"openai", quota(), true},
		{"openai", &schemas.BifrostError{StatusCode: schemas.Ptr(429), Error: &schemas.ErrorField{Type: schemas.Ptr("rate_limit_exceeded")}}, false},
		{"openai", &schemas.BifrostError{Error: &schemas.ErrorField{Code: schemas.Ptr("insufficient_quota"), Message: "monthly spending limit"}}, false},
		{"anthropic", &schemas.BifrostError{Error: &schemas.ErrorField{Message: "Your credit balance is too low to access the Anthropic API."}}, true},
		{"anthropic", &schemas.BifrostError{StatusCode: schemas.Ptr(401), Error: &schemas.ErrorField{Message: "invalid key"}}, false},
	} {
		if CreditError(c.provider, c.e) != c.want {
			t.Errorf("%+v", c)
		}
	}
}

func TestGroupBackoffPersistenceAndEdits(t *testing.T) {
	m := manager(t)
	c := ctx()
	k := schemas.Key{ID: "k1"}
	m.End(c, "openai", k, quota())
	a := m.Snapshot()[0]
	if !a.Exhausted || a.Failures != 1 || a.NextRetry.Sub(m.clock()) != 5*time.Minute {
		t.Fatal(a)
	}
	if m.Begin(ctx(), "openai", schemas.Key{ID: "k2"}) {
		t.Fatal("same-account sibling escaped hold")
	}
	a.Exhausted = false
	a.Failures = 0
	a.NextRetry = time.Time{}
	a.RefreshDate = "2026-10-30"
	if e := m.Replace([]Account{a}); e != nil {
		t.Fatal(e)
	}
	r, e := Open(m.path, time.UTC)
	if e != nil {
		t.Fatal(e)
	}
	a = r.Snapshot()[0]
	if !a.Exhausted || a.Failures != 1 || a.RefreshDate != "2026-10-30" {
		t.Fatal("edit cleared credit verdict", a)
	}
	for i, want := range []time.Duration{15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 6 * time.Hour} {
		m.End(ctx(), "openai", k, quota())
		if got := m.Snapshot()[0].NextRetry.Sub(m.clock()); got != want {
			t.Fatalf("step %d got %s", i, got)
		}
	}
}

func TestOneRecoveryPerAccountAndUnrelatedResults(t *testing.T) {
	m := manager(t)
	m.End(ctx(), "openai", schemas.Key{ID: "k1"}, quota())
	now := m.clock().Add(5 * time.Minute)
	m.clock = func() time.Time { return now }
	c1, c2 := ctx(), ctx()
	if !m.Begin(c1, "openai", schemas.Key{ID: "k1"}) || m.Begin(c2, "openai", schemas.Key{ID: "k2"}) {
		t.Fatal("reservation failed")
	}
	// An older healthy call finishing while recovery is busy cannot release it.
	m.End(c2, "openai", schemas.Key{ID: "k2"}, nil)
	if m.Begin(c2, "openai", schemas.Key{ID: "k2"}) {
		t.Fatal("unrelated request released reservation")
	}
	m.End(c1, "openai", schemas.Key{ID: "k1"}, &schemas.BifrostError{StatusCode: schemas.Ptr(429), Error: &schemas.ErrorField{Message: "rate limited"}})
	if a := m.Snapshot()[0]; !a.Exhausted || a.Busy {
		t.Fatal(a)
	}
	now = now.Add(15 * time.Minute)
	if !m.Begin(c1, "openai", schemas.Key{ID: "k1"}) {
		t.Fatal("backoff never released")
	}
	m.End(c1, "openai", schemas.Key{ID: "k1"}, nil)
	if a := m.Snapshot()[0]; a.Exhausted || a.Failures != 0 || a.Busy {
		t.Fatal(a)
	}
}

func TestImmediateProbeOutsideWindowAndConcurrentButton(t *testing.T) {
	m := manager(t)
	a := m.Snapshot()[0]
	a.RefreshDate = "2026-10-10"
	if e := m.Replace([]Account{a}); e != nil {
		t.Fatal(e)
	}
	m.End(ctx(), "openai", schemas.Key{ID: "k1"}, quota())
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	m.Probe = func(c context.Context, a Account, id string) *schemas.BifrostError {
		calls.Add(1)
		if id != "k1" {
			t.Error(id)
		}
		b := schemas.NewBifrostContext(c, schemas.NoDeadline)
		if !m.Begin(b, "openai", schemas.Key{ID: id}) {
			t.Error("manual probe was blocked")
		}
		close(entered)
		<-release
		return nil
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, e := m.ProbeNow(context.Background(), "one"); e != nil {
			t.Error(e)
		}
	}()
	<-entered
	if _, e := m.ProbeNow(context.Background(), "one"); e == nil {
		t.Fatal("duplicate probe was accepted")
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 || m.Snapshot()[0].Exhausted {
		t.Fatal(m.Snapshot())
	}
}

func TestDisabledKeyIsNotReenabled(t *testing.T) {
	m := manager(t)
	m.Keys = func(string) []schemas.Key {
		return []schemas.Key{{ID: "k1", Enabled: schemas.Ptr(false), Models: schemas.WhiteList{"*"}}}
	}
	m.Probe = func(context.Context, Account, string) *schemas.BifrostError { t.Fatal("disabled key used"); return nil }
	if _, e := m.ProbeNow(context.Background(), "one"); e == nil {
		t.Fatal("disabled key accepted")
	}
}
