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
	m, e := Open(filepath.Join(t.TempDir(), "accounts.json"))
	if e != nil {
		t.Fatal(e)
	}
	m.clock = func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	if e = m.Replace([]Account{{ID: "one", Name: "Account", Provider: "openai", KeyIDs: []string{"k1", "k2"}, RefreshDate: "2026-10-29", Cycle: "monthly"}}, m.Schedules()); e != nil {
		t.Fatal(e)
	}
	m.Keys = func(string) []schemas.Key { return []schemas.Key{{ID: "k1", Models: schemas.WhiteList{"*"}}} }
	m.Models = func(string) []string { return []string{"test-model"} }
	return m
}
func quota() *schemas.BifrostError {
	return &schemas.BifrostError{Type: schemas.Ptr("credit_balance_exhausted"), Error: &schemas.ErrorField{Code: schemas.Ptr("credit_balance_exhausted"), Message: "No credits"}}
}
func ctx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func BenchmarkAccountAttempt(b *testing.B) {
	m := &Manager{clock: time.Now, accounts: []Account{{ID: "a", Provider: "openai", KeyIDs: []string{"k"}}}}
	c := ctx()
	key := schemas.Key{ID: "k"}
	b.ReportAllocs()
	for b.Loop() {
		m.Begin(c, schemas.OpenAI, key)
		m.End(c, schemas.OpenAI, key, nil)
	}
}

