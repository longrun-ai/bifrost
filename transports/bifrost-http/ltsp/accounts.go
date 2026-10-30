// Package ltsp contains the LAN gateway's upstream-account credit policy.
package ltsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

type Account struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Provider    string    `json:"provider"`
	KeyIDs      []string  `json:"key_ids"`
	RefreshDate string    `json:"refresh_date"`
	Cycle       string    `json:"cycle"`
	ProbeModel  string    `json:"probe_model"`
	Exhausted   bool      `json:"exhausted"`
	Failures    int       `json:"failures"`
	NextRetry   time.Time `json:"next_retry"`
	LastChecked time.Time `json:"last_checked"`
	LastResult  string    `json:"last_result"`
	Busy        bool      `json:"busy,omitempty"`
}

type diskState struct {
	Accounts []Account `json:"accounts"`
}
type probeKey struct{}
type probeIdentity struct{ AccountID, KeyID string }
type recoveryKey struct{}

type Manager struct {
	mu          sync.Mutex
	path        string
	zone        *time.Location
	accounts    []Account
	busy        map[string]bool
	probeTokens map[string]probeIdentity
	clock       func() time.Time
	// Keys is read from Bifrost's own live configuration; credentials are never saved here.
	Keys         func(string) []schemas.Key
	Probe        func(context.Context, Account, string) *schemas.BifrostError
	AccountsPage []byte
}

func Open(path string, zone *time.Location) (*Manager, error) {
	m := &Manager{path: path, zone: zone, busy: map[string]bool{}, probeTokens: map[string]probeIdentity{}, clock: time.Now}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var s diskState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("invalid LTSP account state: %w", err)
	}
	if err := validate(s.Accounts); err != nil {
		return nil, err
	}
	m.accounts = s.Accounts
	return m, nil
}

func validate(accounts []Account) error {
	ids, keys := map[string]bool{}, map[string]bool{}
	for _, a := range accounts {
		if a.ID == "" || len(a.ID) > 100 || ids[a.ID] || strings.TrimSpace(a.Name) == "" || len(a.Name) > 200 {
			return errors.New("账号 ID 或名称无效或重复")
		}
		ids[a.ID] = true
		if a.Provider != "openai" && a.Provider != "anthropic" {
			return errors.New("仅支持 OpenAI / Anthropic 账号")
		}
		if a.Cycle != "monthly" && a.Cycle != "annual" {
			return errors.New("刷新周期须为 monthly 或 annual")
		}
		if _, err := time.Parse("2006-01-02", a.RefreshDate); err != nil {
			return errors.New("刷新日期须为 YYYY-MM-DD")
		}
		if len(a.ProbeModel) > 200 || strings.ContainsAny(a.ProbeModel, "\r\n") {
			return errors.New("探测模型无效")
		}
		for _, id := range a.KeyIDs {
			k := a.Provider + ":" + id
			if id == "" || keys[k] {
				return errors.New("每个 API key 只能属于一个上游账号")
			}
			keys[k] = true
		}
	}
	return nil
}

func (m *Manager) saveLocked() error {
	b, err := json.MarshalIndent(diskState{Accounts: m.accounts}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.path), ".accounts-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), m.path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(m.path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (m *Manager) Snapshot() []Account {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := make([]Account, len(m.accounts))
	for i, a := range m.accounts {
		r[i] = a
		r[i].KeyIDs = append([]string{}, a.KeyIDs...)
		r[i].Busy = m.busy[a.ID]
	}
	return r
}

