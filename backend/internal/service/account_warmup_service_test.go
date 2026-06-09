package service

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("invalid date %q: %v", s, err)
	}
	return d
}

func TestWorkdayCalendar_IsWorkday(t *testing.T) {
	cal := NewWorkdayCalendar(
		// Holidays: 2026 May 1–5 (mock 五一)
		[]string{"2026-05-01", "2026-05-02", "2026-05-03", "2026-05-04", "2026-05-05"},
		// Extra workdays: makeup days commonly attached to 五一
		[]string{"2026-04-26", "2026-05-09"},
	)

	tests := []struct {
		name string
		date string
		want bool
	}{
		{"plain weekday Monday", "2026-05-11", true},
		{"plain Saturday", "2026-05-16", false},
		{"plain Sunday", "2026-05-17", false},
		{"holiday Friday", "2026-05-01", false},
		{"holiday Monday", "2026-05-04", false},
		{"makeup Sunday", "2026-04-26", true},
		{"makeup Saturday", "2026-05-09", true},
		{"date not in list, weekday", "2026-06-15", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cal.IsWorkday(mustDate(t, tc.date))
			if got != tc.want {
				t.Errorf("IsWorkday(%s) = %v, want %v", tc.date, got, tc.want)
			}
		})
	}
}

func TestWorkdayCalendar_NilSafe(t *testing.T) {
	var cal *WorkdayCalendar
	if !cal.IsWorkday(mustDate(t, "2026-05-11")) {
		t.Error("nil calendar should treat weekday Monday as workday")
	}
	if cal.IsWorkday(mustDate(t, "2026-05-16")) {
		t.Error("nil calendar should treat Saturday as non-workday")
	}
}

func TestWorkdayCalendar_ExtraOverridesHoliday(t *testing.T) {
	// Pathological but well-defined: same date in both lists → extra wins.
	cal := NewWorkdayCalendar(
		[]string{"2026-05-01"},
		[]string{"2026-05-01"},
	)
	if !cal.IsWorkday(mustDate(t, "2026-05-01")) {
		t.Error("extra workday should override holiday entry on same date")
	}
}

func TestParseStringJSONArray(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"json array", `["2026-05-01","2026-05-02"]`, []string{"2026-05-01", "2026-05-02"}},
		{"json with whitespace", `[" 2026-05-01 ", "", "2026-05-02"]`, []string{"2026-05-01", "2026-05-02"}},
		{"newline separated", "2026-05-01\n2026-05-02", []string{"2026-05-01", "2026-05-02"}},
		{"comma separated", "2026-05-01,2026-05-02", []string{"2026-05-01", "2026-05-02"}},
		{"mixed separators", "2026-05-01;  2026-05-02\n2026-05-03", []string{"2026-05-01", "2026-05-02", "2026-05-03"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseStringJSONArray(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d (%v), want %d (%v)", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestCronCrossed(t *testing.T) {
	// 8:00 every day
	spec := "0 8 * * *"
	loc := time.UTC
	day := func(h, m int) time.Time { return time.Date(2026, 5, 21, h, m, 0, 0, loc) }

	tests := []struct {
		name string
		prev time.Time
		now  time.Time
		want bool
	}{
		{"both before", day(7, 0), day(7, 59), false},
		{"crosses 8:00 boundary", day(7, 59), day(8, 0), true},
		{"both after", day(8, 1), day(8, 5), false},
		{"prev zero", time.Time{}, day(8, 0), true},
		{"reversed (prev after now)", day(9, 0), day(8, 0), true}, // prev reset to now-1min
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cronCrossed(spec, tc.prev, tc.now)
			if got != tc.want {
				t.Errorf("cronCrossed(prev=%v, now=%v) = %v, want %v", tc.prev, tc.now, got, tc.want)
			}
		})
	}
}

func TestCronCrossed_InvalidSpec(t *testing.T) {
	if cronCrossed("nonsense", time.Now().Add(-time.Minute), time.Now()) {
		t.Error("invalid cron spec should never report crossed")
	}
	if cronCrossed("", time.Now().Add(-time.Minute), time.Now()) {
		t.Error("empty cron spec should never report crossed")
	}
}

