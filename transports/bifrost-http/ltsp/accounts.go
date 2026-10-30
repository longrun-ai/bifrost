// Package ltsp contains the LAN gateway's upstream-account credit policy.
package ltsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
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
	Exhausted   bool      `json:"exhausted"`
	Failures    int       `json:"failures"`
	NextRetry   time.Time `json:"next_retry"`
	LastChecked time.Time `json:"last_checked"`
	LastResult  string    `json:"last_result"`
	Busy        bool      `json:"busy,omitempty"`
}

type diskState struct {
	Accounts          []Account          `json:"accounts"`
	ProviderSchedules []ProviderSchedule `json:"provider_schedules"`
}

// ProviderSchedule is shared by all groups using the same upstream. Parsed
// timezone data is retained per configured provider, never per request.
type ProviderSchedule struct {
	Provider     string `json:"provider"`
	Timezone     string `json:"timezone"`
	Cutoff       string `json:"cutoff"`
	ProbeModel   string `json:"probe_model"`
	zone         *time.Location
	cutoffMinute int
}

func defaultSchedules() []ProviderSchedule {
	return []ProviderSchedule{
		{Provider: "openai", Timezone: "America/Los_Angeles", Cutoff: "20:00"},
		{Provider: "anthropic", Timezone: "America/Los_Angeles", Cutoff: "20:00"},
	}
}

// Fixed GMT offsets are a user choice; regional names retain IANA/DST rules.
func refreshTimezone(name string) (*time.Location, error) {
	if !strings.HasPrefix(name, "GMT+") && !strings.HasPrefix(name, "GMT-") {
		return time.LoadLocation(name)
	}
	if len(name) != 9 || (name[3] != '+' && name[3] != '-') || name[6] != ':' || name[4] < '0' || name[4] > '9' || name[5] < '0' || name[5] > '9' || (name[7:] != "00" && name[7:] != "30") {
		return nil, errors.New("GMT 偏移须为 GMT±HH:00 或 GMT±HH:30")
	}
	hour, _ := strconv.Atoi(name[4:6])
	minute, _ := strconv.Atoi(name[7:])
	limit := 14
	if name[3] == '-' {
		limit = 12
	}
	if hour > limit || (hour == limit && minute != 0) {
		return nil, errors.New("GMT 偏移须在 -12:00 至 +14:00 之间")
	}
	offset := (hour*60 + minute) * 60
	if name[3] == '-' {
		offset = -offset
	}
	return time.FixedZone(name, offset), nil
}

func validateSchedules(schedules []ProviderSchedule) ([]ProviderSchedule, error) {
	if len(schedules) != 2 {
		return nil, errors.New("须配置 OpenAI 和 Anthropic 的时区与刷新截止时间")
	}
	result := append([]ProviderSchedule(nil), schedules...)
	seen := map[string]bool{}
	for i := range result {
		s := &result[i]
		if (s.Provider != "openai" && s.Provider != "anthropic") || seen[s.Provider] {
			return nil, errors.New("上游配置缺失或重复")
		}
		seen[s.Provider] = true
		s.ProbeModel = strings.TrimPrefix(strings.TrimSpace(s.ProbeModel), s.Provider+"/")
		if len(s.ProbeModel) > 200 || strings.ContainsAny(s.ProbeModel, "\r\n") {
			return nil, errors.New("探测模型无效")
		}
		if s.Timezone == "" || s.Timezone == "Local" {
			return nil, errors.New("请选择 GMT 偏移或地区时区")
		}
		var err error
		s.zone, err = refreshTimezone(s.Timezone)
		if err != nil {
			return nil, errors.New("无效时区：" + s.Timezone)
		}
		t, err := time.Parse("15:04", s.Cutoff)
		if err != nil || t.Format("15:04") != s.Cutoff {
			return nil, errors.New("刷新截止时间须为 HH:mm")
		}
		s.cutoffMinute = t.Hour()*60 + t.Minute()
	}
	return result, nil
}

type probeKey struct{}
type probeIdentity struct{ AccountID, KeyID string }
type recoveryKey struct{}

type Manager struct {
	mu          sync.Mutex
	path        string
	accounts    []Account
	schedules   []ProviderSchedule
	busy        map[string]bool
	probeTokens map[string]probeIdentity
	clock       func() time.Time
	// Keys is read from Bifrost's own live configuration; credentials are never saved here.
	Keys         func(string) []schemas.Key
	Models       func(string) []string
	Probe        func(context.Context, Account, string, string) *schemas.BifrostError
	AccountsPage []byte
}

