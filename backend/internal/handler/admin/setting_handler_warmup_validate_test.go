package admin

import (
	"testing"
)

func TestValidateScheduledWarmupRequest_RejectsEmptyPlatforms(t *testing.T) {
	empty := []string{}
	req := &UpdateSettingsRequest{
		ScheduledWarmupPlatforms: &empty,
	}
	if err := validateScheduledWarmupRequest(req); err == nil {
		t.Error("expected error for empty platforms list, got nil")
	}
}

func TestValidateScheduledWarmupRequest_AcceptsNilPlatforms(t *testing.T) {
	req := &UpdateSettingsRequest{
		ScheduledWarmupPlatforms: nil, // not provided — patch request
	}
	if err := validateScheduledWarmupRequest(req); err != nil {
		t.Errorf("nil platforms (not provided) should be accepted, got: %v", err)
	}
}

func TestValidateScheduledWarmupRequest_AcceptsNonEmptyPlatforms(t *testing.T) {
	platforms := []string{"anthropic", "openai"}
	req := &UpdateSettingsRequest{
		ScheduledWarmupPlatforms: &platforms,
	}
	if err := validateScheduledWarmupRequest(req); err != nil {
		t.Errorf("non-empty platforms should be accepted, got: %v", err)
	}
}

func TestValidateScheduledWarmupRequest_RejectsInvalidCron(t *testing.T) {
	bad := "not a cron"
	req := &UpdateSettingsRequest{
		ScheduledWarmupCron: &bad,
	}
	if err := validateScheduledWarmupRequest(req); err == nil {
		t.Error("expected error for invalid cron, got nil")
	}
}

func TestValidateScheduledWarmupRequest_AcceptsEmptyCron(t *testing.T) {
	empty := ""
	req := &UpdateSettingsRequest{
		ScheduledWarmupCron: &empty,
	}
	if err := validateScheduledWarmupRequest(req); err != nil {
		t.Errorf("empty cron (use default) should be accepted, got: %v", err)
	}
}