func TestBuildWarmupCardBody(t *testing.T) {
	summary := &WarmupSummary{
		ExecutedAt: time.Date(2026, 5, 21, 8, 0, 12, 0, time.UTC),
		Source:     "schedule",
		Platforms:  []string{"anthropic", "openai"},
		Total:      3,
		Successes: []WarmupAccountResult{
			{AccountID: 1, Name: "ant-1", Platform: "anthropic", LatencyMs: 320},
			{AccountID: 2, Name: "oai-1", Platform: "openai", LatencyMs: 410},
		},
		Failures: []WarmupAccountResult{
			{AccountID: 3, Name: "ant-2", Platform: "anthropic", Error: "401 invalid_api_key"},
		},
		DurationMs: 1234,
	}
	body := buildWarmupCardBody(summary)
	mustContain := []string{
		"时间：2026-05-21",
		"触发来源：schedule",
		"覆盖平台：anthropic, openai",
		"共处理：3 个账号",
		"✅ 成功：2",
		"❌ 失败：1",
		"ant-2",
		"401 invalid_api_key",
	}
	for _, s := range mustContain {
		if !contains(body, s) {
			t.Errorf("body missing %q\nbody=%s", s, body)
		}
	}
}

func TestBuildWarmupCardBody_TruncatesFailures(t *testing.T) {
	failures := make([]WarmupAccountResult, 12)
	for i := range failures {
		failures[i] = WarmupAccountResult{
			AccountID: int64(i),
			Name:      "acc",
			Platform:  "anthropic",
			Error:     "boom",
		}
	}
	body := buildWarmupCardBody(&WarmupSummary{
		ExecutedAt: time.Now(),
		Failures:   failures,
		Total:      12,
	})
	if !contains(body, "+ 4 more") {
		t.Errorf("expected 'more' truncation summary, got:\n%s", body)
	}
}

// ---- stubs for idempotency & lock tests ----

// warmupSettingStub is a minimal SettingRepository stub for warmup tests.
type warmupSettingStub struct {
	values  map[string]string
	setCalls []string // keys passed to Set
	setErr  error
}

func (s *warmupSettingStub) Get(_ context.Context, _ string) (*Setting, error) {
	panic("unexpected: Get")
}
func (s *warmupSettingStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}
func (s *warmupSettingStub) Set(_ context.Context, key, _ string) error {
	s.setCalls = append(s.setCalls, key)
	return s.setErr
}
func (s *warmupSettingStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = s.values[k]
	}
	return out, nil
}
func (s *warmupSettingStub) SetMultiple(_ context.Context, _ map[string]string) error {
	panic("unexpected: SetMultiple")
}
func (s *warmupSettingStub) GetAll(_ context.Context) (map[string]string, error) {
	panic("unexpected: GetAll")
}
func (s *warmupSettingStub) Delete(_ context.Context, _ string) error {
	panic("unexpected: Delete")
}

var _ SettingRepository = (*warmupSettingStub)(nil)

// warmupAccountStub is a minimal AccountRepository stub that delegates only
// ListSchedulableByPlatforms; all other methods panic.
type warmupAccountStub struct {
	listResult []Account
	listErr    error
}

func (s *warmupAccountStub) ListSchedulableByPlatforms(_ context.Context, _ []string) ([]Account, error) {
	return s.listResult, s.listErr
}

// -- all unused methods panic --

