package executor

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

// GH-5080: cross-package regression coverage for the pilot-failed-retry-N
// ladder (GH-3715/GH-5077 lineage) that cannot live inside a single
// implementation package:
//
//   - the merge-time full-ladder strip (auto_merger.go:112) is pinned in
//     internal/autopilot/auto_merger_test.go
//     (TestAutoMerger_MergePR_StripsFailedRetryLadder_AtEachRung);
//   - this file covers the ladder-advancement lifecycle itself
//     (nextFailedRetryLabel/postTitleRejectionEscalation, both unexported
//     here in internal/executor — the only package that can drive them) and
//     its durability across a simulated daemon restart.
//
// Both tests below thread a "durable GitHub label state" between simulated
// fail-label cycles by parsing each cycle's single `gh issue edit` call for
// its --add-label/--remove-label tokens and folding only the recognized
// ladder-rung label into the next cycle's input (nextLadderState). This
// mirrors the real operational lifecycle: pilot-failed/pilot-title-rejected
// are cleared by a separate retry-ready hand-off before the issue is
// redispatched (see buildTitleRejectionComment's own suggested `gh issue
// edit --remove-label pilot-failed --remove-label pilot-title-rejected
// --add-label pilot-retry-ready` command), but the ladder rung itself is
// never touched by anything except this one code path, so it is the one
// label that survives verbatim from one fail-label event to the next.

// isLadderRungLabel reports whether l is one of the three pilot-failed-retry-N
// rung labels (as opposed to pilot-failed, pilot-title-rejected, or any other
// label a `gh issue edit` call in this path also touches).
func isLadderRungLabel(l string) bool {
	return l == labelPilotFailedRetry1 || l == labelPilotFailedRetry2 || l == labelPilotFailedRetryExhausted
}

// containsLabel reports whether target is present in labels.
func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// parseIssueEditLabels extracts the --add-label/--remove-label tokens from
// the sole `gh issue edit` invocation logged in calls (the fake gh CLI
// installed by setupFakeGhCLI echoes argv, one line per invocation). Fails
// the test if the escalation didn't produce exactly one such call, mirroring
// gh5077_failed_retry_ladder_test.go's single-mutation assertion.
func parseIssueEditLabels(t *testing.T, calls string) (add, remove []string) {
	t.Helper()
	lines := grepLinesStartingWith(calls, "issue edit")
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 `gh issue edit` call, got %d:\n%s", len(lines), calls)
	}
	tokens := strings.Fields(lines[0])
	for i := 0; i < len(tokens)-1; i++ {
		switch tokens[i] {
		case "--add-label":
			add = append(add, tokens[i+1])
		case "--remove-label":
			remove = append(remove, tokens[i+1])
		}
	}
	return add, remove
}

