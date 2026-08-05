package autopilot

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestHydrateFromStore_PerLabelValues seeds a store with executions across two
// models and asserts HydrateFromStore restores per-(model,direction) token
// counters, per-model cost counters, per-(model,result) execution counters,
// and the derived success rate — matching the store's lifetime totals
// (GH-4041 acceptance criteria).
func TestHydrateFromStore_PerLabelValues(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execs := []*memory.Execution{
		{
			ID: "h-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed",
			ModelName:   "claude-sonnet-4-5",
			TokensInput: 1000, TokensOutput: 500, TokensTotal: 1500,
			EstimatedCostUSD: 0.05,
		},
		{
			ID: "h-2", TaskID: "TASK-2", ProjectPath: "/p", Status: "completed",
			ModelName:   "claude-sonnet-4-5",
			TokensInput: 2000, TokensOutput: 1000, TokensTotal: 3000,
			EstimatedCostUSD: 0.10,
		},
		{
			ID: "h-3", TaskID: "TASK-3", ProjectPath: "/p", Status: "failed",
			ModelName:   "claude-opus-4-6",
			TokensInput: 500, TokensOutput: 250, TokensTotal: 750,
			EstimatedCostUSD: 0.02,
		},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}

	snap := metrics.Snapshot()

	// Per-label token totals restored individually.
	wantTokens := map[tokenKey]int64{
		{Model: "claude-sonnet-4-5", Direction: "input"}:  3000,
		{Model: "claude-sonnet-4-5", Direction: "output"}: 1500,
		{Model: "claude-opus-4-6", Direction: "input"}:    500,
		{Model: "claude-opus-4-6", Direction: "output"}:   250,
	}
	for k, want := range wantTokens {
		if got := snap.TokensConsumed[k]; got != want {
			t.Errorf("TokensConsumed[%+v] = %d, want %d", k, got, want)
		}
	}
	var tokenSum int64
	for _, v := range snap.TokensConsumed {
		tokenSum += v
	}
	if want := int64(5250); tokenSum != want {
		t.Errorf("sum(TokensConsumed) = %d, want %d", tokenSum, want)
	}

	// Per-model cost restored.
	const epsilon = 0.0001
	if got, want := snap.ExecutionCostUSD["claude-sonnet-4-5"], 0.15; got < want-epsilon || got > want+epsilon {
		t.Errorf("ExecutionCostUSD[claude-sonnet-4-5] = %.4f, want %.4f", got, want)
	}
	if got, want := snap.ExecutionCostUSD["claude-opus-4-6"], 0.02; got < want-epsilon || got > want+epsilon {
		t.Errorf("ExecutionCostUSD[claude-opus-4-6] = %.4f, want %.4f", got, want)
	}

	// Per-(model,result) execution counts restored; sum matches total executions.
	wantExecs := map[execKey]int64{
		{Model: "claude-sonnet-4-5", Result: "success"}: 2,
		{Model: "claude-opus-4-6", Result: "failed"}:    1,
	}
	for k, want := range wantExecs {
		if got := snap.ExecutionsByResult[k]; got != want {
			t.Errorf("ExecutionsByResult[%+v] = %d, want %d", k, got, want)
		}
	}
	var execSum int64
	for _, v := range snap.ExecutionsByResult {
		execSum += v
	}
	if execSum != int64(len(execs)) {
		t.Errorf("sum(ExecutionsByResult) = %d, want %d", execSum, len(execs))
	}

	// pilot_success_rate inherits the fix for free via IssuesProcessed hydration:
	// 2 of the 3 lifetime executions succeeded.
	wantRate := 2.0 / 3.0
	if snap.SuccessRate < wantRate-epsilon || snap.SuccessRate > wantRate+epsilon {
		t.Errorf("SuccessRate = %.4f, want %.4f", snap.SuccessRate, wantRate)
	}
}

