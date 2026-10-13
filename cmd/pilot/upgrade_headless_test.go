package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/dashboard"
	"github.com/qf-studio/pilot/internal/executor"
)

// fakeLiveWorkers is a test double for executor.LiveWorkerChecker whose
// reported IDs can change between polls.
type fakeLiveWorkers struct {
	mu  sync.Mutex
	ids []string
}

func (f *fakeLiveWorkers) GetRunningTaskIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ids...)
}

func TestLiveWorkerTaskChecker_GetRunningTaskIDs(t *testing.T) {
	c := &liveWorkerTaskChecker{workers: &fakeLiveWorkers{ids: []string{"GH-1", "GH-2"}}}
	got := c.GetRunningTaskIDs()
	if len(got) != 2 || got[0] != "GH-1" || got[1] != "GH-2" {
		t.Errorf("GetRunningTaskIDs() = %v, want [GH-1 GH-2]", got)
	}
}

func TestLiveWorkerTaskChecker_WaitForTasks_ReturnsWhenIdle(t *testing.T) {
	c := &liveWorkerTaskChecker{workers: &fakeLiveWorkers{}}
	if err := c.WaitForTasks(context.Background(), time.Second); err != nil {
		t.Errorf("WaitForTasks() on idle workers = %v, want nil", err)
	}
}

func TestLiveWorkerTaskChecker_WaitForTasks_TimeoutIsErrDrainTimeout(t *testing.T) {
	c := &liveWorkerTaskChecker{workers: &fakeLiveWorkers{ids: []string{"GH-9"}}}
	err := c.WaitForTasks(context.Background(), 50*time.Millisecond)
	if err == nil {
		t.Fatal("WaitForTasks() = nil, want drain timeout error")
	}
	if !errors.Is(err, executor.ErrDrainTimeout) {
		t.Errorf("err = %v, want it to wrap executor.ErrDrainTimeout", err)
	}
	// The alert gate must keep classifying it as a drain timeout: the first
	// occurrence is suppressed, the second pages.
	gate := &drainTimeoutAlertGate{}
	if gate.observe(err) {
		t.Error("first drain timeout should not alert")
	}
	if !gate.observe(err) {
		t.Error("second consecutive drain timeout should alert")
	}
}

func TestLiveWorkerTaskChecker_WaitForTasks_ContextCancelled(t *testing.T) {
	c := &liveWorkerTaskChecker{workers: &fakeLiveWorkers{ids: []string{"GH-9"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.WaitForTasks(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestUpgradeNotify_NilProgramLogsOnly(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A nil *tea.Program must not be sent to (would panic) but must log.
	upgradeNotify(nil, dashboard.NotifyUpgradeComplete(true, "")(), "upgrade installed marker")
	upgradeNotifyLog(nil, "plain log marker")

	out := buf.String()
	for _, want := range []string{"upgrade installed marker", "plain log marker"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output %q missing %q", out, want)
		}
	}
}
