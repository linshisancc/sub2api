//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// feishuTestSettingRepo is an in-memory SettingRepository for feishu/ops tests.
type feishuTestSettingRepo struct {
	mu     sync.Mutex
	values map[string]string
}

func newFeishuTestSettingRepo() *feishuTestSettingRepo {
	return &feishuTestSettingRepo{values: map[string]string{}}
}

func (r *feishuTestSettingRepo) Get(ctx context.Context, key string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *feishuTestSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func (r *feishuTestSettingRepo) Set(ctx context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *feishuTestSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (r *feishuTestSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range settings {
		r.values[k] = v
	}
	return nil
}

func (r *feishuTestSettingRepo) GetAll(ctx context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	for k, v := range r.values {
		out[k] = v
	}
	return out, nil
}

func (r *feishuTestSettingRepo) Delete(ctx context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

func TestUpdateEmailNotificationConfigPersistsFeishuEnabled(t *testing.T) {
	repo := newFeishuTestSettingRepo()
	svc := &OpsService{settingRepo: repo}
	ctx := context.Background()

	_, err := svc.UpdateEmailNotificationConfig(ctx, &OpsEmailNotificationConfigUpdateRequest{
		Alert: &OpsEmailAlertConfig{
			Enabled:       true,
			FeishuEnabled: true,
		},
	})
	require.NoError(t, err)

	got, err := svc.GetEmailNotificationConfig(ctx)
	require.NoError(t, err)
	require.True(t, got.Alert.FeishuEnabled, "feishu_enabled must survive a save/load round-trip")
}

func TestMaybeSendAlertFeishu(t *testing.T) {
	rule := &OpsAlertRule{ID: 7, Name: "API 成功率过低", Severity: "P1", MetricType: "error_rate", Operator: ">", Threshold: 20}
	event := &OpsAlertEvent{ID: 99, Status: OpsAlertStatusFiring, FiredAt: time.Now().UTC()}

	setup := func(t *testing.T, feishuEnabled bool, minSeverity string) (*OpsAlertEvaluatorService, *int32) {
		var hits int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		repo := newFeishuTestSettingRepo()
		cfg := &OpsEmailNotificationConfig{}
		cfg.Alert.Enabled = true
		cfg.Alert.FeishuEnabled = feishuEnabled
		cfg.Alert.MinSeverity = minSeverity
		raw, _ := json.Marshal(cfg)
		_ = repo.Set(context.Background(), SettingKeyOpsEmailNotificationConfig, string(raw))
		_ = repo.Set(context.Background(), SettingKeyFeishuWebhookEnabled, "true")
		_ = repo.Set(context.Background(), SettingKeyFeishuWebhookURL, server.URL)

		svc := &OpsAlertEvaluatorService{
			opsService:    &OpsService{settingRepo: repo},
			feishuWebhook: NewFeishuWebhookService(repo, nil),
		}
		return svc, &hits
	}

	t.Run("feishu_enabled=false 不推送", func(t *testing.T) {
		svc, hits := setup(t, false, "")
		svc.maybeSendAlertFeishu(context.Background(), nil, rule, event)
		require.Equal(t, int32(0), atomic.LoadInt32(hits))
	})

	t.Run("高 MinSeverity 不再屏蔽飞书（解耦）", func(t *testing.T) {
		svc, hits := setup(t, true, "critical")
		svc.maybeSendAlertFeishu(context.Background(), nil, rule, event)
		require.Equal(t, int32(1), atomic.LoadInt32(hits), "P1 规则在 MinSeverity=critical 下仍应推送飞书")
	})

	t.Run("命中静默规则不推送", func(t *testing.T) {
		svc, hits := setup(t, true, "")
		runtimeCfg := &OpsAlertRuntimeSettings{
			Silencing: OpsAlertSilencingSettings{
				Enabled:            true,
				GlobalUntilRFC3339: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			},
		}
		svc.maybeSendAlertFeishu(context.Background(), runtimeCfg, rule, event)
		require.Equal(t, int32(0), atomic.LoadInt32(hits))
	})
}

var _ OpsRepository = (*stubOpsRepo)(nil)

type stubOpsRepo struct {
	OpsRepository
	overview *OpsDashboardOverview
	err      error
}

func (s *stubOpsRepo) GetDashboardOverview(ctx context.Context, filter *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.overview != nil {
		return s.overview, nil
	}
	return &OpsDashboardOverview{}, nil
}

func TestComputeGroupAvailableRatio(t *testing.T) {
	t.Parallel()

	t.Run("正常情况: 10个账号, 8个可用 = 80%", func(t *testing.T) {
		t.Parallel()

		got := computeGroupAvailableRatio(&GroupAvailability{
			TotalAccounts:  10,
			AvailableCount: 8,
		})
		require.InDelta(t, 80.0, got, 0.0001)
	})

	t.Run("边界情况: TotalAccounts = 0 应返回 0", func(t *testing.T) {
		t.Parallel()

		got := computeGroupAvailableRatio(&GroupAvailability{
			TotalAccounts:  0,
			AvailableCount: 8,
		})
		require.Equal(t, 0.0, got)
	})

	t.Run("边界情况: AvailableCount = 0 应返回 0%", func(t *testing.T) {
		t.Parallel()

		got := computeGroupAvailableRatio(&GroupAvailability{
			TotalAccounts:  10,
			AvailableCount: 0,
		})
		require.Equal(t, 0.0, got)
	})
}

func TestCountAccountsByCondition(t *testing.T) {
	t.Parallel()

	t.Run("测试限流账号统计: acc.IsRateLimited", func(t *testing.T) {
		t.Parallel()

		accounts := map[int64]*AccountAvailability{
			1: {IsRateLimited: true},
			2: {IsRateLimited: false},
			3: {IsRateLimited: true},
		}

		got := countAccountsByCondition(accounts, func(acc *AccountAvailability) bool {
			return acc.IsRateLimited
		})
		require.Equal(t, int64(2), got)
	})

	t.Run("测试错误账号统计（排除临时不可调度）: acc.HasError && acc.TempUnschedulableUntil == nil", func(t *testing.T) {
		t.Parallel()

		until := time.Now().UTC().Add(5 * time.Minute)
		accounts := map[int64]*AccountAvailability{
			1: {HasError: true},
			2: {HasError: true, TempUnschedulableUntil: &until},
			3: {HasError: false},
		}

		got := countAccountsByCondition(accounts, func(acc *AccountAvailability) bool {
			return acc.HasError && acc.TempUnschedulableUntil == nil
		})
		require.Equal(t, int64(1), got)
	})

	t.Run("边界情况: 空 map 应返回 0", func(t *testing.T) {
		t.Parallel()

		got := countAccountsByCondition(map[int64]*AccountAvailability{}, func(acc *AccountAvailability) bool {
			return acc.IsRateLimited
		})
		require.Equal(t, int64(0), got)
	})
}

func TestComputeRuleMetricNewIndicators(t *testing.T) {
	t.Parallel()

	groupID := int64(101)
	platform := "openai"

	availability := &OpsAccountAvailability{
		Group: &GroupAvailability{
			GroupID:        groupID,
			TotalAccounts:  10,
			AvailableCount: 8,
		},
		Accounts: map[int64]*AccountAvailability{
			1: {IsRateLimited: true},
			2: {IsRateLimited: true},
			3: {HasError: true},
			4: {HasError: true, TempUnschedulableUntil: timePtr(time.Now().UTC().Add(2 * time.Minute))},
			5: {HasError: false, IsRateLimited: false},
		},
	}

	opsService := &OpsService{
		getAccountAvailability: func(_ context.Context, _ string, _ *int64) (*OpsAccountAvailability, error) {
			return availability, nil
		},
	}

	svc := &OpsAlertEvaluatorService{
		opsService: opsService,
		opsRepo:    &stubOpsRepo{overview: &OpsDashboardOverview{}},
	}

	start := time.Now().UTC().Add(-5 * time.Minute)
	end := time.Now().UTC()
	ctx := context.Background()

	tests := []struct {
		name       string
		metricType string
		groupID    *int64
		wantValue  float64
		wantOK     bool
	}{
		{
			name:       "group_available_accounts",
			metricType: "group_available_accounts",
			groupID:    &groupID,
			wantValue:  8,
			wantOK:     true,
		},
		{
			name:       "group_available_ratio",
			metricType: "group_available_ratio",
			groupID:    &groupID,
			wantValue:  80.0,
			wantOK:     true,
		},
		{
			name:       "account_rate_limited_count",
			metricType: "account_rate_limited_count",
			groupID:    nil,
			wantValue:  2,
			wantOK:     true,
		},
		{
			name:       "account_error_count",
			metricType: "account_error_count",
			groupID:    nil,
			wantValue:  1,
			wantOK:     true,
		},
		{
			name:       "group_available_accounts without group_id returns false",
			metricType: "group_available_accounts",
			groupID:    nil,
			wantValue:  0,
			wantOK:     false,
		},
		{
			name:       "group_available_ratio without group_id returns false",
			metricType: "group_available_ratio",
			groupID:    nil,
			wantValue:  0,
			wantOK:     false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rule := &OpsAlertRule{
				MetricType: tt.metricType,
			}
			gotValue, gotOK := svc.computeRuleMetric(ctx, rule, nil, start, end, platform, tt.groupID)
			require.Equal(t, tt.wantOK, gotOK)
			if !tt.wantOK {
				return
			}
			require.InDelta(t, tt.wantValue, gotValue, 0.0001)
		})
	}
}
