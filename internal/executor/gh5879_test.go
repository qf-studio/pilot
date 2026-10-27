package executor

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestParseRateLimitError_JustPassedResetIsAlreadyReset(t *testing.T) {
	const msg = "You've hit your limit · resets 2pm (UTC)"
	day := func(h, m int) time.Time { return time.Date(2026, 10, 26, h, m, 0, 0, time.UTC) }

	tests := []struct {
		name        string
		now         time.Time
		wantReset   time.Time
		wantAlready bool
	}{
		{"three minutes after reset", day(14, 3), day(14, 0), true},
		{"exactly at the window edge", day(16, 0), day(14, 0), true},
		{"just past the window", day(16, 1), day(14, 0).Add(24 * time.Hour), false},
		{"hours after reset", day(16, 30), day(14, 0).Add(24 * time.Hour), false},
		{"before reset", day(13, 50), day(14, 0), false},
		{"exactly at reset", day(14, 0), day(14, 0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := parseRateLimitErrorAt(msg, tt.now)
			if !ok {
				t.Fatal("parse failed")
			}
			if !info.ResetTime.Equal(tt.wantReset) {
				t.Errorf("ResetTime = %v, want %v", info.ResetTime, tt.wantReset)
			}
			if info.AlreadyReset != tt.wantAlready {
				t.Errorf("AlreadyReset = %v, want %v", info.AlreadyReset, tt.wantAlready)
			}
		})
	}
}

func TestDispatcher_UsageLimitPauseJustPassedResetIsShort(t *testing.T) {
	const proj = "/tmp/pilot-gh5879-short"
	now := time.Date(2026, 10, 26, 14, 3, 0, 0, time.UTC)
	backend := newPerProjectBackend(proj)
	clock := newFakePauseClock(now)
	d, alertSink, cleanup := newUsageLimitDispatcher(t, backend,
		&UsageLimitPauseConfig{FallbackPause: 30 * time.Minute, MaxPause: 6 * time.Hour}, clock)
	defer cleanup()

	exec, err := d.QueueTask(context.Background(), &Task{ID: "GH-5879", Title: "A", ProjectPath: proj})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	waitForExecStatus(t, d.store, exec, "running", 2*time.Second)
	// The reset (14:00) passed three minutes ago.
	backend.finish(proj, "You've hit your limit · resets 2pm (UTC)")
	waitForExecStatus(t, d.store, exec, "rate_limited", 2*time.Second)

	want := now.Add(DefaultSchedulerConfig().RetryBuffer)
	if got := d.AdmissionStatus().UsageLimitResumesAt; !got.Equal(want) {
		t.Fatalf("resumes at %v, want now+RetryBuffer %v", got, want)
	}
	if got := alertSink.pauseEvents(); len(got) != 1 {
		t.Fatalf("got %d pause alerts, want 1", len(got))
	}

	clock.Advance(DefaultSchedulerConfig().RetryBuffer)
	if d.IsAdmissionPaused() {
		t.Fatal("admission still paused after RetryBuffer")
	}
}

func TestScheduler_QueueTaskAlreadyResetRetriesAfterBuffer(t *testing.T) {
	config := &SchedulerConfig{CheckInterval: time.Minute, RetryBuffer: 5 * time.Minute}
	queue := NewTaskQueue()
	s := NewScheduler(config, queue)

	// The reset (14:00) is three minutes behind the parse clock.
	rl, ok := parseRateLimitErrorAt("You've hit your limit · resets 2pm (UTC)",
		time.Date(2026, 10, 26, 14, 3, 0, 0, time.UTC))
	if !ok || !rl.AlreadyReset {
		t.Fatalf("parse = %+v, %v; want AlreadyReset", rl, ok)
	}

	before := time.Now()
	s.QueueTask(&Task{ID: "TASK-5879", Title: "t"}, rl)
	after := time.Now()

	tasks := queue.List()
	if len(tasks) != 1 {
		t.Fatalf("queue length = %d, want 1", len(tasks))
	}
	got := tasks[0].RetryAfter
	if got.Before(before.Add(config.RetryBuffer)) || got.After(after.Add(config.RetryBuffer)) {
		t.Fatalf("RetryAfter = %v, want now+RetryBuffer in [%v, %v]",
			got, before.Add(config.RetryBuffer), after.Add(config.RetryBuffer))
	}
}

// rateLimitErrBackend fails every Execute with a classified rate_limit error.
type rateLimitErrBackend struct {
	mu             sync.Mutex
	calls          int
	gotProjectPath string
}

func (b *rateLimitErrBackend) Name() string      { return "mock-rate-limit-err" }
func (b *rateLimitErrBackend) IsAvailable() bool { return true }
func (b *rateLimitErrBackend) Execute(_ context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.calls++
	b.gotProjectPath = opts.ProjectPath
	b.mu.Unlock()
	return nil, &ClaudeCodeError{Type: ErrorTypeRateLimit, Message: "You've hit your limit · resets 2pm (UTC)"}
}

func TestRunner_RateLimitAlertSuppressedWhenPauseArms(t *testing.T) {
	off := false
	tests := []struct {
		name  string
		pause *UsageLimitPauseConfig
		want  AlertEventType
	}{
		{"pause enabled", nil, AlertEventTypeUsageLimitPause},
		{"pause disabled", &UsageLimitPauseConfig{Enabled: &off}, AlertEventTypeRateLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const proj = "/tmp/pilot-gh5879-alert"
			clock := newFakePauseClock(time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC))
			backend := &rateLimitErrBackend{}
			d, alertSink, cleanup := newUsageLimitDispatcher(t, backend, tt.pause, clock)
			defer cleanup()

			exec, err := d.QueueTask(context.Background(), &Task{ID: "GH-5880", Title: "A", ProjectPath: proj})
			if err != nil {
				t.Fatalf("queue: %v", err)
			}
			waitForExecStatus(t, d.store, exec, "rate_limited", 2*time.Second)

			backend.mu.Lock()
			gotPath := backend.gotProjectPath
			backend.mu.Unlock()
			if gotPath != proj {
				t.Errorf("backend received ProjectPath %q, want %q", gotPath, proj)
			}

			alertSink.mu.Lock()
			defer alertSink.mu.Unlock()
			var got []AlertEventType
			for _, e := range alertSink.events {
				if e.Type == AlertEventTypeRateLimit || e.Type == AlertEventTypeUsageLimitPause {
					got = append(got, e.Type)
				}
			}
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("rate-limit alerts = %v, want exactly [%s]", got, tt.want)
			}
		})
	}
}
