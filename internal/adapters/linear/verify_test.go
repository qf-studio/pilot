package linear

import (
	"context"
	"errors"
	"testing"

	"github.com/qf-studio/pilot/internal/health/verify"
)

func TestClient_Verify_ReturnsSentinel(t *testing.T) {
	c := NewClient("test-linear-api-key")
	err := c.Verify(context.Background())
	if !errors.Is(err, verify.ErrProbeNotImplemented) {
		t.Errorf("Verify() = %v, want %v", err, verify.ErrProbeNotImplemented)
	}
}

func TestClient_Name(t *testing.T) {
	c := NewClient("test-linear-api-key")
	if got := c.Name(); got != "linear" {
		t.Errorf("Name() = %q, want %q", got, "linear")
	}
}
