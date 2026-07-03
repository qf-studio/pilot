package jira

import (
	"context"
	"errors"
	"testing"

	"github.com/qf-studio/pilot/internal/health/verify"
)

func TestClient_Verify_ReturnsSentinel(t *testing.T) {
	c := NewClient("https://example.atlassian.net", "test-user", "test-jira-api-token", PlatformCloud)
	err := c.Verify(context.Background())
	if !errors.Is(err, verify.ErrProbeNotImplemented) {
		t.Errorf("Verify() = %v, want %v", err, verify.ErrProbeNotImplemented)
	}
}

func TestClient_Name(t *testing.T) {
	c := NewClient("https://example.atlassian.net", "test-user", "test-jira-api-token", PlatformCloud)
	if got := c.Name(); got != AdapterName {
		t.Errorf("Name() = %q, want %q", got, AdapterName)
	}
}
