package autopilot

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// syncBuffer is a bytes.Buffer safe for the deadline watcher goroutine to write
// while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// eventually polls cond for up to limit.
func eventually(limit time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(limit)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestEpicParentPass_BlockedReconcilerDoesNotStallTick is the GH-5657
// acceptance test: a reconciler in the epic-parent pass blocks (ignoring ctx);
// the Run loop must still call processAllPRs, and a second pass must be skipped
// while the first is inflight.
//
// Run inline, the epicParentTicker branch holds the loop and the PR handler is
// never reached. Without the inflight guard the second pass re-enters the
// blocked reconciler.
func TestEpicParentPass_BlockedReconcilerDoesNotStallTick(t *testing.T) {
	const interval = 100 * time.Millisecond

	c := stageDeadlineFixture(t, time.Minute) // pass deadline never fires here
	c.config.CIPollInterval = interval

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseStep := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseStep)

	var stepCalls atomic.Int32
	entered := make(chan struct{}, 8)
	c.epicParentStepsFn = func() []epicParentStep {
		return []epicParentStep{{"fakeBlockingReconciler", func(context.Context) {
			stepCalls.Add(1)
			entered <- struct{}{}
			<-release // deliberately ignores ctx
		}}}
	}

	var prCalls atomic.Int32
	c.processPRFn = func(context.Context, int, *github.PullRequest) error {
		prCalls.Add(1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		releaseStep()
		<-runDone
	})

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("epic-parent pass never started")
	}

	// The loop must still reach processAllPRs within two tick intervals plus
	// scheduling slack while the reconciler is blocked.
	if !eventually(2*interval+2*time.Second, func() bool { return prCalls.Load() >= 1 }) {
		t.Fatal("processAllPRs never ran while an epic-parent reconciler was blocked — the pass is stalling the controller tick")
	}

	// A second pass while the first is inflight must be skipped, not re-enter.
	skipped := make(chan struct{})
	go func() {
		defer close(skipped)
		c.runEpicParentPass(ctx)
	}()
	select {
	case <-skipped:
	case <-time.After(2 * time.Second):
		t.Fatal("second runEpicParentPass blocked behind the inflight pass instead of skipping")
	}
	// Let a further epicParentTicker tick elapse as well.
	time.Sleep(2 * interval)
	if got := stepCalls.Load(); got != 1 {
		t.Fatalf("blocked reconciler entered %d times, want 1 — a second pass ran while the first was inflight", got)
	}

	// Once the reconciler returns the guard clears and the next pass runs.
	releaseStep()
	if !eventually(5*time.Second, func() bool { return stepCalls.Load() >= 2 }) {
		t.Fatal("no new pass started after the inflight pass finished")
	}
}

// TestEpicParentPass_DeadlineLogsAndCounts pins the per-pass deadline: a pass
// that outlives Config.StageTimeout logs one WARN naming the reconciler that was
// running and increments autopilot_epic_parent_pass_timeouts_total once.
func TestEpicParentPass_DeadlineLogsAndCounts(t *testing.T) {
	c := stageDeadlineFixture(t, 100*time.Millisecond)

	logs := &syncBuffer{}
	c.log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var laterRan atomic.Bool
	c.epicParentStepsFn = func() []epicParentStep {
		return []epicParentStep{
			{"fastReconciler", func(context.Context) {}},
			{"slowReconciler", func(ctx context.Context) { <-ctx.Done() }},
			{"neverReachedReconciler", func(context.Context) { laterRan.Store(true) }},
		}
	}

	c.runEpicParentPass(context.Background())

	warned := func() int {
		return strings.Count(logs.String(), "epic-parent pass exceeded deadline")
	}
	if !eventually(2*time.Second, func() bool { return warned() >= 1 }) {
		t.Fatalf("no deadline WARN logged; logs:\n%s", logs.String())
	}
	time.Sleep(50 * time.Millisecond) // a duplicate WARN would land here
	if got := warned(); got != 1 {
		t.Fatalf("deadline WARN logged %d times, want 1; logs:\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "reconciler=slowReconciler") {
		t.Fatalf("WARN does not name the running reconciler; logs:\n%s", logs.String())
	}
	if got := c.Metrics().Snapshot().EpicParentPassTimeouts; got != 1 {
		t.Fatalf("EpicParentPassTimeouts = %d, want 1", got)
	}
	if laterRan.Load() {
		t.Fatal("reconciler after the deadline still ran")
	}
	if !c.Metrics().Snapshot().LastEpicParentPassAt.IsZero() {
		t.Fatal("LastEpicParentPassAt stamped by a pass that timed out")
	}

	// The guard is released: the loop can run the next pass.
	c.epicParentMu.Lock()
	inflight := c.epicParentInflight
	c.epicParentMu.Unlock()
	if inflight {
		t.Fatal("epicParentInflight still set after the pass returned")
	}
}

// TestEpicParentPass_DeadlineCountedWhenReconcilerHonorsCtx pins GH-5784: a
// reconciler that returns the instant its ctx is cancelled must not let the
// pass-exit path beat the deadline watcher — every such pass counts exactly one
// timeout.
func TestEpicParentPass_DeadlineCountedWhenReconcilerHonorsCtx(t *testing.T) {
	const passes = 50
	c := stageDeadlineFixture(t, time.Millisecond)
	c.log = slog.New(slog.NewTextHandler(&syncBuffer{}, nil))
	c.epicParentStepsFn = func() []epicParentStep {
		return []epicParentStep{{"ctxHonoringReconciler", func(ctx context.Context) { <-ctx.Done() }}}
	}

	for i := 0; i < passes; i++ {
		c.runEpicParentPass(context.Background())
	}

	// The watcher goroutine may still be in flight for the last pass.
	if !eventually(2*time.Second, func() bool { return c.Metrics().Snapshot().EpicParentPassTimeouts >= passes }) {
		t.Fatalf("EpicParentPassTimeouts = %d, want %d", c.Metrics().Snapshot().EpicParentPassTimeouts, passes)
	}
	time.Sleep(20 * time.Millisecond) // a duplicate count would land here
	if got := c.Metrics().Snapshot().EpicParentPassTimeouts; got != passes {
		t.Fatalf("EpicParentPassTimeouts = %d, want exactly %d", got, passes)
	}
}

// TestEpicParentPass_CleanPassStampsPassLivenessNotTick pins the metrics_alerter
// scope: a finished pass stamps its own gauge and never RecordTick, so a stuck
// processAllPRs still trips tickStaleness; a clean pass counts no timeout.
func TestEpicParentPass_CleanPassStampsPassLivenessNotTick(t *testing.T) {
	c := stageDeadlineFixture(t, time.Minute)

	var order []string
	c.epicParentStepsFn = func() []epicParentStep {
		return []epicParentStep{
			{"a", func(context.Context) { order = append(order, "a") }},
			{"b", func(context.Context) { order = append(order, "b") }},
		}
	}

	c.runEpicParentPass(context.Background())

	if strings.Join(order, ",") != "a,b" {
		t.Fatalf("reconciler order = %v, want [a b]", order)
	}
	snap := c.Metrics().Snapshot()
	if snap.LastEpicParentPassAt.IsZero() {
		t.Fatal("LastEpicParentPassAt not stamped by a clean pass")
	}
	if !snap.LastTickAt.IsZero() {
		t.Fatal("epic-parent pass stamped LastTickAt — it would mask a stuck processAllPRs")
	}
	if snap.EpicParentPassTimeouts != 0 {
		t.Fatalf("EpicParentPassTimeouts = %d on a clean pass, want 0", snap.EpicParentPassTimeouts)
	}
}
