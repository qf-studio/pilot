package github

import (
	"testing"
	"time"
)

func TestDefaultApprovalConfig(t *testing.T) {
	cfg := DefaultApprovalConfig()
	if cfg.Enabled {
		t.Error("expected Enabled=false")
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("expected PollInterval=30s, got %v", cfg.PollInterval)
	}
}