func Open(path string) (*Manager, error) {
	m := &Manager{path: path, busy: map[string]bool{}, probeTokens: map[string]probeIdentity{}, clock: time.Now}
	m.schedules, _ = validateSchedules(defaultSchedules())
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
	if s.ProviderSchedules != nil {
		m.schedules, err = validateSchedules(s.ProviderSchedules)
		if err != nil {
			return nil, err
		}
	}
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
		for _, id := range a.KeyIDs {
			k := a.Provider + ":" + id
			if id == "" || keys[k] {
				return errors.New("每个 Bifrost 账号只能关联一个同上游账号组")
			}
			keys[k] = true
		}
	}
	return nil
}

func (m *Manager) saveLocked() error {
	b, err := json.MarshalIndent(diskState{Accounts: m.accounts, ProviderSchedules: m.schedules}, "", "  ")
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

func (m *Manager) Schedules() []ProviderSchedule {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ProviderSchedule(nil), m.schedules...)
}

// Replace edits policy only. UI clients cannot clear an exhaustion hold or its backoff.
func (m *Manager) Replace(accounts []Account, schedules []ProviderSchedule) error {
	return m.Edit(&accounts, &schedules)
}

// Separate page sections save only their own fields, against current server state.
func (m *Manager) Edit(accountEdit *[]Account, scheduleEdit *[]ProviderSchedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if accountEdit == nil && scheduleEdit == nil {
		return errors.New("没有可保存的配置")
	}
	accounts := append([]Account(nil), m.accounts...)
	if accountEdit != nil {
		accounts = *accountEdit
		for i, a := range accounts {
			if a.KeyIDs != nil {
				continue
			}
			for _, current := range m.accounts {
				if a.ID == current.ID {
					if a.Provider != current.Provider && len(current.KeyIDs) > 0 {
						return errors.New("先解除账号关联，再更改账号组上游")
					}
					accounts[i].KeyIDs = append([]string{}, current.KeyIDs...)
					break
				}
			}
		}
	}
	if err := validate(accounts); err != nil {
		return err
	}
	next := m.schedules
	if scheduleEdit != nil {
		var err error
		next, err = validateSchedules(*scheduleEdit)
		if err != nil {
			return err
		}
	}
	return m.replaceLocked(accounts, next)
}

func (m *Manager) Assign(provider, keyID, groupID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (provider != "openai" && provider != "anthropic") || keyID == "" {
		return errors.New("账号参数无效")
	}
	found := groupID == ""
	accounts := append([]Account(nil), m.accounts...)
	for i, a := range accounts {
		accounts[i].KeyIDs = []string{}
		for _, id := range a.KeyIDs {
			if a.Provider != provider || id != keyID {
				accounts[i].KeyIDs = append(accounts[i].KeyIDs, id)
			}
		}
		if a.ID == groupID {
			if a.Provider != provider {
				return errors.New("账号与账号组须属于同一上游")
			}
			found = true
			accounts[i].KeyIDs = append(accounts[i].KeyIDs, keyID)
		}
	}
	if !found {
		return errors.New("请先保存账号组，再关联账号")
	}
	if groupID != "" {
		foundKey := false
		if m.Keys != nil {
			for _, k := range m.Keys(provider) {
				if k.ID == keyID {
					foundKey = true
					break
				}
			}
		}
		if !foundKey {
			return errors.New("Bifrost 账号不存在")
		}
	}
	return m.replaceLocked(accounts, m.schedules)
}

func (m *Manager) replaceLocked(accounts []Account, next []ProviderSchedule) error {
	old, oldSchedules := m.accounts, m.schedules
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
	m.schedules = next
	if err := m.saveLocked(); err != nil {
		m.accounts = old
		m.schedules = oldSchedules
		return err
	}
	return nil
}

