package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/robfig/cron/v3"
)

const (
	accountWarmupDefaultMaxWorkers = 10
	accountWarmupDefaultCron       = "0 8 * * *"
	accountWarmupTickInterval      = 1 * time.Minute
	accountWarmupLeaderLockKey     = "scheduled_warmup:leader"
	accountWarmupLeaderLockTTL     = 15 * time.Minute
)

var accountWarmupCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

var accountWarmupReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

// AccountWarmupService runs a daily warmup against all schedulable accounts so
// that the upstream 5-hour rate-limit window starts ticking at a known time
// (typically 08:00 on workdays). It posts a single summary card to Feishu.
type AccountWarmupService struct {
	settingRepo    SettingRepository
	accountRepo    AccountRepository
	accountTestSvc *AccountTestService
	feishuSvc      *FeishuWebhookService
	redisClient    *redis.Client
	cfg            *config.Config

	instanceID string
	loc        *time.Location

	distributedLockOn bool
	warnNoRedisOnce   sync.Once

	startOnce sync.Once
	stopOnce  sync.Once
	stopCtx   context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup

	// lastTick tracks the previous ticker fire time so cron.Next(prev) can detect
	// whether `now` crossed a scheduled boundary inside this tick interval.
	lastTickMu sync.Mutex
	lastTick   time.Time
}

// NewAccountWarmupService constructs the service. Call Start to begin the
// background ticker.
func NewAccountWarmupService(
	settingRepo SettingRepository,
	accountRepo AccountRepository,
	accountTestSvc *AccountTestService,
	feishuSvc *FeishuWebhookService,
	redisClient *redis.Client,
	cfg *config.Config,
) *AccountWarmupService {
	lockOn := cfg == nil || strings.TrimSpace(cfg.RunMode) != config.RunModeSimple

	loc := time.Local
	if cfg != nil && strings.TrimSpace(cfg.Timezone) != "" {
		if parsed, err := time.LoadLocation(strings.TrimSpace(cfg.Timezone)); err == nil && parsed != nil {
			loc = parsed
		}
	}

	return &AccountWarmupService{
		settingRepo:       settingRepo,
		accountRepo:       accountRepo,
		accountTestSvc:    accountTestSvc,
		feishuSvc:         feishuSvc,
		redisClient:       redisClient,
		cfg:               cfg,
		instanceID:        uuid.NewString(),
		loc:               loc,
		distributedLockOn: lockOn,
	}
}

// Start begins the per-minute ticker that evaluates the cron expression and
// triggers warmups when due.
func (s *AccountWarmupService) Start() {
	s.StartWithContext(context.Background())
}

// StartWithContext is like Start but bound to the provided context.
func (s *AccountWarmupService) StartWithContext(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.accountRepo == nil || s.accountTestSvc == nil {
		return
	}
	s.startOnce.Do(func() {
		s.stopCtx, s.stop = context.WithCancel(ctx)
		s.lastTickMu.Lock()
		s.lastTick = s.now()
		s.lastTickMu.Unlock()
		s.wg.Add(1)
		go s.run()
	})
}

// Stop gracefully shuts the ticker down.
func (s *AccountWarmupService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
	})
	s.wg.Wait()
}

func (s *AccountWarmupService) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(accountWarmupTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCtx.Done():
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

// tick runs each minute. It returns early unless: enabled + cron crossed in this
// interval + (optional) workday + not yet run today + leader lock acquired.
func (s *AccountWarmupService) tick() {
	prev := s.advanceLastTick()
	now := s.now()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, ok := s.loadConfig(ctx)
	if !ok || !cfg.enabled {
		return
	}
	if !cronCrossed(cfg.cronSpec, prev, now) {
		return
	}
	if cfg.workdayOnly && !cfg.calendar.IsWorkday(now) {
		return
	}
	if cfg.lastRunDate == now.Format("2006-01-02") {
		return
	}

	release, ok := s.tryAcquireLeaderLock(ctx)
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}

	// Re-check the "already ran today" guard under the lock to avoid two leaders
	// across cycles double-firing if state is read before the previous run wrote.
	if latest, err := s.settingRepo.GetValue(ctx, SettingKeyScheduledWarmupLastRunDate); err == nil {
		if strings.TrimSpace(latest) == now.Format("2006-01-02") {
			return
		}
	}

	s.executeAndReport(cfg, now, "schedule")
}

