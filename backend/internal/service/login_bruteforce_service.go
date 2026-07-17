package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	loginBruteforceDefaultMaxFailures   = 10
	loginBruteforceDefaultWindowMinutes = 5
	loginBruteforceDefaultBanMinutes    = 60

	loginBruteforceFailKeyPrefix = "login_bruteforce:fail:"
	loginBruteforceBanKeyPrefix  = "banned_ip:"

	loginBruteforceAlertType = "login_bruteforce_autoban"
)

// loginBruteforceIncrScript atomically increments the failure counter and
// (re)applies the window TTL on the first hit, or repairs a key left without
// a TTL. Mirrors the pattern in internal/middleware/rate_limiter.go.
var loginBruteforceIncrScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
if current == 1 or ttl == -1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return current
`)

// LoginBruteforceService tracks per-IP login failures and auto-bans IPs that
// exceed a configurable failure threshold within a time window, notifying via
// the Feishu webhook service. Banning is independent of the Feishu master
// switch (feishu_webhook_enabled) — an IP still gets banned even if Feishu is
// unconfigured, only the notification is skipped in that case.
type LoginBruteforceService struct {
	settingRepo   SettingRepository
	redisClient   *redis.Client
	feishuWebhook *FeishuWebhookService
}

// NewLoginBruteforceService creates a new LoginBruteforceService.
func NewLoginBruteforceService(settingRepo SettingRepository, redisClient *redis.Client, feishuWebhook *FeishuWebhookService) *LoginBruteforceService {
	return &LoginBruteforceService{
		settingRepo:   settingRepo,
		redisClient:   redisClient,
		feishuWebhook: feishuWebhook,
	}
}

// BanInfo is the JSON payload stored against a banned IP's Redis key.
type BanInfo struct {
	Reason   string    `json:"reason"`
	Failures int       `json:"failures"`
	BannedAt time.Time `json:"banned_at"`
}

// BannedIPEntry describes a currently-banned IP for the admin listing endpoint.
type BannedIPEntry struct {
	IP              string    `json:"ip"`
	Reason          string    `json:"reason"`
	Failures        int       `json:"failures"`
	BannedAt        time.Time `json:"banned_at"`
	ExpiresInSecond int64     `json:"expires_in_seconds"`
}

func (s *LoginBruteforceService) loadSettings(ctx context.Context) (enabled bool, maxFailures int, window, banDuration time.Duration) {
	keys := []string{
		SettingKeyFeishuLoginBruteforceAutobanEnabled,
		SettingKeyLoginBruteforceMaxFailures,
		SettingKeyLoginBruteforceWindowMinutes,
		SettingKeyLoginBruteforceBanMinutes,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		slog.Warn("login_bruteforce: failed to load settings, using defaults", "error", err)
		settings = map[string]string{}
	}

	enabled = settings[SettingKeyFeishuLoginBruteforceAutobanEnabled] != "false"

	maxFailures = loginBruteforceDefaultMaxFailures
	if v, err := strconv.Atoi(settings[SettingKeyLoginBruteforceMaxFailures]); err == nil && v > 0 {
		maxFailures = v
	}

	windowMinutes := loginBruteforceDefaultWindowMinutes
	if v, err := strconv.Atoi(settings[SettingKeyLoginBruteforceWindowMinutes]); err == nil && v > 0 {
		windowMinutes = v
	}

	banMinutes := loginBruteforceDefaultBanMinutes
	if v, err := strconv.Atoi(settings[SettingKeyLoginBruteforceBanMinutes]); err == nil && v > 0 {
		banMinutes = v
	}

	return enabled, maxFailures, time.Duration(windowMinutes) * time.Minute, time.Duration(banMinutes) * time.Minute
}

// RecordFailure increments the failure counter for ip within the configured
// window. Once the counter reaches the configured threshold, it bans the IP
// and fires a best-effort Feishu alert, returning banned=true.
func (s *LoginBruteforceService) RecordFailure(ctx context.Context, ip string) (banned bool, failures int, err error) {
	if s.redisClient == nil || ip == "" {
		return false, 0, nil
	}

	enabled, maxFailures, window, banDuration := s.loadSettings(ctx)
	if !enabled {
		return false, 0, nil
	}

	key := loginBruteforceFailKeyPrefix + ip
	count, err := loginBruteforceIncrScript.Run(ctx, s.redisClient, []string{key}, window.Milliseconds()).Int64()
	if err != nil {
		slog.Warn("login_bruteforce: failure counter incr failed", "error", err, "ip", ip)
		return false, 0, err
	}
	failures = int(count)
	if failures < maxFailures {
		return false, failures, nil
	}

	if err := s.ban(ctx, ip, banDuration, failures); err != nil {
		slog.Error("login_bruteforce: ban failed", "error", err, "ip", ip)
		return false, failures, err
	}
	// Reset the counter so a subsequent burst after the ban expires starts fresh.
	s.redisClient.Del(ctx, key)

	s.notifyBan(ip, failures, maxFailures, window, banDuration)
	return true, failures, nil
}

func (s *LoginBruteforceService) ban(ctx context.Context, ip string, duration time.Duration, failures int) error {
	info := BanInfo{Reason: "login_bruteforce", Failures: failures, BannedAt: time.Now()}
	payload, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return s.redisClient.Set(ctx, loginBruteforceBanKeyPrefix+ip, payload, duration).Err()
}

// IsBanned reports whether ip is currently banned and, if so, the remaining TTL.
func (s *LoginBruteforceService) IsBanned(ctx context.Context, ip string) (bool, time.Duration, error) {
	if s.redisClient == nil || ip == "" {
		return false, 0, nil
	}
	ttl, err := s.redisClient.TTL(ctx, loginBruteforceBanKeyPrefix+ip).Result()
	if err != nil {
		return false, 0, err
	}
	if ttl <= 0 {
		return false, 0, nil
	}
	return true, ttl, nil
}

// Unban removes an IP's ban immediately.
func (s *LoginBruteforceService) Unban(ctx context.Context, ip string) error {
	if s.redisClient == nil || ip == "" {
		return nil
	}
	return s.redisClient.Del(ctx, loginBruteforceBanKeyPrefix+ip).Err()
}

// ListBanned returns all currently-banned IPs with their remaining TTL and ban reason.
func (s *LoginBruteforceService) ListBanned(ctx context.Context) ([]BannedIPEntry, error) {
	if s.redisClient == nil {
		return nil, nil
	}

	var entries []BannedIPEntry
	iter := s.redisClient.Scan(ctx, 0, loginBruteforceBanKeyPrefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		ip := strings.TrimPrefix(key, loginBruteforceBanKeyPrefix)

		val, err := s.redisClient.Get(ctx, key).Result()
		if err != nil {
			continue
		}
		ttl, err := s.redisClient.TTL(ctx, key).Result()
		if err != nil || ttl <= 0 {
			continue
		}

		entry := BannedIPEntry{IP: ip, ExpiresInSecond: int64(ttl.Seconds())}
		var info BanInfo
		if json.Unmarshal([]byte(val), &info) == nil {
			entry.Reason = info.Reason
			entry.Failures = info.Failures
			entry.BannedAt = info.BannedAt
		}
		entries = append(entries, entry)
	}
	if err := iter.Err(); err != nil {
		return entries, err
	}
	return entries, nil
}

// notifyBan sends a best-effort, async Feishu alert about the auto-ban.
// Mirrors RateLimitService.NotifyAccountRateLimited's async+recover pattern
// so a slow/failing webhook never blocks the login response path.
func (s *LoginBruteforceService) notifyBan(ip string, failures, maxFailures int, window, banDuration time.Duration) {
	if s.feishuWebhook == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in login bruteforce notification", "recover", r)
			}
		}()
		title := "登录爆破自动封禁"
		content := fmt.Sprintf("IP：%s\n接口：POST /api/v1/auth/login\n窗口内失败次数：%d（阈值 %d）\n统计窗口：%d 分钟\n封禁时长：%d 分钟\n触发时间：%s",
			ip, failures, maxFailures, int(window.Minutes()), int(banDuration.Minutes()), time.Now().Format("2006-01-02 15:04:05"))
		s.feishuWebhook.Send(context.Background(), loginBruteforceAlertType, ip, title, content)
	}()
}
