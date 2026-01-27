package budget

import (
	"context"
	"testing"
	"time"
)

func TestTaskLimiter_TokenLimit(t *testing.T) {
	limiter := NewTaskLimiter(1000, 0) // 1000 token limit, no time limit

	// Add tokens under limit
	if !limiter.AddTokens(500) {
		t.Error("expected success adding tokens under limit")
	}
	if limiter.IsExceeded() {
		t.Error("expected not exceeded after adding 500/1000")
	}

	// Add more tokens, still under
	if !limiter.AddTokens(400) {
		t.Error("expected success adding tokens under limit")
	}
	if limiter.IsExceeded() {
		t.Error("expected not exceeded after adding 900/1000")
	}

	// Exceed limit
	if limiter.AddTokens(200) {
		t.Error("expected failure when exceeding limit")
	}
	if !limiter.IsExceeded() {
		t.Error("expected exceeded after adding 1100/1000")
	}

	if limiter.Reason() == "" {
		t.Error("expected reason to be set")
	}
}

func TestTaskLimiter_NoTokenLimit(t *testing.T) {
	limiter := NewTaskLimiter(0, 0) // No limits

	// Should always succeed
	if !limiter.AddTokens(1000000) {
		t.Error("expected success with no token limit")
	}
	if limiter.IsExceeded() {
		t.Error("expected not exceeded with no limit")
	}
}

func TestTaskLimiter_DurationLimit(t *testing.T) {
	limiter := NewTaskLimiter(0, 100*time.Millisecond) // 100ms limit

	// Should not be exceeded immediately
	if !limiter.CheckDuration() {
		t.Error("expected duration not exceeded immediately")
	}

	// Wait for limit to pass
	time.Sleep(150 * time.Millisecond)

	if limiter.CheckDuration() {
		t.Error("expected duration exceeded after sleep")
	}
	if !limiter.IsExceeded() {
		t.Error("expected exceeded flag set")
	}
}

func TestTaskLimiter_NoDurationLimit(t *testing.T) {
	limiter := NewTaskLimiter(0, 0) // No limits

	time.Sleep(10 * time.Millisecond)

	if !limiter.CheckDuration() {
		t.Error("expected success with no duration limit")
	}
}

func TestTaskLimiter_GetMetrics(t *testing.T) {
	limiter := NewTaskLimiter(1000, time.Hour)

	limiter.AddTokens(250)
	limiter.AddTokens(250)

	if limiter.GetTokens() != 500 {
		t.Errorf("expected 500 tokens, got %d", limiter.GetTokens())
	}

	// Duration should be positive
	if limiter.GetDuration() <= 0 {
		t.Error("expected positive duration")
	}
}

func TestTaskLimiter_CreateContext(t *testing.T) {
	limiter := NewTaskLimiter(0, 100*time.Millisecond)

	ctx, cancel := limiter.CreateContext(context.Background())
	defer cancel()

	// Context should have deadline
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Error("expected context to have deadline")
	}

	if time.Until(deadline) > 100*time.Millisecond {
		t.Error("expected deadline within duration limit")
	}
}

func TestTaskLimiter_CreateContext_NoLimit(t *testing.T) {
	limiter := NewTaskLimiter(0, 0)

	ctx, cancel := limiter.CreateContext(context.Background())
	defer cancel()

	// Context should not have deadline
	_, ok := ctx.Deadline()
	if ok {
		t.Error("expected context without deadline when no duration limit")
	}
}

func TestTaskContext(t *testing.T) {
	limiter := NewTaskLimiter(1000, time.Hour)
	taskCtx := NewTaskContext("team1", "user1", "task1", "project1", limiter, 45.50)

	if taskCtx.TeamID != "team1" {
		t.Errorf("expected team1, got %s", taskCtx.TeamID)
	}
	if taskCtx.UserID != "user1" {
		t.Errorf("expected user1, got %s", taskCtx.UserID)
	}
	if taskCtx.TaskID != "task1" {
		t.Errorf("expected task1, got %s", taskCtx.TaskID)
	}
	if taskCtx.ProjectID != "project1" {
		t.Errorf("expected project1, got %s", taskCtx.ProjectID)
	}
	if taskCtx.BudgetLeft != 45.50 {
		t.Errorf("expected 45.50, got %f", taskCtx.BudgetLeft)
	}
	if taskCtx.Limiter != limiter {
		t.Error("expected same limiter")
	}
}