// RunNow triggers an immediate warmup, ignoring the cron schedule and
// workday guard. It still respects the "already ran today" idempotency unless
// `force` is true. Returns the summary so an admin endpoint can echo it.
func (s *AccountWarmupService) RunNow(ctx context.Context, force bool) (*WarmupSummary, error) {
	if s == nil || s.accountRepo == nil || s.accountTestSvc == nil {
		return nil, fmt.Errorf("warmup service not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, ok := s.loadConfig(ctx)
	if !ok {
		return nil, fmt.Errorf("failed to load warmup config")
	}
	now := s.now()
	if !force && cfg.lastRunDate == now.Format("2006-01-02") {
		return nil, fmt.Errorf("already executed today: %s", cfg.lastRunDate)
	}

	release, ok := s.tryAcquireLeaderLock(ctx)
	if !ok {
		return nil, fmt.Errorf("another instance is running warmup")
	}
	if release != nil {
		defer release()
	}

	// Re-check under the lock to guard against concurrent RunNow calls.
	if !force {
		if latest, err := s.settingRepo.GetValue(ctx, SettingKeyScheduledWarmupLastRunDate); err == nil {
			if strings.TrimSpace(latest) == now.Format("2006-01-02") {
				return nil, fmt.Errorf("already executed today: %s", strings.TrimSpace(latest))
			}
		}
	}

	return s.executeAndReport(cfg, now, "manual"), nil
}

func (s *AccountWarmupService) executeAndReport(cfg *warmupConfig, now time.Time, source string) *WarmupSummary {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	summary := s.runOnce(ctx, cfg, now)
	summary.Source = source

	// Persist last_run_date BEFORE sending the card so a slow webhook doesn't
	// block the idempotency guarantee. Skip when account listing failed so
	// the next cron tick can retry automatically.
	if summary.ListError == "" {
		if err := s.settingRepo.Set(ctx, SettingKeyScheduledWarmupLastRunDate, now.Format("2006-01-02")); err != nil {
			slog.Warn("scheduled_warmup: failed to persist last_run_date", "error", err)
		}
	}

	if s.feishuSvc != nil {
		s.feishuSvc.SendScheduledWarmupSummary(ctx, summary)
	}
	slog.Info("scheduled_warmup: completed",
		"source", source,
		"total", summary.Total,
		"success", len(summary.Successes),
		"failed", len(summary.Failures),
		"duration_ms", summary.DurationMs,
	)
	return summary
}

func (s *AccountWarmupService) runOnce(ctx context.Context, cfg *warmupConfig, now time.Time) *WarmupSummary {
	startedAt := time.Now()
	summary := &WarmupSummary{
		ExecutedAt: now,
		Platforms:  cfg.platforms,
	}

	accounts, err := s.accountRepo.ListSchedulableByPlatforms(ctx, cfg.platforms)
	if err != nil {
		slog.Error("scheduled_warmup: list accounts failed", "error", err)
		summary.ListError = err.Error()
		summary.DurationMs = time.Since(startedAt).Milliseconds()
		return summary
	}
	summary.Total = len(accounts)
	if len(accounts) == 0 {
		summary.DurationMs = time.Since(startedAt).Milliseconds()
		return summary
	}

	sem := make(chan struct{}, accountWarmupDefaultMaxWorkers)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := range accounts {
		acc := accounts[i]
		sem <- struct{}{}
		wg.Add(1)
		go func(a Account) {
			defer wg.Done()
			defer func() { <-sem }()

			itemCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()

			result, terr := s.accountTestSvc.RunTestBackground(itemCtx, a.ID, "")
			mu.Lock()
			defer mu.Unlock()
			if terr != nil || result == nil || result.Status != "success" {
				msg := ""
				if result != nil {
					msg = result.ErrorMessage
				}
				if msg == "" && terr != nil {
					msg = terr.Error()
				}
				if msg == "" {
					msg = "unknown error"
				}
				summary.Failures = append(summary.Failures, WarmupAccountResult{
					AccountID: a.ID,
					Name:      a.Name,
					Platform:  a.Platform,
					Error:     msg,
				})
				return
			}
			latency := int64(0)
			if result != nil {
				latency = result.LatencyMs
			}
			summary.Successes = append(summary.Successes, WarmupAccountResult{
				AccountID: a.ID,
				Name:      a.Name,
				Platform:  a.Platform,
				LatencyMs: latency,
			})
		}(acc)
	}
	wg.Wait()
	summary.DurationMs = time.Since(startedAt).Milliseconds()
	return summary
}

// ---- helpers ----

type warmupConfig struct {
	enabled     bool
	cronSpec    string
	workdayOnly bool
	platforms   []string
	lastRunDate string
	calendar    *WorkdayCalendar
}

func (s *AccountWarmupService) loadConfig(ctx context.Context) (*warmupConfig, bool) {
	keys := []string{
		SettingKeyScheduledWarmupEnabled,
		SettingKeyScheduledWarmupCron,
		SettingKeyScheduledWarmupWorkdayOnly,
		SettingKeyScheduledWarmupHolidays,
		SettingKeyScheduledWarmupExtraWorkdays,
		SettingKeyScheduledWarmupPlatforms,
		SettingKeyScheduledWarmupLastRunDate,
	}
	values, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		slog.Warn("scheduled_warmup: load settings failed", "error", err)
		return nil, false
	}

	cronSpec := strings.TrimSpace(values[SettingKeyScheduledWarmupCron])
	if cronSpec == "" {
		cronSpec = accountWarmupDefaultCron
	}

	platforms := parseStringJSONArray(values[SettingKeyScheduledWarmupPlatforms])
	if len(platforms) == 0 {
		platforms = append([]string(nil), AllowedQuotaPlatforms...)
	}

	calendar := NewWorkdayCalendar(
		parseStringJSONArray(values[SettingKeyScheduledWarmupHolidays]),
		parseStringJSONArray(values[SettingKeyScheduledWarmupExtraWorkdays]),
	)

	return &warmupConfig{
		enabled:     values[SettingKeyScheduledWarmupEnabled] == "true",
		cronSpec:    cronSpec,
		workdayOnly: values[SettingKeyScheduledWarmupWorkdayOnly] != "false", // default true
		platforms:   platforms,
		lastRunDate: strings.TrimSpace(values[SettingKeyScheduledWarmupLastRunDate]),
		calendar:    calendar,
	}, true
}

func (s *AccountWarmupService) now() time.Time {
	t := time.Now()
	if s.loc != nil {
		return t.In(s.loc)
	}
	return t
}

func (s *AccountWarmupService) advanceLastTick() time.Time {
	s.lastTickMu.Lock()
	defer s.lastTickMu.Unlock()
	prev := s.lastTick
	s.lastTick = s.now()
	if prev.IsZero() {
		prev = s.lastTick.Add(-accountWarmupTickInterval)
	}
	return prev
}

func (s *AccountWarmupService) tryAcquireLeaderLock(ctx context.Context) (func(), bool) {
	if s == nil || !s.distributedLockOn {
		return nil, true
	}
	if s.redisClient == nil {
		s.warnNoRedisOnce.Do(func() {
			slog.Warn("scheduled_warmup: redis not configured; running without distributed lock")
		})
		return nil, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := s.redisClient.SetNX(ctx, accountWarmupLeaderLockKey, s.instanceID, accountWarmupLeaderLockTTL).Result()
	if err != nil {
		slog.Warn("scheduled_warmup: leader lock SetNX failed; skipping this cycle", "error", err)
		return nil, false
	}
	if !ok {
		return nil, false
	}
	return func() {
		_, _ = accountWarmupReleaseScript.Run(ctx, s.redisClient, []string{accountWarmupLeaderLockKey}, s.instanceID).Result()
	}, true
}

// ValidateWarmupCron returns an error if the cron expression is not a valid
// 5-field schedule. Empty string is treated as "use default" and accepted.
func ValidateWarmupCron(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	_, err := accountWarmupCronParser.Parse(spec)
	return err
}

// ValidateWarmupDateList returns the first invalid date in the slice, or empty
// string if all entries parse as "2006-01-02". Empty / whitespace entries are
// ignored.
func ValidateWarmupDateList(list []string) string {
	for _, v := range list {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return v
		}
	}
	return ""
}

// cronCrossed reports whether the given cron expression has a fire time within
// the half-open interval (prev, now]. It is robust against minor tick drift.
func cronCrossed(spec string, prev, now time.Time) bool {
	if strings.TrimSpace(spec) == "" {
		return false
	}
	sched, err := accountWarmupCronParser.Parse(spec)
	if err != nil {
		return false
	}
	if prev.IsZero() || !prev.Before(now) {
		prev = now.Add(-accountWarmupTickInterval)
	}
	next := sched.Next(prev)
	return !next.After(now)
}

// parseStringJSONArray accepts a JSON array of strings; falls back to splitting
// by newline/comma/semicolon for ergonomic admin editing.
func parseStringJSONArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err == nil {
			cleaned := make([]string, 0, len(out))
			for _, v := range out {
				v = strings.TrimSpace(v)
				if v != "" {
					cleaned = append(cleaned, v)
				}
			}
			return cleaned
		}
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '\n', '\r', '\t', ';', '；', '，':
			return true
		}
		return false
	})
	cleaned := make([]string, 0, len(parts))
	for _, v := range parts {
		v = strings.TrimSpace(v)
		if v != "" {
			cleaned = append(cleaned, v)
		}
	}
	return cleaned
}