// nextLadderState simulates GitHub applying one `gh issue edit` call's
// add/remove label mutation, keeping only the ladder-rung label(s) for the
// next simulated cycle (see file-level comment for why only the rung
// persists).
func nextLadderState(current []string, add, remove []string) []string {
	set := make(map[string]bool)
	for _, l := range current {
		if isLadderRungLabel(l) {
			set[l] = true
		}
	}
	for _, l := range remove {
		delete(set, l)
	}
	for _, l := range add {
		if isLadderRungLabel(l) {
			set[l] = true
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	return out
}

// TestLadderThreeCycle_EndToEnd_AdvancesThenNeverReAdmits is GH-5080 (b): an
// end-to-end simulation of three consecutive fail-label events for the same
// issue (repeated non-conventional-title rejections — the only production
// path that advances the ladder, per postTitleRejectionEscalation), asserting
// the ladder advances retry-1 -> retry-2 -> exhausted one rung per cycle,
// that the exhausted transition (cycle 3) parks the issue under
// pilot-needs-human and fires an escalation alert in that same mutation
// (GH-5079, composed here through the same fake-gh/alert-processor seams as
// gh5079_exhaustion_escalation_test.go), and that a 4th fail-label event on
// an already-exhausted issue never creates a new rung, removes the terminal
// one, or re-fires the escalation — the ladder is a hard cap, not a rolling
// window. The admission-side half of the park contract — that an issue
// carrying pilot-needs-human is never re-admitted for dispatch — is already
// independently covered by cmd/pilot/gh5056_needs_human_admission_test.go.
func TestLadderThreeCycle_EndToEnd_AdvancesThenNeverReAdmits(t *testing.T) {
	const issueNum = 9301
	var ladder []string // durable GitHub label state, threaded across cycles

	// Cycle 1: fresh issue, no ladder label yet -> retry-1.
	calls := runPostTitleRejectionEscalation(t, issueNum, ladder)
	add, remove := parseIssueEditLabels(t, calls)
	if !containsLabel(add, labelPilotFailedRetry1) {
		t.Fatalf("cycle 1: expected %s to be added, got calls:\n%s", labelPilotFailedRetry1, calls)
	}
	ladder = nextLadderState(ladder, add, remove)
	if !containsLabel(ladder, labelPilotFailedRetry1) || len(ladder) != 1 {
		t.Fatalf("cycle 1: expected ladder state [%s], got %v", labelPilotFailedRetry1, ladder)
	}

	// Cycle 2: retry-1 -> retry-2.
	calls = runPostTitleRejectionEscalation(t, issueNum, ladder)
	add, remove = parseIssueEditLabels(t, calls)
	if !containsLabel(add, labelPilotFailedRetry2) || !containsLabel(remove, labelPilotFailedRetry1) {
		t.Fatalf("cycle 2: expected %s added and %s removed, got calls:\n%s", labelPilotFailedRetry2, labelPilotFailedRetry1, calls)
	}
	ladder = nextLadderState(ladder, add, remove)
	if !containsLabel(ladder, labelPilotFailedRetry2) || len(ladder) != 1 {
		t.Fatalf("cycle 2: expected ladder state [%s], got %v", labelPilotFailedRetry2, ladder)
	}

	// Cycle 3: retry-2 -> exhausted. GH-5079: this is also the mutation that
	// parks the issue under pilot-needs-human (shedding pilot-retry-ready)
	// and fires the escalation alert — assert both here, through the same
	// fake-gh/alert-processor seam gh5079's unit tests use
	// (runPostTitleRejectionEscalationWithAlerts), so the e2e flow proves the
	// park+alert contract actually composes with the 3-cycle ladder
	// advancement rather than living only in isolated unit tests.
	calls, cycle3Events := runPostTitleRejectionEscalationWithAlerts(t, issueNum, ladder)
	add, remove = parseIssueEditLabels(t, calls)
	if !containsLabel(add, labelPilotFailedRetryExhausted) || !containsLabel(remove, labelPilotFailedRetry2) {
		t.Fatalf("cycle 3: expected %s added and %s removed, got calls:\n%s", labelPilotFailedRetryExhausted, labelPilotFailedRetry2, calls)
	}
	if !containsLabel(add, labelPilotNeedsHuman) {
		t.Errorf("cycle 3: expected %s added in the same mutation as the exhausted rung, got calls:\n%s", labelPilotNeedsHuman, calls)
	}
	if !containsLabel(remove, labelPilotRetryReady) {
		t.Errorf("cycle 3: expected %s removed in the same mutation (GH-5042/PR#5048 never-coexist invariant), got calls:\n%s", labelPilotRetryReady, calls)
	}
	if len(cycle3Events) != 1 {
		t.Fatalf("cycle 3: expected exactly 1 escalation alert on the exhausted transition, got %d: %+v", len(cycle3Events), cycle3Events)
	}
	if cycle3Events[0].Type != AlertEventTypeEscalation {
		t.Errorf("cycle 3: event.Type = %q, want %q", cycle3Events[0].Type, AlertEventTypeEscalation)
	}
	if !strings.Contains(cycle3Events[0].Error, strconv.Itoa(issueNum)) {
		t.Errorf("cycle 3: event.Error should name the issue, got: %q", cycle3Events[0].Error)
	}
	if !strings.Contains(cycle3Events[0].Error, labelPilotFailedRetryExhausted) {
		t.Errorf("cycle 3: event.Error should name the exhausted rung, got: %q", cycle3Events[0].Error)
	}
	ladder = nextLadderState(ladder, add, remove)
	if !containsLabel(ladder, labelPilotFailedRetryExhausted) || len(ladder) != 1 {
		t.Fatalf("cycle 3: expected ladder state [%s], got %v", labelPilotFailedRetryExhausted, ladder)
	}

	// Cycle 4: exhausted is terminal — no new rung is ever created, the
	// terminal rung is never removed by a further fail-label event, and the
	// park+escalation from cycle 3 must not be duplicated: pilot-needs-human
	// is already applied and the alert already fired once, so a repeat
	// fail-label event on an already-parked issue must add nothing further
	// and emit no additional alert.
	calls, cycle4Events := runPostTitleRejectionEscalationWithAlerts(t, issueNum, ladder)
	add, remove = parseIssueEditLabels(t, calls)
	if containsLabel(add, labelPilotFailedRetry1) || containsLabel(add, labelPilotFailedRetry2) {
		t.Errorf("cycle 4: no new rung should ever be created past exhaustion, add=%v", add)
	}
	if containsLabel(add, labelPilotNeedsHuman) {
		t.Errorf("cycle 4: pilot-needs-human must not be re-applied — it was already applied on the exhausted transition (cycle 3), add=%v", add)
	}
	if len(remove) != 0 {
		t.Errorf("cycle 4: exhausted rung must never be removed by a fail-label event, remove=%v", remove)
	}
	if len(cycle4Events) != 0 {
		t.Errorf("cycle 4: escalation alert must not re-fire once already parked, got %d: %+v", len(cycle4Events), cycle4Events)
	}
	ladder = nextLadderState(ladder, add, remove)
	if !containsLabel(ladder, labelPilotFailedRetryExhausted) || len(ladder) != 1 {
		t.Errorf("cycle 4: ladder state must remain capped at [%s], got %v", labelPilotFailedRetryExhausted, ladder)
	}
}

// TestLadderRestartPersistence_SurvivesInMemoryReset is GH-5080 (c): rebuilds
// the Runner — and, critically, its titleRejectionTracker — fresh between
// every cycle, simulating a daemon restart, and confirms the ladder rung
// still advances correctly from wherever GitHub's labels say it left off.
// This proves the durable state is the GitHub label, not the in-memory
// titleRejectionTracker, whose own doc comment already documents it as
// intentionally non-durable ("Process restarts reset the counter... The user
// still gets at most a handful of redundant comments before the guard
// re-engages").
//
// Each cycle drives recordTitleRejection (not postTitleRejectionEscalation
// directly, unlike the sibling end-to-end test above) because that is the
// path that actually reads titleRejections — the counter this test proves
// does NOT gate the durable ladder cap. A fresh tracker requires two calls
// with the same still-non-conventional title to reach titleRejectionMaxCount
// and fire the escalation, exactly like a freshly restarted daemon
// re-observing the same still-broken issue for the first time.
func TestLadderRestartPersistence_SurvivesInMemoryReset(t *testing.T) {
	const issueNum = 9302
	const title = "not a conventional title"

	// restartAndEscalate rebuilds an entirely fresh Runner + tracker — a
	// process restart carries none of the prior in-memory state forward —
	// then drives recordTitleRejection twice against the given durable
	// ladder state (as if freshly read from GitHub), returning the raw gh
	// CLI calls from the escalating (2nd) invocation and the resulting
	// ladder state.
	restartAndEscalate := func(currentLadder []string) (calls string, newLadder []string) {
		r := newSilentRunnerTask359()
		r.titleRejections = newTitleRejectionTracker()

		logFile := setupFakeGhCLI(t)
		stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
			return IssueState{Closed: false, Labels: currentLadder}, nil
		})
		task := &Task{
			ID:            "unused",
			Title:         title,
			ProjectPath:   t.TempDir(),
			SourceAdapter: "github",
			SourceIssueID: strconv.Itoa(issueNum),
		}

		result1 := &ExecutionResult{TaskID: task.ID}
		r.recordTitleRejection(context.Background(), task, result1)
		if result1.TitleRejected {
			t.Fatalf("first rejection after restart must not escalate yet (fresh tracker), title=%q", title)
		}

		result2 := &ExecutionResult{TaskID: task.ID}
		r.recordTitleRejection(context.Background(), task, result2)
		if !result2.TitleRejected {
			t.Fatalf("second consecutive rejection must escalate")
		}

		logBytes, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatalf("expected gh CLI to be invoked: %v", err)
		}
		calls = string(logBytes)
		add, remove := parseIssueEditLabels(t, calls)
		newLadder = nextLadderState(currentLadder, add, remove)
		return calls, newLadder
	}

	var ladder []string

	// Cycle 1 (fresh daemon, issue never failed before): none -> retry-1.
	_, ladder = restartAndEscalate(ladder)
	if !containsLabel(ladder, labelPilotFailedRetry1) || len(ladder) != 1 {
		t.Fatalf("cycle 1 (post-restart): expected ladder [%s], got %v", labelPilotFailedRetry1, ladder)
	}

	// Cycle 2 (restart again): retry-1 -> retry-2, despite the in-memory
	// tracker having forgotten this issue was ever escalated before.
	_, ladder = restartAndEscalate(ladder)
	if !containsLabel(ladder, labelPilotFailedRetry2) || len(ladder) != 1 {
		t.Fatalf("cycle 2 (post-restart): expected ladder [%s], got %v", labelPilotFailedRetry2, ladder)
	}

	// Cycle 3 (restart again): retry-2 -> exhausted.
	_, ladder = restartAndEscalate(ladder)
	if !containsLabel(ladder, labelPilotFailedRetryExhausted) || len(ladder) != 1 {
		t.Fatalf("cycle 3 (post-restart): expected ladder [%s], got %v", labelPilotFailedRetryExhausted, ladder)
	}

	// Cycle 4 (restart again): exhausted is a durable terminal cap — it must
	// hold even though the in-memory tracker has again been wiped and has no
	// record that this issue is "done" retrying.
	var calls string
	calls, ladder = restartAndEscalate(ladder)
	add, remove := parseIssueEditLabels(t, calls)
	if containsLabel(add, labelPilotFailedRetry1) || containsLabel(add, labelPilotFailedRetry2) {
		t.Errorf("cycle 4 (post-restart): no new rung should ever be created past exhaustion, add=%v", add)
	}
	if len(remove) != 0 {
		t.Errorf("cycle 4 (post-restart): exhausted rung must never be removed, remove=%v", remove)
	}
	if !containsLabel(ladder, labelPilotFailedRetryExhausted) || len(ladder) != 1 {
		t.Errorf("cycle 4 (post-restart): ladder must remain capped at [%s], got %v", labelPilotFailedRetryExhausted, ladder)
	}
}
