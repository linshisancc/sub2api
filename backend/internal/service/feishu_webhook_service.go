package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	httppool "github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/redis/go-redis/v9"
)

const (
	feishuWebhookTimeout         = 10 * time.Second
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
		SettingKeyFeishuWebhookAtAll,
		SettingKeyFeishuWebhookAtUserIDs,
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
	case "account_quota", "account_rate_limited", "account_rate_limit_recovered":
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

	card := buildAlertCard(feishuAlertStyleFor(alertType), title, content, settings)
	if err := s.postCard(webhookURL, card); err != nil {
		slog.Error("feishu_webhook: send failed", "error", err, "alert_type", alertType)
	}
}

// SendAccountRateLimited delivers a Feishu alert when an account enters the rate-limited state.
func (s *FeishuWebhookService) SendAccountRateLimited(ctx context.Context, account *Account, resetAt time.Time) {
	if account == nil {
		return
	}
	title := "账号限流告警"
	content := fmt.Sprintf("账号：%s\n平台：%s\n状态：限流中\n预计恢复时间：%s",
		account.Name, account.Platform, resetAt.Format("2006-01-02 15:04:05"))
	s.Send(ctx, "account_rate_limited", strconv.FormatInt(account.ID, 10), title, content)
}

// SendAccountRateLimitRecovered delivers a Feishu alert when an account exits the rate-limited state.
func (s *FeishuWebhookService) SendAccountRateLimitRecovered(ctx context.Context, account *Account) {
	if account == nil {
		return
	}
	title := "账号限流恢复"
	content := fmt.Sprintf("账号：%s\n平台：%s\n状态：已恢复\n恢复时间：%s",
		account.Name, account.Platform, time.Now().Format("2006-01-02 15:04:05"))
	s.Send(ctx, "account_rate_limit_recovered", strconv.FormatInt(account.ID, 10), title, content)
}

// SendOpsAlert delivers an Ops monitoring alert to the Feishu webhook.
// Unlike Send, it bypasses the per-type toggle and the Redis cooldown — the Ops
// alert module already owns its own cooldown/sustained/silencing throttling.
func (s *FeishuWebhookService) SendOpsAlert(ctx context.Context, rule *OpsAlertRule, event *OpsAlertEvent) {
	if rule == nil || event == nil {
		return
	}
	settings, webhookURL, ok := s.loadOpsWebhookSettings(ctx, rule.ID)
	if !ok {
		return
	}

	value := "-"
	if event.MetricValue != nil {
		value = fmt.Sprintf("%.2f", *event.MetricValue)
	}
	title := fmt.Sprintf("运维告警（%s）", rule.Severity)
	content := fmt.Sprintf("规则：%s\n指标：%s %s %.2f（当前 %s）\n状态：%s\n触发时间：%s\n说明：%s",
		rule.Name, rule.MetricType, rule.Operator, rule.Threshold, value,
		event.Status, event.FiredAt.In(timezone.Location()).Format("2006-01-02 15:04:05"), event.Description)

	style := feishuAlertStyle{emoji: "🚨", template: feishuOpsTemplate(rule.Severity)}
	card := buildAlertCard(style, title, content, settings)
	if err := s.postCard(webhookURL, card); err != nil {
		slog.Error("feishu_webhook: ops alert send failed", "error", err, "rule_id", rule.ID)
	}
}

// SendOpsAlertResolved delivers a recovery notification when an Ops monitoring alert resolves.
func (s *FeishuWebhookService) SendOpsAlertResolved(ctx context.Context, rule *OpsAlertRule, event *OpsAlertEvent, resolvedAt time.Time) {
	if rule == nil || event == nil {
		return
	}
	settings, webhookURL, ok := s.loadOpsWebhookSettings(ctx, rule.ID)
	if !ok {
		return
	}

	title := fmt.Sprintf("运维告警恢复（%s）", rule.Severity)
	content := fmt.Sprintf("规则：%s\n指标：%s %s %.2f\n状态：已恢复\n触发时间：%s\n恢复时间：%s",
		rule.Name, rule.MetricType, rule.Operator, rule.Threshold,
		event.FiredAt.In(timezone.Location()).Format("2006-01-02 15:04:05"), resolvedAt.In(timezone.Location()).Format("2006-01-02 15:04:05"))

	style := feishuAlertStyle{emoji: "✅", template: "green"}
	card := buildAlertCard(style, title, content, settings)
	if err := s.postCard(webhookURL, card); err != nil {
		slog.Error("feishu_webhook: ops alert resolved send failed", "error", err, "rule_id", rule.ID)
	}
}