// Replace edits policy only. UI clients cannot clear an exhaustion hold or its backoff.
func (m *Manager) Replace(accounts []Account) error {
	if err := validate(accounts); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.accounts
	byID := map[string]Account{}
	for _, a := range old {
		byID[a.ID] = a
	}
	for i, a := range accounts {
		if p, ok := byID[a.ID]; ok {
			if m.busy[a.ID] {
				return errors.New("探测正在进行，请完成后再编辑")
			}
			a.Exhausted, a.Failures, a.NextRetry = p.Exhausted, p.Failures, p.NextRetry
			a.LastChecked, a.LastResult = p.LastChecked, p.LastResult
		} else {
			a.Exhausted = false
			a.Failures = 0
			a.NextRetry = time.Time{}
			a.LastChecked = time.Time{}
			a.LastResult = ""
		}
		a.Busy = false
		accounts[i] = a
	}
	// Do not delete a currently probed account.
	for id, busy := range m.busy {
		if busy {
			found := false
			for _, a := range accounts {
				if a.ID == id {
					found = true
				}
			}
			if !found {
				return errors.New("探测正在进行，不能删除账号")
			}
		}
	}
	m.accounts = accounts
	if err := m.saveLocked(); err != nil {
		m.accounts = old
		return err
	}
	return nil
}

// Window checks calendar dates in the gateway timezone. Monthly 29/30/31 clamps
// within each month without drifting the original anchor; leap years behave likewise.
func Window(date, cycle string, now time.Time, zone *time.Location) bool {
	anchor, err := time.ParseInLocation("2006-01-02", date, zone)
	if err != nil {
		return false
	}
	t := now.In(zone)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, zone)
	for delta := -1; delta <= 1; delta++ {
		var y int
		var mo time.Month
		if cycle == "annual" {
			y = t.Year() + delta
			mo = anchor.Month()
		} else {
			n := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, zone).AddDate(0, delta, 0)
			y, mo = n.Year(), n.Month()
		}
		last := time.Date(y, mo+1, 0, 0, 0, 0, 0, zone).Day()
		d := min(anchor.Day(), last)
		refresh := time.Date(y, mo, d, 0, 0, 0, 0, zone)
		if !refresh.Before(anchor) && !day.Before(refresh.AddDate(0, 0, -1)) && !day.After(refresh.AddDate(0, 0, 1)) {
			return true
		}
	}
	return false
}

func (m *Manager) findLocked(provider string, keyID string) *Account {
	for i := range m.accounts {
		a := &m.accounts[i]
		if a.Provider == provider {
			for _, id := range a.KeyIDs {
				if id == keyID {
					return a
				}
			}
		}
	}
	return nil
}

func (m *Manager) allowedLocked(a *Account, now time.Time) bool {
	return a == nil || !a.Exhausted || (!m.busy[a.ID] && !now.Before(a.NextRetry) && Window(a.RefreshDate, a.Cycle, now, m.zone))
}

// Begin is called at the actual provider-attempt boundary, including pinned keys.
func (m *Manager) Begin(ctx *schemas.BifrostContext, provider schemas.ModelProvider, key schemas.Key) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.findLocked(string(provider), key.ID)
	if p, ok := ctx.Value(probeKey{}).(probeIdentity); ok {
		return a != nil && p.AccountID == a.ID && p.KeyID == key.ID
	}
	if a == nil {
		return true
	}
	if !m.allowedLocked(a, m.clock()) {
		return false
	}
	if a.Exhausted {
		m.busy[a.ID] = true
		ctx.SetValue(recoveryKey{}, a.ID)
	}
	return true
}

func (m *Manager) SingleAttempt(ctx *schemas.BifrostContext) bool {
	_, ok := ctx.Value(probeKey{}).(probeIdentity)
	return ok
}

// CreditError deliberately excludes rate limits, subscription-app limits, spending
// caps, authentication and generic quota errors. Only an upstream balance verdict pauses.
func CreditError(provider string, e *schemas.BifrostError) bool {
	if e == nil || e.IsBifrostError || e.Error == nil {
		return false
	}
	if provider == "openai" {
		for _, s := range []*string{e.Type, e.Error.Type, e.Error.Code} {
			if s != nil && *s == "credit_balance_exhausted" {
				return true
			}
		}
	}
	return provider == "anthropic" && strings.Contains(strings.ToLower(e.Error.Message), "credit balance is too low")
}

