package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// loginBruteforceSettingStub is a minimal SettingRepository stub for these tests.
type loginBruteforceSettingStub struct {
	values map[string]string
}

func (s *loginBruteforceSettingStub) Get(_ context.Context, _ string) (*Setting, error) {
	panic("unexpected: Get")
}
func (s *loginBruteforceSettingStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}
func (s *loginBruteforceSettingStub) Set(_ context.Context, _, _ string) error {
	panic("unexpected: Set")
}
func (s *loginBruteforceSettingStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = s.values[k]
	}
	return out, nil
}
func (s *loginBruteforceSettingStub) SetMultiple(_ context.Context, _ map[string]string) error {
	panic("unexpected: SetMultiple")
}
func (s *loginBruteforceSettingStub) GetAll(_ context.Context) (map[string]string, error) {
	panic("unexpected: GetAll")
}
func (s *loginBruteforceSettingStub) Delete(_ context.Context, _ string) error {
	panic("unexpected: Delete")
}

var _ SettingRepository = (*loginBruteforceSettingStub)(nil)

func newLoginBruteforceTestService(t *testing.T, values map[string]string) (*LoginBruteforceService, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	if values == nil {
		values = map[string]string{}
	}
	settingRepo := &loginBruteforceSettingStub{values: values}
	// feishuWebhook left nil: notifyBan no-ops without it, which is fine for these tests.
	svc := NewLoginBruteforceService(settingRepo, rdb, nil)
	return svc, mr
}

func TestLoginBruteforceService_BansExactlyAtThreshold(t *testing.T) {
	svc, _ := newLoginBruteforceTestService(t, map[string]string{
		SettingKeyLoginBruteforceMaxFailures:   "3",
		SettingKeyLoginBruteforceWindowMinutes: "5",
		SettingKeyLoginBruteforceBanMinutes:    "60",
	})
	ctx := context.Background()
	ip := "1.2.3.4"

	for i := 1; i < 3; i++ {
		banned, failures, err := svc.RecordFailure(ctx, ip)
		if err != nil {
			t.Fatalf("RecordFailure #%d: unexpected error: %v", i, err)
		}
		if banned {
			t.Fatalf("RecordFailure #%d: banned too early (failures=%d)", i, failures)
		}
		if failures != i {
			t.Fatalf("RecordFailure #%d: expected failures=%d, got %d", i, i, failures)
		}
	}

	banned, failures, err := svc.RecordFailure(ctx, ip)
	if err != nil {
		t.Fatalf("RecordFailure #3: unexpected error: %v", err)
	}
	if !banned {
		t.Fatalf("RecordFailure #3: expected ban at threshold, got banned=false (failures=%d)", failures)
	}
	if failures != 3 {
		t.Fatalf("expected failures=3 at ban time, got %d", failures)
	}

	isBanned, ttl, err := svc.IsBanned(ctx, ip)
	if err != nil {
		t.Fatalf("IsBanned: unexpected error: %v", err)
	}
	if !isBanned {
		t.Fatalf("expected IP to be banned")
	}
	if ttl <= 0 || ttl > 60*time.Minute {
		t.Fatalf("expected ban TTL in (0, 60m], got %v", ttl)
	}
}

func TestLoginBruteforceService_DisabledDoesNotBan(t *testing.T) {
	svc, _ := newLoginBruteforceTestService(t, map[string]string{
		SettingKeyFeishuLoginBruteforceAutobanEnabled: "false",
		SettingKeyLoginBruteforceMaxFailures:          "1",
	})
	ctx := context.Background()
	ip := "1.2.3.4"

	banned, failures, err := svc.RecordFailure(ctx, ip)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if banned || failures != 0 {
		t.Fatalf("expected no-op when disabled, got banned=%v failures=%d", banned, failures)
	}

	isBanned, _, err := svc.IsBanned(ctx, ip)
	if err != nil {
		t.Fatalf("IsBanned: unexpected error: %v", err)
	}
	if isBanned {
		t.Fatalf("expected IP not banned when feature disabled")
	}
}