// TestHydrateFromStore_NonFailuresExcludedFromFailed pins TASK-392: declined/
// no_op/stalled/rate_limited/infra/skipped outcomes must not be folded into
// the hydrated "failed" bucket, mirroring the dispatcher's live taxonomy
// (TASK-358) where those statuses are distinct from genuine failures.
func TestHydrateFromStore_NonFailuresExcludedFromFailed(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	statuses := []struct {
		id, status string
	}{
		{"nf-1", "completed"},
		{"nf-2", "failed"},
		{"nf-3", "declined"},
		{"nf-4", "no_op"},
		{"nf-5", "stalled"},
		{"nf-6", "rate_limited"},
		{"nf-7", "infra"},
		{"nf-8", "skipped"},
	}
	for _, s := range statuses {
		if err := store.SaveExecution(&memory.Execution{
			ID: s.id, TaskID: "TASK-" + s.id, ProjectPath: "/p", Status: s.status,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", s.id, err)
		}
	}

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	snap := metrics.Snapshot()

	// Only the single genuine "failed" row counts as failed — the five
	// non-failure statuses (declined/no_op/stalled/infra/skipped) must not
	// be folded in, and rate_limited hydrates into its own key.
	if got := snap.IssuesProcessed["failed"]; got != 1 {
		t.Errorf("IssuesProcessed[failed] = %d, want 1 (non-failures must not collapse in)", got)
	}
	if got := snap.IssuesProcessed["success"]; got != 1 {
		t.Errorf("IssuesProcessed[success] = %d, want 1", got)
	}
	if got := snap.IssuesProcessed["rate_limited"]; got != 1 {
		t.Errorf("IssuesProcessed[rate_limited] = %d, want 1", got)
	}
}

// TestHydrateFromStore_PerModelTaxonomyAndEmptyModelExcluded pins GH-4483:
// the per-model execution baseline (pilot_executions_total{model,result})
// must preserve the full status taxonomy instead of collapsing every
// non-completed/non-stalled status into "failed", and rows with an empty
// model_name must be absent from the series entirely rather than surfacing
// as model="unknown".
func TestHydrateFromStore_PerModelTaxonomyAndEmptyModelExcluded(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execs := []*memory.Execution{
		{ID: "pm-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "declined", ModelName: "claude-sonnet-5"},
		{ID: "pm-2", TaskID: "TASK-2", ProjectPath: "/p", Status: "no_op", ModelName: "claude-sonnet-5"},
		{ID: "pm-3", TaskID: "TASK-3", ProjectPath: "/p", Status: "infra", ModelName: "claude-sonnet-5"},
		{ID: "pm-4", TaskID: "TASK-4", ProjectPath: "/p", Status: "skipped", ModelName: "claude-sonnet-5"},
		{ID: "pm-5", TaskID: "TASK-5", ProjectPath: "/p", Status: "failed", ModelName: "claude-sonnet-5"},
		// Empty model_name, various non-failure statuses: must not appear
		// under model="unknown" (or any other model label) at all.
		{ID: "pm-6", TaskID: "TASK-6", ProjectPath: "/p", Status: "no_op", ModelName: ""},
		{ID: "pm-7", TaskID: "TASK-7", ProjectPath: "/p", Status: "declined", ModelName: ""},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	snap := metrics.Snapshot()

	// Each non-failure status on the named model gets its own result label —
	// none of them collapse into "failed".
	wantExecs := map[execKey]int64{
		{Model: "claude-sonnet-5", Result: "declined"}: 1,
		{Model: "claude-sonnet-5", Result: "no_op"}:    1,
		{Model: "claude-sonnet-5", Result: "infra"}:    1,
		{Model: "claude-sonnet-5", Result: "skipped"}:  1,
		{Model: "claude-sonnet-5", Result: "failed"}:   1, // pm-5 only
	}
	for k, want := range wantExecs {
		if got := snap.ExecutionsByResult[k]; got != want {
			t.Errorf("ExecutionsByResult[%+v] = %d, want %d", k, got, want)
		}
	}

	// The genuine "failed" bucket for this model must be exactly 1 (pm-5) —
	// the non-failure statuses above must not have inflated it.
	if got := snap.ExecutionsByResult[execKey{Model: "claude-sonnet-5", Result: "failed"}]; got != 1 {
		t.Errorf("ExecutionsByResult[claude-sonnet-5,failed] = %d, want 1 (non-failures must not collapse in)", got)
	}

	// No key for any empty/unknown model label — pm-6 and pm-7 must be
	// absent from the series entirely, not surfaced as model="unknown".
	for k := range snap.ExecutionsByResult {
		if k.Model == "" || k.Model == "unknown" {
			t.Errorf("empty/unknown model row leaked into ExecutionsByResult: %+v", k)
		}
	}

	var execSum int64
	for _, v := range snap.ExecutionsByResult {
		execSum += v
	}
	if execSum != 5 {
		t.Errorf("sum(ExecutionsByResult) = %d, want 5 (pm-1..pm-5 only; pm-6/pm-7 excluded)", execSum)
	}
}

// TestHydrateFromStore_IssueLevelCountsByModel pins GH-4483: a task retried
// twice on the same model before shipping (2 failed rows + 1 completed row,
// same task_id, same model) hydrates to 100% issue-level success for that
// model, distinct from the attempt-level ExecutionsByResult view.
func TestHydrateFromStore_IssueLevelCountsByModel(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execs := []*memory.Execution{
		{ID: "ilm-1", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "failed", ModelName: "claude-sonnet-5"},
		{ID: "ilm-2", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "failed", ModelName: "claude-sonnet-5"},
		{ID: "ilm-3", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "completed", ModelName: "claude-sonnet-5"},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	snap := metrics.Snapshot()

	if got := snap.IssuesAttemptedByModel["claude-sonnet-5"]; got != 1 {
		t.Errorf("IssuesAttemptedByModel[claude-sonnet-5] = %d, want 1 (deduped by task_id)", got)
	}
	if got := snap.IssuesShippedByModel["claude-sonnet-5"]; got != 1 {
		t.Errorf("IssuesShippedByModel[claude-sonnet-5] = %d, want 1", got)
	}

	// Attempt-level semantics for this model are unchanged: all 3 rows still
	// count individually, 2 of them "failed".
	if got := snap.ExecutionsByResult[execKey{Model: "claude-sonnet-5", Result: "failed"}]; got != 2 {
		t.Errorf("ExecutionsByResult[claude-sonnet-5,failed] = %d, want 2", got)
	}
	if got := snap.ExecutionsByResult[execKey{Model: "claude-sonnet-5", Result: "success"}]; got != 1 {
		t.Errorf("ExecutionsByResult[claude-sonnet-5,success] = %d, want 1", got)
	}
}

// TestHydrateFromStore_IssueLevelCounts pins TASK-392: a task retried twice
// before shipping (2 failed rows + 1 completed row, same task_id) hydrates to
// issue-level success 100%, distinct from the per-attempt view.
func TestHydrateFromStore_IssueLevelCounts(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execs := []*memory.Execution{
		{ID: "il-1", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "failed"},
		{ID: "il-2", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "failed"},
		{ID: "il-3", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "completed"},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	snap := metrics.Snapshot()

	if snap.IssuesAttempted != 1 {
		t.Errorf("IssuesAttempted = %d, want 1 (deduped by task_id)", snap.IssuesAttempted)
	}
	if snap.IssuesShipped != 1 {
		t.Errorf("IssuesShipped = %d, want 1", snap.IssuesShipped)
	}
	if snap.IssueLevelSuccessRate != 1.0 {
		t.Errorf("IssueLevelSuccessRate = %f, want 1.0", snap.IssueLevelSuccessRate)
	}

	// Per-attempt semantics are unchanged by the issue-level fix: rate_limited
	// is still excluded from the denominator, and every attempt still counts.
	wantAttemptRate := 1.0 / 3.0
	if snap.SuccessRate != wantAttemptRate {
		t.Errorf("SuccessRate = %f, want %f", snap.SuccessRate, wantAttemptRate)
	}
}

// TestHydrateFromStore_PRFamilyCounters pins GH-4121: pilot_prs_merged_lifetime
// and pilot_prs_failed_lifetime hydrate all-time from the executions table (not
// the execution_events ledger, which only goes back to its TASK-379/GH-3844
// introduction and undercounts against every other lifetime counter on this
// Metrics). A bare executor-level task failure with no PR ever created must
// not inflate PRsFailedLifetime, and pre-ledger executions (no execution_events
// rows at all) must still contribute — the entire point of the fix.
//
// GH-4511: PRsMerged/PRsFailed are now pure session counters that must start
// at 0 regardless of what's hydrated — the lifetime baseline lands only on
// PRsMergedLifetime/PRsFailedLifetime (gauges, immune to the Prometheus
// counter-reset misinterpretation that hydrating a live Counter caused).
func TestHydrateFromStore_PRFamilyCounters(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execs := []*memory.Execution{
		// Pre-ledger merges: completed with a PR URL, no execution_events rows
		// at all — exactly the population the ledger-only hydration missed.
		{ID: "pr-1", TaskID: "TASK-PR-1", ProjectPath: "/p", Status: "completed", PRUrl: "https://github.com/o/r/pull/1"},
		{ID: "pr-2", TaskID: "TASK-PR-2", ProjectPath: "/p", Status: "completed", PRUrl: "https://github.com/o/r/pull/2"},
		// Genuine PR-family failure: a PR was created but failed CI/merge.
		{ID: "pr-3", TaskID: "TASK-PR-3", ProjectPath: "/p", Status: "failed", PRUrl: "https://github.com/o/r/pull/3"},
		// Executor-level failure: no PR was ever created for this attempt.
		{ID: "pr-4", TaskID: "TASK-PR-4", ProjectPath: "/p", Status: "failed"},
		// Retried task: failed once with a PR, then shipped on retry — must
		// count once as merged and NOT also inflate PRsFailed.
		{ID: "pr-5a", TaskID: "TASK-PR-5", ProjectPath: "/p", Status: "failed", PRUrl: "https://github.com/o/r/pull/5a"},
		{ID: "pr-5b", TaskID: "TASK-PR-5", ProjectPath: "/p", Status: "completed", PRUrl: "https://github.com/o/r/pull/5b"},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	snap := metrics.Snapshot()

	// GH-4511: hydration must never touch the session counters — they start
	// at 0 every boot regardless of the store's lifetime baseline.
	if snap.PRsMerged != 0 {
		t.Errorf("PRsMerged = %d, want 0 (session counter must not be hydrated)", snap.PRsMerged)
	}
	if snap.PRsFailed != 0 {
		t.Errorf("PRsFailed = %d, want 0 (session counter must not be hydrated)", snap.PRsFailed)
	}

	if snap.PRsMergedLifetime != 3 {
		t.Errorf("PRsMergedLifetime = %d, want 3 (pr-1, pr-2, pr-5 deduped to its completed attempt)", snap.PRsMergedLifetime)
	}
	if snap.PRsFailedLifetime != 1 {
		t.Errorf("PRsFailedLifetime = %d, want 1 (pr-3 only; pr-4 has no PR, pr-5 shipped on retry)", snap.PRsFailedLifetime)
	}
	if snap.PRsMergedLifetime > snap.IssuesShipped {
		t.Errorf("PRsMergedLifetime = %d must not exceed IssuesShipped = %d", snap.PRsMergedLifetime, snap.IssuesShipped)
	}

	// Acceptance: live merges on top of the hydrated baseline must not
	// double count on the lifetime gauge, and the session counter tracks
	// only the live call.
	metrics.RecordPRMerged()
	got := metrics.Snapshot()
	if got.PRsMerged != 1 {
		t.Errorf("PRsMerged after live merge = %d, want 1 (session-only, no hydrated baseline)", got.PRsMerged)
	}
	if got.PRsMergedLifetime != 4 {
		t.Errorf("PRsMergedLifetime after live merge = %d, want 4 (hydrated 3 + live 1)", got.PRsMergedLifetime)
	}
}

// TestHydrateFromStore_PRsMergedRestartResetSemantics pins GH-4511's AC1: a
// restart must never make the Prometheus-visible pilot_prs_merged_total
// series drop to some intermediate value below its pre-restart high-water
// mark — it must either keep climbing or cleanly reset to 0. A drop to a
// nonzero value below the pre-restart value is exactly what previously
// caused Prometheus's increase()/rate() to misfire: seeing a value that
// looks like a reset (lower than before) but isn't 0 makes those functions
// add the ENTIRE pre-reset value back in as fabricated "new" activity
// (observed live: sum(increase(...[3h])) = 1236 against a true 3h count of
// 3). Since PRsMerged is now a pure session counter (never hydrated) it
// always resets cleanly to 0 on restart, which is the one shape
// increase()/rate() are designed to compensate for correctly. The
// all-time truth instead lives on PRsMergedLifetime, a gauge, which is
// immune to reset misinterpretation entirely and must never regress across
// a restart when the store itself is monotonic.
func TestHydrateFromStore_PRsMergedRestartResetSemantics(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Two merges shipped before this process ever started.
	for _, id := range []string{"boot-1", "boot-2"} {
		if err := store.SaveExecution(&memory.Execution{
			ID: id, TaskID: "TASK-" + id, ProjectPath: "/p", Status: "completed",
			PRUrl: "https://github.com/o/r/pull/" + id,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", id, err)
		}
	}

	// --- Session 1 (pre-restart) ---
	session1 := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, session1); err != nil {
		t.Fatalf("HydrateFromStore (session 1): %v", err)
	}

	// One genuine live merge happens during session 1. In production this
	// pairs RecordPRMerged with a store write (recordMergeSuccess +
	// self-heal/MarkExecutionCompleted); mirror that here so the store stays
	// the source of truth for the next boot's hydration.
	session1.RecordPRMerged()
	if err := store.SaveExecution(&memory.Execution{
		ID: "live-1", TaskID: "TASK-live-1", ProjectPath: "/p", Status: "completed",
		PRUrl: "https://github.com/o/r/pull/live-1",
	}); err != nil {
		t.Fatalf("SaveExecution live-1: %v", err)
	}

	preRestart := session1.Snapshot()
	if preRestart.PRsMerged != 1 {
		t.Fatalf("pre-restart PRsMerged = %d, want 1", preRestart.PRsMerged)
	}
	if preRestart.PRsMergedLifetime != 3 {
		t.Fatalf("pre-restart PRsMergedLifetime = %d, want 3", preRestart.PRsMergedLifetime)
	}

	// --- Restart: fresh Metrics, re-hydrated from the same store ---
	session2 := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, session2); err != nil {
		t.Fatalf("HydrateFromStore (session 2): %v", err)
	}
	postRestart := session2.Snapshot()

	// The core AC1 assertion: the session counter resets cleanly to 0, NOT
	// to some value between 0 and the pre-restart high-water mark (1). A
	// nonzero-but-lower value is what fabricates the counter-reset spike;
	// 0 is the one shape increase()/rate() handle correctly.
	if postRestart.PRsMerged != 0 {
		t.Errorf("post-restart PRsMerged = %d, want 0 (session counter must never be hydrated)", postRestart.PRsMerged)
	}

	// The lifetime gauge must reflect the store's current truth and must
	// never regress below what it reported pre-restart.
	if postRestart.PRsMergedLifetime < preRestart.PRsMergedLifetime {
		t.Errorf("post-restart PRsMergedLifetime = %d, want >= pre-restart %d",
			postRestart.PRsMergedLifetime, preRestart.PRsMergedLifetime)
	}
	if postRestart.PRsMergedLifetime != 3 {
		t.Errorf("post-restart PRsMergedLifetime = %d, want 3 (unchanged store truth)", postRestart.PRsMergedLifetime)
	}

	// A second genuine live merge happens in session 2, post-restart.
	session2.RecordPRMerged()
	final := session2.Snapshot()

	// Windowed increase() semantics check: across the whole test (session 1
	// start through session 2's live merge), exactly 2 genuine merges
	// occurred. That must equal the lifetime gauge's total delta — the
	// invariant a dashboard actually cares about — regardless of the
	// session counter having reset to 0 in between.
	totalGenuineMerges := int64(2)
	if delta := final.PRsMergedLifetime - 2; delta != totalGenuineMerges {
		t.Errorf("lifetime gauge delta across restart = %d, want %d genuine merges", delta, totalGenuineMerges)
	}
	// And the post-restart session counter reflects only the post-restart
	// activity (1), never replaying the pre-restart merge that already
	// reset to 0 at hydration.
	if final.PRsMerged != 1 {
		t.Errorf("final session PRsMerged = %d, want 1 (only the post-restart live merge)", final.PRsMerged)
	}
}

// TestHydrateFromStore_CIRunCounters pins GH-4134: pilot_ci_runs_total{result}
// hydrates from the execution_events ledger (the only available history — CI
// verdicts have no pre-ledger equivalent in the executions table), and a
// fresh live RecordCIRun() call on top of the hydrated baseline must not
// double count.
func TestHydrateFromStore_CIRunCounters(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&memory.Execution{ID: "ci-1", TaskID: "GH-1", ProjectPath: "/p", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution ci-1: %v", err)
	}
	if err := store.InsertExecutionEvent("ci-1", memory.StagePRCreated, ""); err != nil {
		t.Fatalf("InsertExecutionEvent(pr_created): %v", err)
	}
	if err := store.InsertExecutionEvent("ci-1", memory.StageCIPassed, ""); err != nil {
		t.Fatalf("InsertExecutionEvent(ci_passed): %v", err)
	}

	if err := store.SaveExecution(&memory.Execution{ID: "ci-2", TaskID: "GH-2", ProjectPath: "/p", Status: "failed"}); err != nil {
		t.Fatalf("SaveExecution ci-2: %v", err)
	}
	if err := store.InsertExecutionEvent("ci-2", memory.StagePRCreated, ""); err != nil {
		t.Fatalf("InsertExecutionEvent(pr_created): %v", err)
	}
	if err := store.InsertExecutionEvent("ci-2", memory.StageCIFailed, ""); err != nil {
		t.Fatalf("InsertExecutionEvent(ci_failed): %v", err)
	}

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	snap := metrics.Snapshot()

	if got := snap.CIRuns["pass"]; got != 1 {
		t.Errorf("CIRuns[pass] = %d, want 1", got)
	}
	if got := snap.CIRuns["fail"]; got != 1 {
		t.Errorf("CIRuns[fail] = %d, want 1", got)
	}

	// Live recording on top of the hydrated baseline adds, does not replace.
	metrics.RecordCIRun("pass")
	if got := metrics.Snapshot().CIRuns["pass"]; got != 2 {
		t.Errorf("CIRuns[pass] after live record = %d, want 2 (hydrated 1 + live 1)", got)
	}
}

// TestHydrateFromStore_CircuitBreakerTripsNeverRegressesAcrossRestart pins
// GH-4390's acceptance criterion "hydrated counters never serve a value
// lower than the last-served value across a simulated restart". Session 1
// records some trips and persists a periodic snapshot (as the live
// metrics_persister does); a restart must hydrate the fresh Metrics up to at
// least that snapshot value instead of leaving it at 0, and live recording
// on top must add rather than replace.
func TestHydrateFromStore_CircuitBreakerTripsNeverRegressesAcrossRestart(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// --- Session 1 (pre-restart): 5 trips occur, then a periodic snapshot
	// persists that value (mirrors metrics_persister.go's periodic save).
	session1 := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, session1); err != nil {
		t.Fatalf("HydrateFromStore (session 1): %v", err)
	}
	for i := 0; i < 5; i++ {
		session1.RecordCircuitBreakerTrip()
	}
	preRestart := session1.Snapshot()
	if preRestart.CircuitBreakerTrips != 5 {
		t.Fatalf("pre-restart CircuitBreakerTrips = %d, want 5", preRestart.CircuitBreakerTrips)
	}
	if err := store.SaveAutopilotMetrics(&memory.AutopilotMetricsRow{
		SnapshotAt:          time.Now(),
		CircuitBreakerTrips: int(preRestart.CircuitBreakerTrips),
	}); err != nil {
		t.Fatalf("SaveAutopilotMetrics: %v", err)
	}

	// --- Restart: fresh Metrics, re-hydrated from the same store.
	session2 := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, session2); err != nil {
		t.Fatalf("HydrateFromStore (session 2): %v", err)
	}
	postRestart := session2.Snapshot()

	// The core GH-4390 assertion: post-restart must be >= the pre-restart
	// high-water mark, not reset to 0 — a fresh 0 would make increase()/
	// rate() replay every subsequent live trip as if the counter had never
	// seen the pre-restart 5, silently losing them from the lifetime view.
	if postRestart.CircuitBreakerTrips < preRestart.CircuitBreakerTrips {
		t.Errorf("post-restart CircuitBreakerTrips = %d, want >= pre-restart %d",
			postRestart.CircuitBreakerTrips, preRestart.CircuitBreakerTrips)
	}
	if postRestart.CircuitBreakerTrips != 5 {
		t.Errorf("post-restart CircuitBreakerTrips = %d, want 5 (floored to last-served snapshot)", postRestart.CircuitBreakerTrips)
	}

	// Live recording on top of the hydrated floor adds, does not replace.
	session2.RecordCircuitBreakerTrip()
	if got := session2.Snapshot().CircuitBreakerTrips; got != 6 {
		t.Errorf("CircuitBreakerTrips after live record post-restart = %d, want 6 (floor 5 + live 1)", got)
	}
}

// TestHydrateFromStore_CircuitBreakerTripsNoSnapshotStartsAtZero verifies a
// fresh store (no periodic snapshot ever persisted, e.g. first boot) leaves
// the counter at 0 rather than erroring or panicking — LatestAutopilotMetrics
// returns (nil, nil) and HydrateCircuitBreakerTrips must handle that.
func TestHydrateFromStore_CircuitBreakerTripsNoSnapshotStartsAtZero(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	if got := metrics.Snapshot().CircuitBreakerTrips; got != 0 {
		t.Errorf("CircuitBreakerTrips with no persisted snapshot = %d, want 0", got)
	}
}

// TestHydrateFromStore_NilStoreIsNoop verifies hydration is a no-op (not an
// error) when no store is configured, matching how other optional
// store-backed features degrade in this codebase.
func TestHydrateFromStore_NilStoreIsNoop(t *testing.T) {
	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), nil, metrics); err != nil {
		t.Fatalf("HydrateFromStore with nil store: %v", err)
	}
	snap := metrics.Snapshot()
	if len(snap.TokensConsumed) != 0 {
		t.Errorf("expected no hydration with nil store, got %+v", snap.TokensConsumed)
	}
}

// TestHydrateFromStore_RestartContinuity simulates a daemon restart: snapshot
// counter values after the first hydration (pre-restart), then hydrate a
// brand-new Metrics instance from the same store (post-restart) and assert
// the post-hydration values are >= the pre-restart snapshot — the core
// GH-4041 acceptance criterion (no observable regression across restart).
func TestHydrateFromStore_RestartContinuity(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&memory.Execution{
		ID: "restart-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed",
		ModelName:   "claude-sonnet-4-5",
		TokensInput: 1000, TokensOutput: 500, TokensTotal: 1500,
		EstimatedCostUSD: 0.05,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	// Pre-restart: daemon boots, hydrates from store.
	preMetrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, preMetrics); err != nil {
		t.Fatalf("HydrateFromStore (pre-restart): %v", err)
	}
	preSnap := preMetrics.Snapshot()
	preTokens := preSnap.TokensConsumed[tokenKey{Model: "claude-sonnet-4-5", Direction: "input"}]
	preCost := preSnap.ExecutionCostUSD["claude-sonnet-4-5"]
	preExecs := preSnap.ExecutionsByResult[execKey{Model: "claude-sonnet-4-5", Result: "success"}]

	// More work happens against the same store before the simulated restart.
	if err := store.SaveExecution(&memory.Execution{
		ID: "restart-2", TaskID: "TASK-2", ProjectPath: "/p", Status: "completed",
		ModelName:   "claude-sonnet-4-5",
		TokensInput: 500, TokensOutput: 250, TokensTotal: 750,
		EstimatedCostUSD: 0.02,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	// Post-restart: a fresh (all-zero) Metrics is constructed, as happens on
	// daemon boot, then hydrated from the same store.
	postMetrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, postMetrics); err != nil {
		t.Fatalf("HydrateFromStore (post-restart): %v", err)
	}
	postSnap := postMetrics.Snapshot()
	postTokens := postSnap.TokensConsumed[tokenKey{Model: "claude-sonnet-4-5", Direction: "input"}]
	postCost := postSnap.ExecutionCostUSD["claude-sonnet-4-5"]
	postExecs := postSnap.ExecutionsByResult[execKey{Model: "claude-sonnet-4-5", Result: "success"}]

	if postTokens < preTokens {
		t.Errorf("post-restart tokens = %d, want >= pre-restart %d", postTokens, preTokens)
	}
	if postCost < preCost {
		t.Errorf("post-restart cost = %.4f, want >= pre-restart %.4f", postCost, preCost)
	}
	if postExecs < preExecs {
		t.Errorf("post-restart executions = %d, want >= pre-restart %d", postExecs, preExecs)
	}

	// No 0-then-baseline gap: the freshly constructed postMetrics never
	// observably held zero for these labels before HydrateFromStore returned —
	// nothing reads postMetrics between NewMetrics() and HydrateFromStore().
	if postTokens == 0 {
		t.Error("post-restart tokens baseline is zero, hydration did not run")
	}
}

// TestHydrateFromStore_ExcludesCanaryRows covers GH-4240: a canary sandbox
// execution — same shape as a real one (tokens, cost, model, PR url, CI
// verdict) — must not move any lifetime baseline the hydrator restores:
// token/cost/execution counters, issue-level shipped/attempted, PR merged/
// failed, and CI pass/fail. Table-driven with the flag on vs. off against an
// otherwise identical fixture.
func TestHydrateFromStore_ExcludesCanaryRows(t *testing.T) {
	build := func(t *testing.T, canary bool) *Metrics {
		t.Helper()
		tmpDir := t.TempDir()
		store, err := memory.NewStore(tmpDir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		defer func() { _ = store.Close() }()

		if err := store.SaveExecution(&memory.Execution{
			ID: "canary-real", TaskID: "TASK-REAL", ProjectPath: "/p", Status: "completed",
			ModelName: "claude-sonnet-4-5", TokensInput: 1000, TokensOutput: 500, TokensTotal: 1500,
			EstimatedCostUSD: 0.05, PRUrl: "https://github.com/o/r/pull/1",
		}); err != nil {
			t.Fatalf("SaveExecution real: %v", err)
		}
		if err := store.InsertExecutionEvent("canary-real", memory.StageCIPassed, ""); err != nil {
			t.Fatalf("InsertExecutionEvent real: %v", err)
		}

		sandboxExec := &memory.Execution{
			ID: "canary-sandbox", TaskID: "TASK-SANDBOX", ProjectPath: "/canary-sandbox", Status: "completed",
			ModelName: "claude-sonnet-4-5", TokensInput: 9000, TokensOutput: 4000, TokensTotal: 13000,
			EstimatedCostUSD: 5.00, PRUrl: "https://github.com/o/r/pull/2",
			IsCanary: canary,
		}
		if err := store.SaveExecution(sandboxExec); err != nil {
			t.Fatalf("SaveExecution sandbox: %v", err)
		}
		if err := store.InsertExecutionEvent("canary-sandbox", memory.StageCIPassed, ""); err != nil {
			t.Fatalf("InsertExecutionEvent sandbox: %v", err)
		}

		metrics := NewMetrics()
		if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
			t.Fatalf("HydrateFromStore: %v", err)
		}
		return metrics
	}

	t.Run("canary=false includes sandbox row in every baseline", func(t *testing.T) {
		snap := build(t, false).Snapshot()
		if snap.IssuesShipped != 2 {
			t.Errorf("IssuesShipped = %d, want 2", snap.IssuesShipped)
		}
		if snap.PRsMergedLifetime != 2 {
			t.Errorf("PRsMergedLifetime = %d, want 2", snap.PRsMergedLifetime)
		}
		if snap.CIRuns["pass"] != 2 {
			t.Errorf("CIRuns[pass] = %d, want 2", snap.CIRuns["pass"])
		}
	})

	t.Run("canary=true excludes sandbox row from every baseline", func(t *testing.T) {
		snap := build(t, true).Snapshot()
		if snap.IssuesShipped != 1 {
			t.Errorf("IssuesShipped = %d, want 1 (sandbox row excluded)", snap.IssuesShipped)
		}
		if snap.IssuesAttempted != 1 {
			t.Errorf("IssuesAttempted = %d, want 1 (sandbox row excluded)", snap.IssuesAttempted)
		}
		if snap.PRsMergedLifetime != 1 {
			t.Errorf("PRsMergedLifetime = %d, want 1 (sandbox row excluded)", snap.PRsMergedLifetime)
		}
		if snap.CIRuns["pass"] != 1 {
			t.Errorf("CIRuns[pass] = %d, want 1 (sandbox row excluded)", snap.CIRuns["pass"])
		}
		wantTokens := map[tokenKey]int64{
			{Model: "claude-sonnet-4-5", Direction: "input"}:  1000,
			{Model: "claude-sonnet-4-5", Direction: "output"}: 500,
		}
		for k, want := range wantTokens {
			if got := snap.TokensConsumed[k]; got != want {
				t.Errorf("TokensConsumed[%+v] = %d, want %d (sandbox tokens must not leak in)", k, got, want)
			}
		}
		const epsilon = 0.0001
		if got, want := snap.ExecutionCostUSD["claude-sonnet-4-5"], 0.05; got < want-epsilon || got > want+epsilon {
			t.Errorf("ExecutionCostUSD[claude-sonnet-4-5] = %.4f, want %.4f (sandbox cost must not leak in)", got, want)
		}
	})
}

// seedGH4211ExecutionTimes sets explicit created_at/started_at on an
// executions row via a direct SQL connection — store.SaveExecution never
// writes started_at (GH-4033's column is only stamped by
// UpdateExecutionStatus's CURRENT_TIMESTAMP), and this test needs
// deterministic, well-separated timestamps rather than relying on
// whole-second wall-clock resolution.
func seedGH4211ExecutionTimes(t *testing.T, dbPath string, executionID string, createdAt, startedAt time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(
		`UPDATE executions SET created_at = ?, started_at = ? WHERE id = ?`,
		createdAt, startedAt, executionID,
	); err != nil {
		t.Fatalf("seed execution times failed: %v", err)
	}
}

// seedGH4211EventAt inserts an execution_events row with an explicit
// occurred_at, bypassing store.InsertExecutionEvent (which always stamps
// time.Now().UTC()) so the derived duration samples below are deterministic.
func seedGH4211EventAt(t *testing.T, dbPath string, executionID string, stage memory.Stage, occurredAt time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(
		`INSERT INTO execution_events (execution_id, stage, occurred_at, detail) VALUES (?, ?, ?, ?)`,
		executionID, string(stage), occurredAt, "",
	); err != nil {
		t.Fatalf("seed event failed: %v", err)
	}
}

// TestHydrateFromStore_ThroughputHistogramsSurviveRestart pins GH-4211 D2: the
// TASK-393/GH-4128 throughput histograms (pilot_time_to_pr_seconds,
// pilot_queue_wait_seconds, pilot_approval_wait_seconds) must be reconstructed
// from the ledger/executions table at hydration time, same as
// pilot_pr_time_to_merge_seconds already is (GH-4093) — otherwise the daily
// self-upgrade restart wipes the throughput view every day even after D1's
// live-path fix.
func TestHydrateFromStore_ThroughputHistogramsSurviveRestart(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execID := "gh4211-1"
	if err := store.SaveExecution(&memory.Execution{
		ID: execID, TaskID: "GH-4211", ProjectPath: "/p", Status: "completed",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	now := time.Now()
	createdAt := now.Add(-20 * time.Minute)
	startedAt := now.Add(-14 * time.Minute)  // queue wait ~= 6m
	prCreatedAt := now.Add(-9 * time.Minute) // time-to-PR ~= 5m from startedAt
	awaitingAt := now.Add(-8 * time.Minute)
	mergedAt := now.Add(-3 * time.Minute) // approval wait ~= 5m from awaitingAt

	dbPath := filepath.Join(tmpDir, "pilot.db")
	seedGH4211ExecutionTimes(t, dbPath, execID, createdAt, startedAt)
	seedGH4211EventAt(t, dbPath, execID, memory.StageRunning, startedAt)
	seedGH4211EventAt(t, dbPath, execID, memory.StagePRCreated, prCreatedAt)
	seedGH4211EventAt(t, dbPath, execID, memory.StageAwaitingApproval, awaitingAt)
	seedGH4211EventAt(t, dbPath, execID, memory.StageMerged, mergedAt)

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	hist := metrics.HistogramSnapshot()

	if len(hist.TimeToPRDurations) != 1 {
		t.Fatalf("TimeToPRDurations = %v, want 1 hydrated sample", hist.TimeToPRDurations)
	}
	if got := hist.TimeToPRDurations[0]; got < 4*time.Minute || got > 6*time.Minute {
		t.Errorf("TimeToPRDurations[0] = %v, want ~5m", got)
	}

	if len(hist.QueueWaitDurations) != 1 {
		t.Fatalf("QueueWaitDurations = %v, want 1 hydrated sample", hist.QueueWaitDurations)
	}
	if got := hist.QueueWaitDurations[0]; got < 5*time.Minute || got > 7*time.Minute {
		t.Errorf("QueueWaitDurations[0] = %v, want ~6m", got)
	}

	if len(hist.ApprovalWaitDurations) != 1 {
		t.Fatalf("ApprovalWaitDurations = %v, want 1 hydrated sample", hist.ApprovalWaitDurations)
	}
	if got := hist.ApprovalWaitDurations[0]; got < 4*time.Minute || got > 6*time.Minute {
		t.Errorf("ApprovalWaitDurations[0] = %v, want ~5m", got)
	}

	// Restart simulation: a fresh (all-zero) Metrics hydrated from the same
	// store must not observe zero for any of the three histograms.
	restarted := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, restarted); err != nil {
		t.Fatalf("HydrateFromStore (post-restart): %v", err)
	}
	restartedHist := restarted.HistogramSnapshot()
	if len(restartedHist.TimeToPRDurations) == 0 {
		t.Error("TimeToPRDurations reset to zero on simulated restart")
	}
	if len(restartedHist.QueueWaitDurations) == 0 {
		t.Error("QueueWaitDurations reset to zero on simulated restart")
	}
	if len(restartedHist.ApprovalWaitDurations) == 0 {
		t.Error("ApprovalWaitDurations reset to zero on simulated restart")
	}
}

// TestHydrateFromStore_ThroughputHistogramsExcludeCanary covers GH-4240: a
// canary sandbox execution with a full running->pr_created->awaiting_approval
// ->merged event chain (same shape TestHydrateFromStore_ThroughputHistogramsSurviveRestart
// exercises for a real one) must not contribute a sample to any of the three
// throughput histograms, even though execution_events carries no project or
// canary information of its own — the hydrator must join back to executions.
func TestHydrateFromStore_ThroughputHistogramsExcludeCanary(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execID := "gh4240-canary"
	if err := store.SaveExecution(&memory.Execution{
		ID: execID, TaskID: "GH-4240-CANARY", ProjectPath: "/canary-sandbox", Status: "completed",
		IsCanary: true,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	now := time.Now()
	createdAt := now.Add(-20 * time.Minute)
	startedAt := now.Add(-14 * time.Minute)
	prCreatedAt := now.Add(-9 * time.Minute)
	awaitingAt := now.Add(-8 * time.Minute)
	mergedAt := now.Add(-3 * time.Minute)

	dbPath := filepath.Join(tmpDir, "pilot.db")
	seedGH4211ExecutionTimes(t, dbPath, execID, createdAt, startedAt)
	seedGH4211EventAt(t, dbPath, execID, memory.StageRunning, startedAt)
	seedGH4211EventAt(t, dbPath, execID, memory.StagePRCreated, prCreatedAt)
	seedGH4211EventAt(t, dbPath, execID, memory.StageAwaitingApproval, awaitingAt)
	seedGH4211EventAt(t, dbPath, execID, memory.StageMerged, mergedAt)

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	hist := metrics.HistogramSnapshot()

	if len(hist.TimeToPRDurations) != 0 {
		t.Errorf("TimeToPRDurations = %v, want 0 (canary row must be excluded)", hist.TimeToPRDurations)
	}
	if len(hist.QueueWaitDurations) != 0 {
		t.Errorf("QueueWaitDurations = %v, want 0 (canary row must be excluded)", hist.QueueWaitDurations)
	}
	if len(hist.ApprovalWaitDurations) != 0 {
		t.Errorf("ApprovalWaitDurations = %v, want 0 (canary row must be excluded)", hist.ApprovalWaitDurations)
	}

	if len(hist.PRTimeToMerge) != 0 {
		t.Errorf("PRTimeToMerge = %v, want 0 (canary row must be excluded)", hist.PRTimeToMerge)
	}
}

// TestHydrateWindowStats_SeedsGaugesFromStore is GH-4735: HydrateWindowStats
// must populate the pilot_window_* gauge fields on Metrics from a single
// GetWindowedStats query, scoped fleet-wide ("") regardless of the
// executions' individual project paths.
func TestHydrateWindowStats_SeedsGaugesFromStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	if err := store.SaveExecution(&memory.Execution{
		ID: "ws-hydrate-1", TaskID: "TASK-1", ProjectPath: "/p1", Status: "completed",
		CreatedAt: now, EstimatedCostUSD: 1.00,
	}); err != nil {
		t.Fatalf("SaveExecution 1: %v", err)
	}
	if err := store.SaveExecution(&memory.Execution{
		ID: "ws-hydrate-2", TaskID: "TASK-2", ProjectPath: "/p2", Status: "failed",
		CreatedAt: now, EstimatedCostUSD: 0.50,
	}); err != nil {
		t.Fatalf("SaveExecution 2: %v", err)
	}
	// Outside the 30-day window: must not affect the hydrated gauges.
	if err := store.SaveExecution(&memory.Execution{
		ID: "ws-hydrate-old", TaskID: "TASK-3", ProjectPath: "/p1", Status: "completed",
		CreatedAt: now.AddDate(0, 0, -60), EstimatedCostUSD: 100.00,
	}); err != nil {
		t.Fatalf("SaveExecution old: %v", err)
	}

	metrics := NewMetrics()
	if err := HydrateWindowStats(store, metrics, 30); err != nil {
		t.Fatalf("HydrateWindowStats: %v", err)
	}

	snap := metrics.Snapshot()
	const epsilon = 0.0001
	if snap.WindowDays != 30 {
		t.Errorf("WindowDays = %d, want 30", snap.WindowDays)
	}
	if got, want := snap.WindowCostUSD, 1.50; got < want-epsilon || got > want+epsilon {
		t.Errorf("WindowCostUSD = %.4f, want %.4f (60-day-old row must be excluded, both projects included)", got, want)
	}
	if got, want := snap.WindowCostPerDeliveredUSD, 1.50; got < want-epsilon || got > want+epsilon {
		t.Errorf("WindowCostPerDeliveredUSD = %.4f, want %.4f (1 delivered issue)", got, want)
	}
	if got, want := snap.WindowDeliveryRate, 0.5; got < want-epsilon || got > want+epsilon {
		t.Errorf("WindowDeliveryRate = %.4f, want %.4f (1 delivered / 2 attempted)", got, want)
	}
	if got, want := snap.WindowAttemptSuccessRate, 0.5; got < want-epsilon || got > want+epsilon {
		t.Errorf("WindowAttemptSuccessRate = %.4f, want %.4f (1 completed / (1 completed + 1 failed))", got, want)
	}
}

// TestHydrateWindowStats_NilStoreIsNoop mirrors
// TestHydrateFromStore_NilStoreIsNoop for the window-stats hydrator.
func TestHydrateWindowStats_NilStoreIsNoop(t *testing.T) {
	metrics := NewMetrics()
	if err := HydrateWindowStats(nil, metrics, 30); err != nil {
		t.Fatalf("HydrateWindowStats with nil store: %v", err)
	}
	snap := metrics.Snapshot()
	if snap.WindowDays != 0 {
		t.Errorf("WindowDays = %d, want 0 (no hydration with nil store)", snap.WindowDays)
	}
}

// TestStartWindowStatsRefresher_RefreshesOnTicker is GH-4735: the background
// refresher must re-run HydrateWindowStats on each tick so the gauges pick up
// executions saved after the initial synchronous hydration, and the returned
// stop function must be safe to call multiple times.
func TestStartWindowStatsRefresher_RefreshesOnTicker(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	metrics := NewMetrics()
	if err := HydrateWindowStats(store, metrics, 30); err != nil {
		t.Fatalf("initial HydrateWindowStats: %v", err)
	}
	if got := metrics.Snapshot().WindowCostUSD; got != 0 {
		t.Fatalf("WindowCostUSD before seeding = %.4f, want 0", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := StartWindowStatsRefresher(ctx, store, metrics, 30, 10*time.Millisecond)
	defer stop()

	if err := store.SaveExecution(&memory.Execution{
		ID: "ws-refresh-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed",
		CreatedAt: time.Now().UTC(), EstimatedCostUSD: 3.25,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := metrics.Snapshot().WindowCostUSD; got >= 3.25 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := metrics.Snapshot().WindowCostUSD; got < 3.25 {
		t.Fatalf("WindowCostUSD after refresh = %.4f, want >= 3.25 (ticker must pick up the new execution)", got)
	}

	// stop must be idempotent (sync.OnceFunc-backed).
	stop()
}

// TestStartWindowStatsRefresher_NilStoreIsNoop asserts the refresher returns
// a harmless no-op stop function rather than panicking when store/metrics
// are nil, mirroring the nil-guard on HydrateWindowStats itself.
func TestStartWindowStatsRefresher_NilStoreIsNoop(t *testing.T) {
	stop := StartWindowStatsRefresher(context.Background(), nil, NewMetrics(), 30, time.Millisecond)
	stop() // must not panic
}

// TestHydrateWindowStats_ZeroOrNegativeWindowDaysClamps pins GH-4738's
// defensive fallback: a caller passing windowDays <= 0 (e.g. a config path
// that skipped the config.DefaultDashboardStatsWindowDays fallback) must not
// silently query since=now() — which would report window="0d" with all-zero
// values even though the store has data — but instead clamp to
// defaultStatsWindowDays, matching the TUI/gateway dashboard fallbacks.
func TestHydrateWindowStats_ZeroOrNegativeWindowDaysClamps(t *testing.T) {
	for _, windowDays := range []int{0, -5} {
		t.Run(fmt.Sprintf("windowDays=%d", windowDays), func(t *testing.T) {
			tmpDir := t.TempDir()
			store, err := memory.NewStore(tmpDir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			if err := store.SaveExecution(&memory.Execution{
				ID: "clamp-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed",
				CreatedAt: time.Now().UTC(), EstimatedCostUSD: 2.00,
			}); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			metrics := NewMetrics()
			if err := HydrateWindowStats(store, metrics, windowDays); err != nil {
				t.Fatalf("HydrateWindowStats: %v", err)
			}

			snap := metrics.Snapshot()
			if snap.WindowDays != defaultStatsWindowDays {
				t.Errorf("WindowDays = %d, want clamped default %d", snap.WindowDays, defaultStatsWindowDays)
			}
			const epsilon = 0.0001
			if got, want := snap.WindowCostUSD, 2.00; got < want-epsilon || got > want+epsilon {
				t.Errorf("WindowCostUSD = %.4f, want %.4f (clamp must still query a real window, not since=now())", got, want)
			}
		})
	}
}