func classify(provider string, e *schemas.BifrostError) string {
	if e == nil {
		return "调用成功，额度可用"
	}
	if CreditError(provider, e) {
		return "上游明确报告余额不足"
	}
	if e.StatusCode != nil {
		switch *e.StatusCode {
		case 401, 403:
			return "认证或权限错误，额度恢复未确认"
		case 429:
			return "上游限流或配额限制，额度恢复未确认"
		case 400, 404:
			return "模型或请求参数错误，额度恢复未确认"
		case 408, 504:
			return "网络超时，额度恢复未确认"
		}
	}
	if e.Type != nil {
		switch *e.Type {
		case schemas.RequestTimedOut:
			return "网络超时，额度恢复未确认"
		case schemas.ProviderConnectionFailed:
			return "代理或上游连接失败，额度恢复未确认"
		}
	}
	return "调用失败，额度恢复未确认；请查看原生请求统计中的错误类型"
}

func (m *Manager) End(ctx *schemas.BifrostContext, provider schemas.ModelProvider, key schemas.Key, e *schemas.BifrostError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.findLocked(string(provider), key.ID)
	if a == nil {
		return
	}
	// A manual probe owns its reservation until the API returns; finish it in ProbeNow.
	if p, ok := ctx.Value(probeKey{}).(probeIdentity); ok && p.AccountID == a.ID {
		return
	}
	recoveryID, _ := ctx.Value(recoveryKey{}).(string)
	wasRecovery := recoveryID == a.ID
	if wasRecovery {
		delete(m.busy, a.ID)
		ctx.ClearValue(recoveryKey{})
	}
	if !CreditError(string(provider), e) && !wasRecovery {
		return
	}
	if err := m.finishLocked(a, e); err != nil {
		log.Printf("LTSP account state save failed: %v", err)
	}
}

func (m *Manager) finishLocked(a *Account, e *schemas.BifrostError) error {
	a.LastChecked = m.clock()
	a.LastResult = classify(a.Provider, e)
	if e == nil {
		a.Exhausted = false
		a.Failures = 0
		a.NextRetry = time.Time{}
	} else {
		if CreditError(a.Provider, e) {
			a.Exhausted = true
		}
		a.Failures++
		intervals := []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}
		a.NextRetry = m.clock().Add(intervals[min(a.Failures-1, len(intervals)-1)])
	}
	if err := m.saveLocked(); err != nil {
		// Keep the conservative in-memory verdict and surface the persistence failure.
		a.LastResult = "账号状态保存失败：" + err.Error()
		return err
	}
	return nil
}

func (m *Manager) ProbeNow(ctx context.Context, id string) (Account, error) {
	m.mu.Lock()
	var a Account
	found := false
	for _, v := range m.accounts {
		if v.ID == id {
			a = v
			found = true
			break
		}
	}
	if !found {
		m.mu.Unlock()
		return a, errors.New("账号不存在")
	}
	if m.busy[id] {
		m.mu.Unlock()
		return a, errors.New("该账号已有恢复探测正在进行")
	}
	if a.ProbeModel == "" {
		m.mu.Unlock()
		return a, errors.New("请先设置该账号支持的探测模型")
	}
	keyID := ""
	if m.Keys != nil {
		for _, k := range m.Keys(a.Provider) {
			for _, id := range a.KeyIDs {
				if k.ID == id && (k.Enabled == nil || *k.Enabled) && k.Models.IsAllowed(a.ProbeModel) && !k.BlacklistedModels.IsBlocked(a.ProbeModel) {
					keyID = k.ID
					break
				}
			}
			if keyID != "" {
				break
			}
		}
	}
	if keyID == "" {
		m.mu.Unlock()
		return a, errors.New("该账号没有已启用且支持探测模型的 key")
	}
	if m.Probe == nil {
		m.mu.Unlock()
		return a, errors.New("探测接口未就绪")
	}
	m.busy[id] = true
	m.mu.Unlock()
	// Bounded ordinary inference; no timer or downstream traffic is required.
	probeCtx, cancel := context.WithTimeout(context.WithValue(ctx, probeKey{}, probeIdentity{id, keyID}), 45*time.Second)
	defer cancel()
	e := m.Probe(probeCtx, a, keyID)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.busy, id)
	for i := range m.accounts {
		if m.accounts[i].ID == id {
			err := m.finishLocked(&m.accounts[i], e)
			return m.accounts[i], err
		}
	}
	return a, errors.New("账号已删除")
}
