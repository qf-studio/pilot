package executor

import (
	"context"
	"testing"
	"time"
)

func TestAgentReadTokenProvider_SetAndGet(t *testing.T) {
	t.Cleanup(func() { SetAgentReadTokenProvider(nil) })

	SetAgentReadTokenProvider(nil)
	if got := GetAgentReadTokenProvider(); got != nil {
		t.Fatal("GetAgentReadTokenProvider() != nil with no provider installed")
	}

	var gotRepo string
	SetAgentReadTokenProvider(func(ctx context.Context, repo string) (string, time.Time, error) {
		gotRepo = repo
		return "test-scoped-read-token", time.Unix(1, 0), nil
	})
	provider := GetAgentReadTokenProvider()
	if provider == nil {
		t.Fatal("GetAgentReadTokenProvider() = nil after SetAgentReadTokenProvider")
	}
	token, expires, err := provider(context.Background(), "owner/repo")
	if err != nil || token != "test-scoped-read-token" || !expires.Equal(time.Unix(1, 0)) {
		t.Fatalf("provider() = %q, %v, %v; want the installed provider's result", token, expires, err)
	}
	if gotRepo != "owner/repo" {
		t.Errorf("provider got repo %q, want %q", gotRepo, "owner/repo")
	}

	SetAgentReadTokenProvider(nil)
	if got := GetAgentReadTokenProvider(); got != nil {
		t.Error("GetAgentReadTokenProvider() != nil after SetAgentReadTokenProvider(nil)")
	}
}
