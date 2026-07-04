package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestReadConfirmation_Accepted verifies that "y"/"Y" input confirms the
// upgrade, and anything else (including a bare newline) declines it.
func TestReadConfirmation_Accepted(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase y", "y\n", true},
		{"uppercase Y", "Y\n", true},
		{"lowercase n", "n\n", false},
		{"empty line", "\n", false},
		{"garbage", "sure\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			got, err := readConfirmation(ctx, strings.NewReader(tt.input), time.Second)
			if err != nil {
				t.Fatalf("readConfirmation() error = %v, want nil", err)
			}
			if got != tt.want {
				t.Errorf("readConfirmation() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReadConfirmation_ContextCancelled reproduces GH-3791: SIGTERM during
// the prompt must unblock the read via ctx cancellation instead of hanging
// forever on the blocking stdin read.
func TestReadConfirmation_ContextCancelled(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := readConfirmation(ctx, pr, time.Minute)
	elapsed := time.Since(start)

	if !errors.Is(err, errPromptCancelled) {
		t.Fatalf("readConfirmation() error = %v, want errPromptCancelled", err)
	}
	if elapsed > time.Second {
		t.Fatalf("readConfirmation() took %v to react to cancellation, want < 1s", elapsed)
	}
}

// TestReadConfirmation_Timeout reproduces the "unanswered prompt" acceptance
// criterion: an unanswered prompt must give up after the timeout rather than
// blocking forever.
func TestReadConfirmation_Timeout(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()

	start := time.Now()
	_, err := readConfirmation(context.Background(), pr, 50*time.Millisecond)
	elapsed := time.Since(start)

	if !errors.Is(err, errPromptTimeout) {
		t.Fatalf("readConfirmation() error = %v, want errPromptTimeout", err)
	}
	if elapsed > time.Second {
		t.Fatalf("readConfirmation() took %v to time out, want ~50ms", elapsed)
	}
}
