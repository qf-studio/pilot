package azuredevops

import (
	"context"
	"errors"
	"testing"

	"github.com/qf-studio/pilot/internal/health/verify"
)

func TestClient_Verify_ReturnsSentinel(t *testing.T) {
	c := NewClient("test-azuredevops-pat", "test-org", "test-project")
	err := c.Verify(context.Background())
	if !errors.Is(err, verify.ErrProbeNotImplemented) {
		t.Errorf("Verify() = %v, want %v", err, verify.ErrProbeNotImplemented)
	}
}

func TestClient_Name(t *testing.T) {
	c := NewClient("test-azuredevops-pat", "test-org", "test-project")
	if got := c.Name(); got != "azure_devops" {
		t.Errorf("Name() = %q, want %q", got, "azure_devops")
	}
}