func (s *warmupAccountStub) Create(context.Context, *Account) error                    { panic("unexpected") }
func (s *warmupAccountStub) GetByID(context.Context, int64) (*Account, error)          { panic("unexpected") }
func (s *warmupAccountStub) GetByIDs(context.Context, []int64) ([]*Account, error)     { panic("unexpected") }
func (s *warmupAccountStub) ExistsByID(context.Context, int64) (bool, error)           { panic("unexpected") }
func (s *warmupAccountStub) GetByCRSAccountID(context.Context, string) (*Account, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) FindByExtraField(context.Context, string, any) ([]Account, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) ListCRSAccountIDs(context.Context) (map[string]int64, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) Update(context.Context, *Account) error        { panic("unexpected") }
func (s *warmupAccountStub) Delete(context.Context, int64) error           { panic("unexpected") }
func (s *warmupAccountStub) List(context.Context, pagination.PaginationParams) ([]Account, *pagination.PaginationResult, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, string, int64, string) ([]Account, *pagination.PaginationResult, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) ListByGroup(context.Context, int64) ([]Account, error) { panic("unexpected") }
func (s *warmupAccountStub) ListActive(context.Context) ([]Account, error)          { panic("unexpected") }
func (s *warmupAccountStub) ListByPlatform(context.Context, string) ([]Account, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) UpdateLastUsed(context.Context, int64) error { panic("unexpected") }
func (s *warmupAccountStub) BatchUpdateLastUsed(context.Context, map[int64]time.Time) error {
	panic("unexpected")
}
func (s *warmupAccountStub) SetError(context.Context, int64, string) error { panic("unexpected") }
func (s *warmupAccountStub) ClearError(context.Context, int64) error       { panic("unexpected") }
func (s *warmupAccountStub) SetSchedulable(context.Context, int64, bool) error {
	panic("unexpected")
}
func (s *warmupAccountStub) AutoPauseExpiredAccounts(context.Context, time.Time) (int64, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) BindGroups(context.Context, int64, []int64) error { panic("unexpected") }
func (s *warmupAccountStub) ListSchedulable(context.Context) ([]Account, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) ListSchedulableByPlatform(context.Context, string) ([]Account, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Account, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]Account, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) ListExpiredRateLimitedAccounts(context.Context) ([]Account, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) SetRateLimited(context.Context, int64, time.Time) error {
	panic("unexpected")
}
func (s *warmupAccountStub) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	panic("unexpected")
}
func (s *warmupAccountStub) SetOverloaded(context.Context, int64, time.Time) error {
	panic("unexpected")
}
func (s *warmupAccountStub) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	panic("unexpected")
}
func (s *warmupAccountStub) ClearTempUnschedulable(context.Context, int64) error {
	panic("unexpected")
}
func (s *warmupAccountStub) ClearRateLimit(context.Context, int64) error             { panic("unexpected") }
func (s *warmupAccountStub) ClearAntigravityQuotaScopes(context.Context, int64) error { panic("unexpected") }
func (s *warmupAccountStub) ClearModelRateLimits(context.Context, int64) error       { panic("unexpected") }
func (s *warmupAccountStub) RevertProxyFallback(context.Context, int64) error         { panic("unexpected") }
func (s *warmupAccountStub) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	panic("unexpected")
}
func (s *warmupAccountStub) UpdateSessionWindowEnd(context.Context, int64, time.Time) error {
	panic("unexpected")
}
func (s *warmupAccountStub) UpdateExtra(context.Context, int64, map[string]any) error { panic("unexpected") }
func (s *warmupAccountStub) BulkUpdate(context.Context, []int64, AccountBulkUpdate) (int64, error) {
	panic("unexpected")
}
func (s *warmupAccountStub) IncrementQuotaUsed(context.Context, int64, float64) error { panic("unexpected") }
func (s *warmupAccountStub) ResetQuotaUsed(context.Context, int64) error               { panic("unexpected") }

var _ AccountRepository = (*warmupAccountStub)(nil)

// baseWarmupSettings returns a map with all warmup settings that loadConfig reads,
// pre-filled with sensible defaults for testing.
func baseWarmupSettings(today string) map[string]string {
	return map[string]string{
		SettingKeyScheduledWarmupEnabled:       "true",
		SettingKeyScheduledWarmupCron:          accountWarmupDefaultCron,
		SettingKeyScheduledWarmupWorkdayOnly:   "false", // simplify: skip workday check
		SettingKeyScheduledWarmupHolidays:      "[]",
		SettingKeyScheduledWarmupExtraWorkdays: "[]",
		SettingKeyScheduledWarmupPlatforms:     `["anthropic"]`,
		SettingKeyScheduledWarmupLastRunDate:   "", // not run yet
	}
}

// newWarmupServiceForTest builds an AccountWarmupService wired to the given stubs.
// redisClient is nil so tryAcquireLeaderLock skips the actual lock.
func newWarmupServiceForTest(settingRepo SettingRepository, accountRepo AccountRepository) *AccountWarmupService {
	svc := &AccountWarmupService{
		settingRepo:       settingRepo,
		accountRepo:       accountRepo,
		accountTestSvc:    &AccountTestService{}, // non-nil guard; RunTestBackground never called
		feishuSvc:         nil,
		redisClient:       nil, // no Redis → tryAcquireLeaderLock returns (nil, true) after warning
		distributedLockOn: true,
		instanceID:        "test-instance",
		loc:               time.UTC,
	}
	svc.stopCtx, svc.stop = context.WithCancel(context.Background())
	return svc
}

