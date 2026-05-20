package service

import (
	"testing"
	"time"
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