func TestWindowCalendar(t *testing.T) {
	for _, c := range []struct {
		anchor, cycle, zone, instant string
		want                         bool
	}{
		{"2026-10-29", "monthly", "UTC", "2026-10-29T19:59:59Z", false},
		{"2026-10-29", "monthly", "UTC", "2026-10-29T20:00:00Z", true},
		{"2026-10-29", "monthly", "UTC", "2026-10-31T19:59:59Z", true},
		{"2026-10-29", "monthly", "UTC", "2026-10-31T20:00:00Z", false},
		{"2026-10-29", "monthly", "America/Los_Angeles", "2026-10-30T02:59:59Z", false},
		{"2026-10-29", "monthly", "America/Los_Angeles", "2026-10-30T03:00:00Z", true},
		{"2026-03-07", "monthly", "America/Los_Angeles", "2026-03-10T03:59:59Z", true},
		{"2026-03-07", "monthly", "America/Los_Angeles", "2026-03-10T04:00:00Z", false},
		{"2026-10-31", "monthly", "America/Los_Angeles", "2026-11-03T02:59:59Z", true},
		{"2026-10-31", "monthly", "America/Los_Angeles", "2026-11-03T03:00:00Z", false},
		{"2026-01-31", "monthly", "UTC", "2026-02-28T20:00:00Z", true},
		{"2026-01-31", "monthly", "UTC", "2026-03-30T20:00:00Z", false},
		{"2026-01-31", "monthly", "UTC", "2026-03-31T20:00:00Z", true},
		{"2024-02-29", "annual", "UTC", "2027-02-28T20:00:00Z", true},
		{"2024-02-29", "annual", "UTC", "2028-02-28T20:00:00Z", false},
		{"2025-12-31", "monthly", "UTC", "2026-01-01T12:00:00Z", true},
		{"2026-12-01", "monthly", "UTC", "2026-10-31T20:00:00Z", false},
	} {
		s := defaultSchedules()
		s[0].Timezone = c.zone
		s, e := validateSchedules(s)
		if e != nil {
			t.Fatal(e)
		}
		now, _ := time.Parse(time.RFC3339, c.instant)
		start, end := RetryWindow(c.anchor, c.cycle, now, s[0])
		got := !now.Before(start) && now.Before(end)
		if got != c.want || end.Sub(start) != 48*time.Hour {
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
	if e := m.Replace([]Account{a}, m.Schedules()); e != nil {
		t.Fatal(e)
	}
	r, e := Open(m.path)
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
	if e := m.Replace([]Account{a}, m.Schedules()); e != nil {
		t.Fatal(e)
	}
	m.End(ctx(), "openai", schemas.Key{ID: "k1"}, quota())
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	m.Probe = func(c context.Context, a Account, id, model string) *schemas.BifrostError {
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
	m.Probe = func(context.Context, Account, string, string) *schemas.BifrostError {
		t.Fatal("disabled key used")
		return nil
	}
	if _, e := m.ProbeNow(context.Background(), "one"); e == nil {
		t.Fatal("disabled key accepted")
	}
}

func TestSchedulesAreRequiredValidatedAndPersisted(t *testing.T) {
	m := manager(t)
	for _, s := range m.Schedules() {
		if s.Timezone != "America/Los_Angeles" || s.Cutoff != "20:00" {
			t.Fatal("new provider defaults must use Los Angeles at 20:00", s)
		}
	}
	for _, mutate := range []func([]ProviderSchedule) []ProviderSchedule{
		func(s []ProviderSchedule) []ProviderSchedule { return nil },
		func(s []ProviderSchedule) []ProviderSchedule { return s[:1] },
		func(s []ProviderSchedule) []ProviderSchedule { s[0].Timezone = "Local"; return s },
		func(s []ProviderSchedule) []ProviderSchedule { s[0].Timezone = "bad/zone"; return s },
		func(s []ProviderSchedule) []ProviderSchedule { s[0].Cutoff = "24:00"; return s },
		func(s []ProviderSchedule) []ProviderSchedule { s[0].Cutoff = ""; return s },
		func(s []ProviderSchedule) []ProviderSchedule { s[0].Provider = "anthropic"; return s },
	} {
		if err := m.Replace(m.Snapshot(), mutate(m.Schedules())); err == nil {
			t.Fatal("invalid schedule accepted")
		}
	}
	s := m.Schedules()
	s[0].Timezone, s[0].Cutoff, s[0].ProbeModel = "Asia/Shanghai", "21:30", "chosen-model"
	if err := m.Replace(m.Snapshot(), s); err != nil {
		t.Fatal(err)
	}
	r, err := Open(m.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Schedules()[0]; got.Timezone != "Asia/Shanghai" || got.Cutoff != "21:30" || got.ProbeModel != "chosen-model" {
		t.Fatal(got)
	}
	r.clock = func() time.Time { return time.Date(2026, 10, 29, 13, 29, 59, 0, time.UTC) }
	r.End(ctx(), "openai", schemas.Key{ID: "k1"}, quota())
	r.accounts[0].NextRetry = time.Time{}
	if r.Begin(ctx(), "openai", schemas.Key{ID: "k1"}) {
		t.Fatal("retry began before local 21:30 cutoff")
	}
	r.clock = func() time.Time { return time.Date(2026, 10, 29, 13, 30, 0, 0, time.UTC) }
	if !r.Begin(ctx(), "openai", schemas.Key{ID: "k1"}) {
		t.Fatal("retry did not begin at configured cutoff")
	}
}

func TestProbeDiscoversRemembersAndReplacesUnsupportedModel(t *testing.T) {
	m := manager(t)
	m.Models = func(string) []string { return []string{"good", "other", "good"} }
	seen := map[string]int{}
	m.Probe = func(_ context.Context, _ Account, keyID, model string) *schemas.BifrostError {
		seen[model]++
		if keyID != "k1" {
			t.Fatal("wrong account key")
		}
		if model == "other" || model == "retired" {
			return &schemas.BifrostError{StatusCode: schemas.Ptr(404), Error: &schemas.ErrorField{Code: schemas.Ptr("model_not_found"), Message: "model not found"}}
		}
		return nil
	}
	if _, err := m.ProbeNow(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if m.Schedules()[0].ProbeModel != "good" || seen["good"] != 1 || seen["other"] > 1 {
		t.Fatal("catalog discovery did not remember a usable unique candidate", seen)
	}
	r, err := Open(m.path)
	if err != nil || r.Schedules()[0].ProbeModel != "good" {
		t.Fatal("discovered model was not persisted", err)
	}
	s := m.Schedules()
	s[0].ProbeModel = "retired"
	if err := m.Replace(m.Snapshot(), s); err != nil {
		t.Fatal(err)
	}
	clear(seen)
	if _, err := m.ProbeNow(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if seen["retired"] != 1 || seen["good"] != 1 || seen["other"] > 1 || m.Schedules()[0].ProbeModel != "good" {
		t.Fatal("unsupported preferred model was not replaced", seen)
	}
}

func TestProbeDoesNotSearchOnBillingAuthNetworkOrRateErrors(t *testing.T) {
	for _, e := range []*schemas.BifrostError{
		quota(),
		{StatusCode: schemas.Ptr(401), Error: &schemas.ErrorField{Message: "model authorization failed"}},
		{StatusCode: schemas.Ptr(403), Error: &schemas.ErrorField{Message: "account forbidden"}},
		{StatusCode: schemas.Ptr(429), Error: &schemas.ErrorField{Message: "rate limited"}},
		{IsBifrostError: true, Type: schemas.Ptr(schemas.ProviderConnectionFailed), Error: &schemas.ErrorField{Message: "network failed"}},
		{StatusCode: schemas.Ptr(400), Error: &schemas.ErrorField{Message: "max_output_tokens not supported for this model"}},
	} {
		m := manager(t)
		s := m.Schedules()
		s[0].ProbeModel = "preferred"
		if err := m.Replace(m.Snapshot(), s); err != nil {
			t.Fatal(err)
		}
		m.Models = func(string) []string { return []string{"fallback-a", "fallback-b"} }
		calls := 0
		m.Probe = func(context.Context, Account, string, string) *schemas.BifrostError { calls++; return e }
		if _, err := m.ProbeNow(context.Background(), "one"); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || m.Schedules()[0].ProbeModel != "preferred" {
			t.Fatal("non-model failure triggered a model sweep", calls)
		}
	}
}

func BenchmarkRetryWindow(b *testing.B) {
	s, _ := validateSchedules(defaultSchedules())
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	for b.Loop() {
		RetryWindow("2026-10-29", "monthly", now, s[0])
	}
}

func TestFixedGMTTimezoneOffsetsAndBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset int
	}{
		{"GMT+05:30", 19800}, {"GMT-03:30", -12600}, {"GMT-00:30", -1800}, {"GMT+14:00", 50400}, {"GMT-12:00", -43200}, {"GMT", 0},
	} {
		s := defaultSchedules()
		s[0].Timezone = tc.name
		s, err := validateSchedules(s)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
		start, end := RetryWindow("2026-10-29", "monthly", now, s[0])
		_, offset := start.Zone()
		if offset != tc.offset || start.Hour() != 20 || end.Sub(start) != 48*time.Hour {
			t.Fatal(tc, start, end)
		}
	}
	for _, name := range []string{"GMT+14:30", "GMT-12:30", "GMT+15:00", "GMT+5:30", "GMT+05:45", "GMT+aa:00"} {
		s := defaultSchedules()
		s[0].Timezone = name
		if _, err := validateSchedules(s); err == nil {
			t.Fatal("invalid GMT accepted", name)
		}
	}
}

func TestIndependentEditsAndImmediateAssignmentPreserveOtherState(t *testing.T) {
	m := manager(t)
	m.End(ctx(), "openai", schemas.Key{ID: "k1"}, quota())
	s := m.Schedules()
	s[0].Timezone = "GMT+05:30"
	if err := m.Edit(nil, &s); err != nil {
		t.Fatal(err)
	}
	groups := m.Snapshot()
	groups[0].Name = "renamed"
	groups[0].KeyIDs = nil
	if err := m.Edit(&groups, nil); err != nil {
		t.Fatal(err)
	}
	if m.Schedules()[0].Timezone != "GMT+05:30" || len(m.Snapshot()[0].KeyIDs) != 2 || !m.Snapshot()[0].Exhausted {
		t.Fatal("section save overwrote unrelated fields")
	}
	if err := m.Assign("openai", "k1", ""); err != nil {
		t.Fatal(err)
	}
	if len(m.Snapshot()[0].KeyIDs) != 1 || !m.Snapshot()[0].Exhausted {
		t.Fatal("unassign cleared hold")
	}
	if err := m.Assign("anthropic", "missing", "one"); err == nil {
		t.Fatal("cross-provider association allowed")
	}
	if err := m.Assign("openai", "k1", "unsaved"); err == nil {
		t.Fatal("unsaved group accepted")
	}
	if err := m.Assign("openai", "k1", "one"); err != nil {
		t.Fatal(err)
	}
	r, err := Open(m.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Snapshot()[0].KeyIDs) != 2 || r.Snapshot()[0].Name != "renamed" || !r.Snapshot()[0].Exhausted || r.Schedules()[0].Timezone != "GMT+05:30" {
		t.Fatal("assignment/edit state did not persist")
	}
}

func TestProbeTriesEachUnsupportedCatalogModelOnce(t *testing.T) {
	m := manager(t)
	s := m.Schedules()
	s[0].ProbeModel = "retired"
	if err := m.Replace(m.Snapshot(), s); err != nil {
		t.Fatal(err)
	}
	m.End(ctx(), "openai", schemas.Key{ID: "k1"}, quota())
	m.Models = func(string) []string { return []string{"retired", "missing-a", "missing-a", "missing-b"} }
	seen := map[string]int{}
	m.Probe = func(_ context.Context, _ Account, _ string, model string) *schemas.BifrostError {
		seen[model]++
		return &schemas.BifrostError{StatusCode: schemas.Ptr(404), Error: &schemas.ErrorField{Type: schemas.Ptr("not_found_error"), Message: "model: " + model}}
	}
	if _, err := m.ProbeNow(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || !m.Snapshot()[0].Exhausted || m.Snapshot()[0].Busy || m.Schedules()[0].ProbeModel != "retired" {
		t.Fatal(seen)
	}
	for _, count := range seen {
		if count != 1 {
			t.Fatal("catalog model repeated", seen)
		}
	}
}

func TestModelParameterVerdictDoesNotHideOtherParameterErrors(t *testing.T) {
	e := &schemas.BifrostError{StatusCode: schemas.Ptr(400), Error: &schemas.ErrorField{Code: schemas.Ptr("unsupported_value"), Param: "model", Message: "This model is not supported for Responses"}}
	if !UnsupportedModel(e) {
		t.Fatal("explicit unsupported model was not recognized")
	}
	e.Error.Param = "max_output_tokens"
	if UnsupportedModel(e) {
		t.Fatal("unrelated request parameter triggered a model sweep")
	}
	e.Error.Param = "model"
	e.Error.Type = schemas.Ptr("permission_error")
	if UnsupportedModel(e) {
		t.Fatal("authentication/permission failure triggered a model sweep")
	}
}