// RetryWindow returns the current or next window: 48 elapsed hours starting at
// the upstream's local refresh cutoff. Monthly anchors clamp to
// the month's last day without drifting; timezone rules include DST changes.
func RetryWindow(date, cycle string, now time.Time, schedule ProviderSchedule) (time.Time, time.Time) {
	zone := schedule.zone
	if zone == nil {
		return time.Time{}, time.Time{}
	}
	anchor, err := time.ParseInLocation("2006-01-02", date, zone)
	if err != nil {
		return time.Time{}, time.Time{}
	}
	t := now.In(zone)
	if t.Before(anchor) {
		t = anchor
	}
	var nextStart, nextEnd time.Time
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
		if refresh.Before(anchor) {
			continue
		}
		start := time.Date(y, mo, d, schedule.cutoffMinute/60, schedule.cutoffMinute%60, 0, 0, zone)
		end := start.Add(48 * time.Hour)
		if !now.Before(start) && now.Before(end) {
			return start, end
		}
		if now.Before(start) && (nextStart.IsZero() || start.Before(nextStart)) {
			nextStart, nextEnd = start, end
		}
	}
	return nextStart, nextEnd
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
	if a == nil || !a.Exhausted {
		return true
	}
	if m.busy[a.ID] || now.Before(a.NextRetry) {
		return false
	}
	for _, s := range m.schedules {
		if s.Provider == a.Provider {
			start, end := RetryWindow(a.RefreshDate, a.Cycle, now, s)
			return !start.IsZero() && !now.Before(start) && now.Before(end)
		}
	}
	return false
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
	preferred := ""
	for _, s := range m.schedules {
		if s.Provider == a.Provider {
			preferred = s.ProbeModel
		}
	}
	models := []string{}
	if m.Models != nil {
		models = append(models, m.Models(a.Provider)...)
	}
	rand.Shuffle(len(models), func(i, j int) { models[i], models[j] = models[j], models[i] })
	if preferred != "" {
		models = append([]string{preferred}, models...)
	}
	type candidate struct{ model, keyID string }
	candidates := []candidate{}
	seen := map[string]bool{}
	if m.Keys != nil {
		keys := m.Keys(a.Provider)
		for _, model := range models {
			model = strings.TrimPrefix(model, a.Provider+"/")
			if model == "" || seen[model] {
				continue
			}
			seen[model] = true
			foundKey := ""
			for _, k := range keys {
				if k.Enabled != nil && !*k.Enabled || !k.Models.IsAllowed(model) || k.BlacklistedModels.IsBlocked(model) {
					continue
				}
				for _, id := range a.KeyIDs {
					if k.ID == id {
						foundKey = k.ID
						break
					}
				}
				if foundKey != "" {
					break
				}
			}
			if foundKey != "" {
				candidates = append(candidates, candidate{model, foundKey})
			}
		}
	}
	if len(candidates) == 0 {
		m.mu.Unlock()
		return a, errors.New("账号组没有已启用且支持候选模型的账号；请检查 catalog、模型配置与账号关联")
	}
	if m.Probe == nil {
		m.mu.Unlock()
		return a, errors.New("探测接口未就绪")
	}
	m.busy[id] = true
	m.mu.Unlock()
	// One bounded button operation. Only explicit model incompatibility advances
	// to another catalog candidate; billing/auth/network errors stop immediately.
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var e *schemas.BifrostError
	model := ""
	for _, c := range candidates {
		model = c.model
		if probeCtx.Err() != nil {
			e = &schemas.BifrostError{IsBifrostError: true, Type: schemas.Ptr(schemas.RequestTimedOut), Error: &schemas.ErrorField{Message: "probe timed out"}}
			break
		}
		e = m.Probe(context.WithValue(probeCtx, probeKey{}, probeIdentity{id, c.keyID}), a, c.keyID, c.model)
		if e == nil || CreditError(a.Provider, e) || !UnsupportedModel(e) {
			break
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.busy, id)
	for i := range m.accounts {
		if m.accounts[i].ID == id {
			if e == nil {
				for j := range m.schedules {
					if m.schedules[j].Provider == a.Provider {
						m.schedules[j].ProbeModel = model
					}
				}
			}
			err := m.finishLocked(&m.accounts[i], e)
			return m.accounts[i], err
		}
	}
	return a, errors.New("账号已删除")
}

func UnsupportedModel(e *schemas.BifrostError) bool {
	if e == nil || e.Error == nil {
		return false
	}
	if e.StatusCode != nil && (*e.StatusCode == 401 || *e.StatusCode == 429 || *e.StatusCode >= 500) {
		return false
	}
	for _, v := range []*string{e.Type, e.Error.Type, e.Error.Code} {
		if v != nil && (*v == "authentication_error" || *v == "permission_error" || *v == "invalid_api_key") {
			return false
		}
	}
	message := strings.ToLower(e.Error.Message)
	if param, ok := e.Error.Param.(string); ok && param != "" {
		if param != "model" {
			return false
		}
		if strings.Contains(message, "not supported") || strings.Contains(message, "unsupported") || strings.Contains(message, "not available") {
			return true
		}
	}
	for _, v := range []*string{e.Type, e.Error.Type, e.Error.Code} {
		if v == nil {
			continue
		}
		switch *v {
		case "model_not_found", "unsupported_model", "model_not_supported", "policy_model_blocked", "no_key_supports_model":
			return true
		case "not_found_error":
			if strings.Contains(strings.ToLower(e.Error.Message), "model") {
				return true
			}
		}
	}
	return strings.Contains(message, "model") && (strings.Contains(message, "does not exist") || strings.Contains(message, "model not found") || strings.Contains(message, "model is not supported") || strings.Contains(message, "not a valid model") || strings.Contains(message, "does not support this model"))
}
