package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	httppool "github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/redis/go-redis/v9"
)

const (
	feishuWebhookTimeout        = 10 * time.Second
	feishuDefaultCooldownMinutes = 30
)

// FeishuWebhookService sends alert messages to a Feishu (Lark) webhook,
// with per-alert-type Redis-based cooldown to prevent spam.
type FeishuWebhookService struct {
	settingRepo SettingRepository
	redisClient *redis.Client
}

// NewFeishuWebhookService creates a new FeishuWebhookService.
func NewFeishuWebhookService(settingRepo SettingRepository, redisClient *redis.Client) *FeishuWebhookService {
	return &FeishuWebhookService{
		settingRepo: settingRepo,
		redisClient: redisClient,
	}
}

// Send delivers a Feishu alert if the service is enabled and the cooldown has not been hit.
// alertType identifies the kind of alert (e.g. "balance_low", "account_quota").
// identifier scopes the cooldown to a specific entity (e.g. user ID, account ID as string).
func (s *FeishuWebhookService) Send(ctx context.Context, alertType, identifier, title, content string) {
	keys := []string{
		SettingKeyFeishuWebhookEnabled,
		SettingKeyFeishuWebhookURL,
		SettingKeyFeishuWebhookCooldownMinutes,
		SettingKeyFeishuWebhookNotifyBalance,
		SettingKeyFeishuWebhookNotifyAccount,
	}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		slog.Warn("feishu_webhook: failed to load settings", "error", err)
		return
	}

	if settings[SettingKeyFeishuWebhookEnabled] != "true" {
		return
	}
	webhookURL := settings[SettingKeyFeishuWebhookURL]
	if webhookURL == "" {
		return
	}

	// Check per-type notification toggle
	switch alertType {
	case "balance_low":
		if settings[SettingKeyFeishuWebhookNotifyBalance] != "true" {
			return
		}
	case "account_quota":
		if settings[SettingKeyFeishuWebhookNotifyAccount] != "true" {
			return
		}
	}

	cooldownMinutes := feishuDefaultCooldownMinutes
	if v := settings[SettingKeyFeishuWebhookCooldownMinutes]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cooldownMinutes = n
		}
	}

	if !s.acquireCooldown(ctx, alertType, identifier, time.Duration(cooldownMinutes)*time.Minute) {
		slog.Debug("feishu_webhook: skipped due to cooldown", "alert_type", alertType, "identifier", identifier)
		return
	}

	if err := s.post(webhookURL, title, content); err != nil {
		slog.Error("feishu_webhook: send failed", "error", err, "alert_type", alertType)
	}
}

// acquireCooldown tries to set a Redis key with TTL. Returns true if the lock was acquired
// (meaning we should send), false if the key already existed (cooldown active).
func (s *FeishuWebhookService) acquireCooldown(ctx context.Context, alertType, identifier string, ttl time.Duration) bool {
	if s.redisClient == nil {
		return true
	}
	key := fmt.Sprintf("feishu:cooldown:%s:%s", alertType, identifier)
	ok, err := s.redisClient.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		slog.Warn("feishu_webhook: redis SetNX failed, allowing send", "error", err)
		return true
	}
	return ok
}

// feishuTextMessage is the Feishu webhook text message payload.
type feishuTextMessage struct {
	MsgType string             `json:"msg_type"`
	Content feishuTextContent  `json:"content"`
}

type feishuTextContent struct {
	Text string `json:"text"`
}

func (s *FeishuWebhookService) post(webhookURL, title, content string) error {
	msg := feishuTextMessage{
		MsgType: "text",
		Content: feishuTextContent{
			Text: fmt.Sprintf("[Sub2API] %s\n%s", title, content),
		},
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	client, err := httppool.GetClient(httppool.Options{Timeout: feishuWebhookTimeout})
	if err != nil {
		return fmt.Errorf("get http client: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("feishu webhook returned status %d", resp.StatusCode)
	}
	return nil
}
