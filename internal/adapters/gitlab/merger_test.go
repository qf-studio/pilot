package gitlab

import (
	"errors"
	"testing"
	"time"
)

func TestMergeWaitResult(t *testing.T) {
	tests := []struct {
		name   string
		result MergeWaitResult
	}{
		{
			name: "merged result",
			result: MergeWaitResult{
				Merged:   true,
				MRNumber: 42,
				MRURL:    "https://gitlab.com/namespace/project/-/merge_requests/42",
				Message:  "MR !42 was merged",
			},
		},
		{
			name: "closed result",
			result: MergeWaitResult{
				Closed:   true,
				MRNumber: 42,
				Message:  "MR !42 was closed without merging",
			},
		},
		{
			name: "has conflicts",
			result: MergeWaitResult{
				HasConflicts: true,
				MRNumber:     42,
				Message:      "MR !42 has merge conflicts",
			},
		},
		{
			name: "timed out",
			result: MergeWaitResult{
				TimedOut: true,
				MRNumber: 42,
				Message:  "Timed out waiting for MR !42 to merge after 1h0m0s",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify the struct can be created and fields accessed
			if tt.result.MRNumber != 42 {
				t.Errorf("MRNumber = %d, want 42", tt.result.MRNumber)
			}
		})
	}
}

func TestDefaultMergeWaiterConfig(t *testing.T) {
	config := DefaultMergeWaiterConfig()

	if config.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want 30s", config.PollInterval)
	}

	if config.Timeout != 1*time.Hour {
		t.Errorf("Timeout = %v, want 1h", config.Timeout)
	}
}

func TestNewMergeWaiter(t *testing.T) {
	client := NewClient("test-token", "namespace/project")

	// With nil config - should use defaults
	waiter := NewMergeWaiter(client, nil)
	if waiter == nil {
		t.Fatal("NewMergeWaiter returned nil")
	}
	if waiter.config.PollInterval != 30*time.Second {
		t.Errorf("default PollInterval = %v, want 30s", waiter.config.PollInterval)
	}

	// With custom config
	customConfig := &MergeWaiterConfig{
		PollInterval: 10 * time.Second,
		Timeout:      30 * time.Minute,
	}
	waiter = NewMergeWaiter(client, customConfig)
	if waiter.config.PollInterval != 10*time.Second {
		t.Errorf("custom PollInterval = %v, want 10s", waiter.config.PollInterval)
	}
	if waiter.config.Timeout != 30*time.Minute {
		t.Errorf("custom Timeout = %v, want 30m", waiter.config.Timeout)
	}
}

func TestMergeWaiterErrors(t *testing.T) {
	// Test error constants exist and are the expected types
	if !errors.Is(ErrMRClosed, ErrMRClosed) {
		t.Error("ErrMRClosed should be equal to itself")
	}
	if !errors.Is(ErrMRConflict, ErrMRConflict) {
		t.Error("ErrMRConflict should be equal to itself")
	}
	if !errors.Is(ErrMergeTimeout, ErrMergeTimeout) {
		t.Error("ErrMergeTimeout should be equal to itself")
	}
	if !errors.Is(ErrPipelineFailed, ErrPipelineFailed) {
		t.Error("ErrPipelineFailed should be equal to itself")
	}

	// Verify they have meaningful error messages
	if ErrMRClosed.Error() == "" {
		t.Error("ErrMRClosed should have an error message")
	}
	if ErrMRConflict.Error() == "" {
		t.Error("ErrMRConflict should have an error message")
	}
	if ErrMergeTimeout.Error() == "" {
		t.Error("ErrMergeTimeout should have an error message")
	}
	if ErrPipelineFailed.Error() == "" {
		t.Error("ErrPipelineFailed should have an error message")
	}
}