// ---- WorkdayCalendar ----

// WorkdayCalendar decides whether a given date is a workday given the
// configured holiday list (e.g. 春节) and extra-workday list (e.g. 补班).
type WorkdayCalendar struct {
	holidays      map[string]struct{}
	extraWorkdays map[string]struct{}
}

// NewWorkdayCalendar builds a calendar from two lists of "YYYY-MM-DD" strings.
// Invalid entries are skipped silently — callers should validate at write time.
func NewWorkdayCalendar(holidays, extraWorkdays []string) *WorkdayCalendar {
	c := &WorkdayCalendar{
		holidays:      make(map[string]struct{}, len(holidays)),
		extraWorkdays: make(map[string]struct{}, len(extraWorkdays)),
	}
	for _, d := range holidays {
		if _, err := time.Parse("2006-01-02", d); err == nil {
			c.holidays[d] = struct{}{}
		}
	}
	for _, d := range extraWorkdays {
		if _, err := time.Parse("2006-01-02", d); err == nil {
			c.extraWorkdays[d] = struct{}{}
		}
	}
	return c
}

// IsWorkday reports whether t falls on a workday.
//
// Precedence: extra-workday > holiday > weekday default.
// Holiday and extra-workday lists are independent — the same date appearing in
// both means "补班 wins" (because the explicit override is the more recent
// signal: the holiday list says "May 1 is a holiday", but a补班 entry for it
// would override that — admins should not normally configure this).
func (c *WorkdayCalendar) IsWorkday(t time.Time) bool {
	if c == nil {
		wd := t.Weekday()
		return wd != time.Saturday && wd != time.Sunday
	}
	d := t.Format("2006-01-02")
	if _, ok := c.extraWorkdays[d]; ok {
		return true
	}
	if _, ok := c.holidays[d]; ok {
		return false
	}
	wd := t.Weekday()
	return wd != time.Saturday && wd != time.Sunday
}

// ---- WarmupSummary ----

// WarmupSummary captures the outcome of a single warmup execution.
type WarmupSummary struct {
	ExecutedAt time.Time
	Source     string // "schedule" or "manual"
	Platforms  []string
	Total      int
	Successes  []WarmupAccountResult
	Failures   []WarmupAccountResult
	DurationMs int64
	ListError  string
}

// WarmupAccountResult is the per-account outcome.
type WarmupAccountResult struct {
	AccountID int64
	Name      string
	Platform  string
	LatencyMs int64
	Error     string
}