// loadOpsWebhookSettings loads the settings needed for an Ops Feishu push and reports
// whether the push should proceed (enabled + URL set + notify_ops on).
func (s *FeishuWebhookService) loadOpsWebhookSettings(ctx context.Context, ruleID int64) (map[string]string, string, bool) {
	settings, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyFeishuWebhookEnabled,
		SettingKeyFeishuWebhookURL,
		SettingKeyFeishuWebhookNotifyOps,
		SettingKeyFeishuWebhookAtAll,
		SettingKeyFeishuWebhookAtUserIDs,
	})
	if err != nil {
		slog.Warn("feishu_webhook: failed to load settings for ops alert", "error", err)
		return nil, "", false
	}
	if settings[SettingKeyFeishuWebhookEnabled] != "true" {
		slog.Debug("feishu_webhook: ops alert skipped, global webhook disabled", "rule_id", ruleID)
		return nil, "", false
	}
	webhookURL := settings[SettingKeyFeishuWebhookURL]
	if webhookURL == "" {
		slog.Warn("feishu_webhook: ops alert skipped, webhook URL not configured", "rule_id", ruleID)
		return nil, "", false
	}
	if settings[SettingKeyFeishuWebhookNotifyOps] != "true" {
		slog.Debug("feishu_webhook: ops alert skipped, feishu_webhook_notify_ops disabled", "rule_id", ruleID)
		return nil, "", false
	}
	return settings, webhookURL, true
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

// feishuAlertStyle holds the visual style for a Feishu alert card.
type feishuAlertStyle struct {
	emoji    string
	template string // Feishu card header color template
}

func feishuAlertStyleFor(alertType string) feishuAlertStyle {
	switch alertType {
	case "balance_low":
		return feishuAlertStyle{emoji: "💰", template: "orange"}
	case "account_quota":
		return feishuAlertStyle{emoji: "📊", template: "orange"}
	case "account_rate_limited":
		return feishuAlertStyle{emoji: "⏳", template: "red"}
	case "account_rate_limit_recovered":
		return feishuAlertStyle{emoji: "✅", template: "green"}
	default:
		return feishuAlertStyle{emoji: "🔔", template: "blue"}
	}
}

// feishuOpsTemplate maps an Ops alert severity to a Feishu card header color.
func feishuOpsTemplate(severity string) string {
	switch strings.ToUpper(strings.TrimSpace(severity)) {
	case "P0":
		return "red"
	case "P1":
		return "orange"
	case "P2":
		return "yellow"
	default:
		return "grey"
	}
}

// feishuCardMessage is the Feishu webhook interactive card payload.
type feishuCardMessage struct {
	MsgType string     `json:"msg_type"`
	Card    feishuCard `json:"card"`
}

type feishuCard struct {
	Config   feishuCardConfig `json:"config"`
	Header   feishuCardHeader `json:"header"`
	Elements []any            `json:"elements"`
}

type feishuCardConfig struct {
	WideScreenMode bool `json:"wide_screen_mode"`
}

type feishuCardHeader struct {
	Title    feishuCardText `json:"title"`
	Template string         `json:"template"`
}

type feishuCardText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type feishuCardElement struct {
	Tag  string         `json:"tag"`
	Text *feishuCardText `json:"text,omitempty"`
}

// buildAlertCard constructs an interactive Feishu card with a colored header,
// emoji-prefixed title, the alert body, and an optional @mention block.
func buildAlertCard(style feishuAlertStyle, title, content string, settings map[string]string) feishuCardMessage {
	elements := []any{
		feishuCardElement{
			Tag:  "div",
			Text: &feishuCardText{Tag: "lark_md", Content: feishuRenderBody(content)},
		},
	}

	atAll := settings[SettingKeyFeishuWebhookAtAll] == "true"
	atStr := buildFeishuAtString(parseFeishuUserIDs(settings[SettingKeyFeishuWebhookAtUserIDs]), atAll)
	if atStr != "" {
		elements = append(elements,
			feishuCardElement{Tag: "hr"},
			feishuCardElement{Tag: "div", Text: &feishuCardText{Tag: "lark_md", Content: atStr}},
		)
	}

	return feishuCardMessage{
		MsgType: "interactive",
		Card: feishuCard{
			Config: feishuCardConfig{WideScreenMode: true},
			Header: feishuCardHeader{
				Title:    feishuCardText{Tag: "plain_text", Content: fmt.Sprintf("%s [Sub2API] %s", style.emoji, title)},
				Template: style.template,
			},
			Elements: elements,
		},
	}
}

// feishuRenderBody turns "key：value" lines into lark_md with bolded keys.
func feishuRenderBody(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" {
			continue
		}
		if idx := strings.Index(ln, "："); idx >= 0 {
			out = append(out, fmt.Sprintf("**%s：**%s", ln[:idx], ln[idx+len("："):]))
		} else {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}

// parseFeishuUserIDs splits a raw setting string into Feishu user/open IDs.
func parseFeishuUserIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '\n', '\r', '\t', ' ', ';', '；', '，':
			return true
		}
		return false
	})
}

// buildFeishuAtString builds the lark_md <at> block for the configured mention targets.
func buildFeishuAtString(openIDs []string, atAll bool) string {
	parts := make([]string, 0, len(openIDs)+1)
	if atAll {
		parts = append(parts, "<at id=all></at>")
	}
	for _, id := range openIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			parts = append(parts, fmt.Sprintf("<at id=%s></at>", id))
		}
	}
	return strings.Join(parts, " ")
}

func (s *FeishuWebhookService) postCard(webhookURL string, msg feishuCardMessage) error {
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