// TestRunNow_RejectsWhenAlreadyRanUnderLock verifies that RunNow(force=false) re-checks
// last_run_date under the lock and rejects a second concurrent call even when the first
// call's cfg snapshot showed an empty date.
func TestRunNow_RejectsWhenAlreadyRanUnderLock(t *testing.T) {
	today := time.Now().In(time.UTC).Format("2006-01-02")

	values := baseWarmupSettings(today)
	// First loadConfig sees empty lastRunDate (first check passes).
	// GetValue (called inside lock) returns today — simulating another call having just
	// written the idempotency key.
	var getValueCallCount atomic.Int32
	stub := &warmupSettingStub{values: values}

	// Override GetValue to return today on the second call (the one inside the lock).
	// We can't partially override so we use a wrapper.
	wrappedStub := &getValueOverrideStub{
		warmupSettingStub: stub,
		overrideKey:       SettingKeyScheduledWarmupLastRunDate,
		overrideValue:     today,
		callCount:         &getValueCallCount,
	}

	svc := newWarmupServiceForTest(wrappedStub, &warmupAccountStub{})
	_, err := svc.RunNow(context.Background(), false)
	if err == nil {
		t.Fatal("expected error from lock-internal idempotency check, got nil")
	}
	if !strings.Contains(err.Error(), "already executed today") {
		t.Errorf("unexpected error: %v", err)
	}
}

// getValueOverrideStub wraps warmupSettingStub and overrides GetValue for a specific key.
type getValueOverrideStub struct {
	*warmupSettingStub
	overrideKey   string
	overrideValue string
	callCount     *atomic.Int32
}

func (s *getValueOverrideStub) GetValue(_ context.Context, key string) (string, error) {
	if key == s.overrideKey {
		s.callCount.Add(1)
		return s.overrideValue, nil
	}
	return s.warmupSettingStub.values[key], nil
}

// TestExecuteAndReport_SkipsIdempotencyWriteOnListError verifies that when
// ListSchedulableByPlatforms returns an error, last_run_date is NOT written.
func TestExecuteAndReport_SkipsIdempotencyWriteOnListError(t *testing.T) {
	today := time.Now().In(time.UTC).Format("2006-01-02")
	settingStub := &warmupSettingStub{values: baseWarmupSettings(today)}
	accountStub := &warmupAccountStub{listErr: errors.New("db timeout")}

	svc := newWarmupServiceForTest(settingStub, accountStub)

	cfg := &warmupConfig{
		enabled:     true,
		cronSpec:    accountWarmupDefaultCron,
		workdayOnly: false,
		platforms:   []string{"anthropic"},
		lastRunDate: "",
		calendar:    NewWorkdayCalendar(nil, nil),
	}
	summary := svc.executeAndReport(cfg, time.Now().In(time.UTC), "manual")

	if summary.ListError == "" {
		t.Fatal("expected ListError to be set")
	}
	for _, key := range settingStub.setCalls {
		if key == SettingKeyScheduledWarmupLastRunDate {
			t.Errorf("Set(%q) was called despite list error; idempotency key must NOT be written on list failure", key)
		}
	}
}

// TestExecuteAndReport_WritesIdempotencyWhenListSucceeds verifies that when account
// listing succeeds (even with 0 accounts), last_run_date IS written.
func TestExecuteAndReport_WritesIdempotencyWhenListSucceeds(t *testing.T) {
	today := time.Now().In(time.UTC).Format("2006-01-02")
	settingStub := &warmupSettingStub{values: baseWarmupSettings(today)}
	accountStub := &warmupAccountStub{listResult: []Account{}} // empty list, no error

	svc := newWarmupServiceForTest(settingStub, accountStub)

	cfg := &warmupConfig{
		enabled:     true,
		cronSpec:    accountWarmupDefaultCron,
		workdayOnly: false,
		platforms:   []string{"anthropic"},
		lastRunDate: "",
		calendar:    NewWorkdayCalendar(nil, nil),
	}
	svc.executeAndReport(cfg, time.Now().In(time.UTC), "manual")

	wrote := false
	for _, key := range settingStub.setCalls {
		if key == SettingKeyScheduledWarmupLastRunDate {
			wrote = true
		}
	}
	if !wrote {
		t.Error("expected Set(SettingKeyScheduledWarmupLastRunDate) to be called when list succeeds with 0 accounts")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