func TestLoginBruteforceService_DefaultsWhenSettingsMissing(t *testing.T) {
	// No settings configured at all: default-true enable + default thresholds should apply.
	svc, _ := newLoginBruteforceTestService(t, nil)
	ctx := context.Background()
	ip := "5.6.7.8"

	for i := 1; i < loginBruteforceDefaultMaxFailures; i++ {
		banned, _, err := svc.RecordFailure(ctx, ip)
		if err != nil {
			t.Fatalf("RecordFailure #%d: unexpected error: %v", i, err)
		}
		if banned {
			t.Fatalf("RecordFailure #%d: banned before reaching default threshold %d", i, loginBruteforceDefaultMaxFailures)
		}
	}
	banned, failures, err := svc.RecordFailure(ctx, ip)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !banned || failures != loginBruteforceDefaultMaxFailures {
		t.Fatalf("expected ban at default threshold %d, got banned=%v failures=%d", loginBruteforceDefaultMaxFailures, banned, failures)
	}
}

func TestLoginBruteforceService_WindowExpiryResetsCounter(t *testing.T) {
	svc, mr := newLoginBruteforceTestService(t, map[string]string{
		SettingKeyLoginBruteforceMaxFailures:   "3",
		SettingKeyLoginBruteforceWindowMinutes: "1",
		SettingKeyLoginBruteforceBanMinutes:    "60",
	})
	ctx := context.Background()
	ip := "9.9.9.9"

	for i := 0; i < 2; i++ {
		banned, _, err := svc.RecordFailure(ctx, ip)
		if err != nil || banned {
			t.Fatalf("unexpected state before window expiry: banned=%v err=%v", banned, err)
		}
	}

	// Fast-forward past the 1-minute window; the counter key should expire.
	mr.FastForward(2 * time.Minute)

	banned, failures, err := svc.RecordFailure(ctx, ip)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if banned {
		t.Fatalf("expected ban NOT triggered after window reset (failures=%d)", failures)
	}
	if failures != 1 {
		t.Fatalf("expected counter to reset to 1 after window expiry, got %d", failures)
	}
}

func TestLoginBruteforceService_UnbanRemovesBan(t *testing.T) {
	svc, _ := newLoginBruteforceTestService(t, map[string]string{
		SettingKeyLoginBruteforceMaxFailures: "1",
	})
	ctx := context.Background()
	ip := "8.8.8.8"

	banned, _, err := svc.RecordFailure(ctx, ip)
	if err != nil || !banned {
		t.Fatalf("expected immediate ban with threshold=1, got banned=%v err=%v", banned, err)
	}

	if err := svc.Unban(ctx, ip); err != nil {
		t.Fatalf("Unban: unexpected error: %v", err)
	}

	isBanned, _, err := svc.IsBanned(ctx, ip)
	if err != nil {
		t.Fatalf("IsBanned: unexpected error: %v", err)
	}
	if isBanned {
		t.Fatalf("expected IP to be unbanned")
	}
}

func TestLoginBruteforceService_ListBanned(t *testing.T) {
	svc, _ := newLoginBruteforceTestService(t, map[string]string{
		SettingKeyLoginBruteforceMaxFailures: "1",
	})
	ctx := context.Background()

	if _, _, err := svc.RecordFailure(ctx, "1.1.1.1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, _, err := svc.RecordFailure(ctx, "2.2.2.2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries, err := svc.ListBanned(ctx)
	if err != nil {
		t.Fatalf("ListBanned: unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 banned entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Reason != "login_bruteforce" {
			t.Errorf("expected reason=login_bruteforce, got %q", e.Reason)
		}
		if e.Failures != 1 {
			t.Errorf("expected failures=1, got %d", e.Failures)
		}
		if e.ExpiresInSecond <= 0 {
			t.Errorf("expected positive ExpiresInSecond, got %d", e.ExpiresInSecond)
		}
	}
}

func TestLoginBruteforceService_NilRedisIsNoop(t *testing.T) {
	settingRepo := &loginBruteforceSettingStub{values: map[string]string{}}
	svc := NewLoginBruteforceService(settingRepo, nil, nil)
	ctx := context.Background()

	banned, failures, err := svc.RecordFailure(ctx, "1.2.3.4")
	if err != nil || banned || failures != 0 {
		t.Fatalf("expected no-op with nil redis client, got banned=%v failures=%d err=%v", banned, failures, err)
	}
	isBanned, _, err := svc.IsBanned(ctx, "1.2.3.4")
	if err != nil || isBanned {
		t.Fatalf("expected IsBanned no-op with nil redis client, got banned=%v err=%v", isBanned, err)
	}
}
