package sdkshim

import (
	"errors"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/studio-sdk/sdk/core"
)

func TestResolveRepoForEvent_NilConfig(t *testing.T) {
	_, _, _, err := ResolveRepoForEvent(nil, "plane", core.IssueEvent{})
	if err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
}

func TestResolveRepoForEvent_Stub(t *testing.T) {
	// Phase 0: every source returns ErrRepoNotResolved. Phase 1+ replaces
	// this test with a per-source table.
	cfg := &config.Config{}
	_, _, _, err := ResolveRepoForEvent(cfg, "plane", core.IssueEvent{ProjectID: "abc-123"})
	if !errors.Is(err, ErrRepoNotResolved) {
		t.Fatalf("expected ErrRepoNotResolved, got %v", err)
	}
}
