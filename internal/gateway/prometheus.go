package gateway

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/logging"
)

// goPanicCounter is the concrete PanicCounter wired into the logging package.
// It accumulates per-component panic counts and is read by WritePrometheus.
type goPanicCounter struct {
	mu     sync.Mutex
	counts map[string]int64
}

func newGoPanicCounter() *goPanicCounter {
	return &goPanicCounter{counts: make(map[string]int64)}
}

func (c *goPanicCounter) Inc(component string) {
	c.mu.Lock()
	c.counts[component]++
	c.mu.Unlock()
}

func (c *goPanicCounter) snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.counts))
	for k, v := range c.counts {
		out[k] = v
	}
	return out
}

// PrometheusExporter formats metrics for Prometheus scraping.
type PrometheusExporter struct {
	metricsSource MetricsSource
	alertsSource  AlertMetricsSource
	panicCtr      *goPanicCounter
}

// MetricsSource provides metrics data for the exporter.
type MetricsSource interface {
	Snapshot() autopilot.MetricsSnapshot
	HistogramSnapshot() autopilot.HistogramData
}

// AlertMetricsSource provides alert metrics for the exporter.
// *alerts.Engine satisfies this interface via its AlertSnapshot method.
type AlertMetricsSource interface {
	AlertSnapshot() alerts.AlertMetricsSnapshot
}

// NewPrometheusExporter creates a new Prometheus exporter and wires the panic counter.
func NewPrometheusExporter(source MetricsSource) *PrometheusExporter {
	ctr := newGoPanicCounter()
	logging.SetPanicCounter(ctr)
	return &PrometheusExporter{metricsSource: source, panicCtr: ctr}
}

// SetAlertsSource wires an alert metrics source into the exporter.
func (e *PrometheusExporter) SetAlertsSource(s AlertMetricsSource) {
	e.alertsSource = s
}

// WritePrometheus writes metrics in Prometheus text format to the writer.
func (e *PrometheusExporter) WritePrometheus(w io.Writer) error {
	snap := e.metricsSource.Snapshot()
	hist := e.metricsSource.HistogramSnapshot()

	// --- Counters ---

	// pilot_issues_processed_total
	writeHelp(w, "pilot_issues_processed_total", "Total issues processed by result type")
	writeType(w, "pilot_issues_processed_total", "counter")
	for result, count := range snap.IssuesProcessed {
		writeCounter(w, "pilot_issues_processed_total", count, "result", result)
	}
	// Ensure standard results always appear (even if 0)
	for _, result := range []string{"success", "failed", "rate_limited"} {
		if _, exists := snap.IssuesProcessed[result]; !exists {
			writeCounter(w, "pilot_issues_processed_total", 0, "result", result)
		}
	}

	// pilot_prs_merged_total (GH-4511): session-scoped only — starts at 0 on
	// every process restart and is never hydrated with a store baseline. A
	// hydrated baseline previously caused Prometheus counter-reset artifacts:
	// when the baseline landed below the pre-restart live value,
	// increase()/rate() treated it as a reset and replayed the whole
	// baseline as fabricated new activity. Use pilot_prs_merged_lifetime for
	// the all-time total instead.
	writeHelp(w, "pilot_prs_merged_total", "PRs successfully merged since this process started, one per shipped issue (task_id) regardless of retries. Resets to 0 on restart — see pilot_prs_merged_lifetime for the all-time total")
	writeType(w, "pilot_prs_merged_total", "counter")
	writeCounter(w, "pilot_prs_merged_total", snap.PRsMerged)

	// pilot_prs_failed_total (GH-4511): session-scoped only, see
	// pilot_prs_merged_total above.
	writeHelp(w, "pilot_prs_failed_total", "Genuine PR-family failures since this process started (a PR was created but failed CI/merge/release; excludes coding-stage failures with no PR and issues that later shipped). Resets to 0 on restart — see pilot_prs_failed_lifetime for the all-time total")
	writeType(w, "pilot_prs_failed_total", "counter")
	writeCounter(w, "pilot_prs_failed_total", snap.PRsFailed)

	// pilot_prs_merged_lifetime / pilot_prs_failed_lifetime (GH-4511): gauges
	// hydrated once at boot from the store's durable lifetime baseline
	// (GetLifetimePRCountersFromExecutions) and bumped alongside the session
	// counters above on every live merge/failure. Gauges are immune to the
	// counter-reset misinterpretation that affected the old hydrated
	// counters, so this is the correct series for all-time/dashboard totals
	// across restarts.
	writeHelp(w, "pilot_prs_merged_lifetime", "Total PRs successfully merged, all-time across restarts, one per shipped issue (task_id) regardless of retries")
	writeType(w, "pilot_prs_merged_lifetime", "gauge")
	writeGauge(w, "pilot_prs_merged_lifetime", float64(snap.PRsMergedLifetime))

	writeHelp(w, "pilot_prs_failed_lifetime", "Total genuine PR-family failures, all-time across restarts (a PR was created but failed CI/merge/release; excludes coding-stage failures with no PR and issues that later shipped)")
	writeType(w, "pilot_prs_failed_lifetime", "gauge")
	writeGauge(w, "pilot_prs_failed_lifetime", float64(snap.PRsFailedLifetime))

	// pilot_ci_runs_total (GH-4134): true CI pass/fail counter, distinct from
	// the pilot_prs_failed_total proxy — that counter also folds in approval
	// rejections, merge escalations, and size-guard failures (see
	// RecordPRFailed call sites), so it overcounts as a CI health signal.
	// Lifetime baseline comes from the execution_events ledger (see
	// GetLifetimeCIRunCounters); verdicts predating the ledger are not
	// recoverable, unlike pilot_prs_merged_total/pilot_prs_failed_total which
	// backfill from the executions table.
	writeHelp(w, "pilot_ci_runs_total", "Total CI verdicts by result (pass, fail), one per distinct CI run — not per fix-iteration retry. CI pass rate = sum(result=\"pass\") / sum(all). Lifetime baseline hydrated from the execution_events ledger only (no pre-ledger source), so restarts before the ledger's introduction are not reflected")
	writeType(w, "pilot_ci_runs_total", "counter")
	for result, count := range snap.CIRuns {
		writeCounter(w, "pilot_ci_runs_total", count, "result", result)
	}
	for _, result := range []string{"pass", "fail"} {
		if _, exists := snap.CIRuns[result]; !exists {
			writeCounter(w, "pilot_ci_runs_total", 0, "result", result)
		}
	}

	// pilot_prs_conflicting_total
	writeHelp(w, "pilot_prs_conflicting_total", "Total PRs with merge conflicts")
	writeType(w, "pilot_prs_conflicting_total", "counter")
	writeCounter(w, "pilot_prs_conflicting_total", snap.PRsConflicting)

	// pilot_circuit_breaker_trips_total
	writeHelp(w, "pilot_circuit_breaker_trips_total", "Total circuit breaker trips")
	writeType(w, "pilot_circuit_breaker_trips_total", "counter")
	writeCounter(w, "pilot_circuit_breaker_trips_total", snap.CircuitBreakerTrips)

	// pilot_rate_limit_floor_engaged_total (GH-4391): counts distinct
	// episodes where the shared GitHub rate-limit budget dropped below the
	// reserved floor and background scans (merged-PR scans, orphan-PR
	// sweeps, reconciler evidence fetches) were paused. Pollers and
	// active-PR CI watches are never gated, so this metric being nonzero
	// does not imply degraded polling.
	writeHelp(w, "pilot_rate_limit_floor_engaged_total", "Total GitHub rate-limit budget-floor engagement episodes (background scans paused; pollers/active-PR CI watches unaffected)")
	writeType(w, "pilot_rate_limit_floor_engaged_total", "counter")
	writeCounter(w, "pilot_rate_limit_floor_engaged_total", snap.RateLimitFloorEngagements)

	// pilot_api_errors_total
	writeHelp(w, "pilot_api_errors_total", "Total API errors by endpoint")
	writeType(w, "pilot_api_errors_total", "counter")
	for endpoint, count := range snap.APIErrors {
		writeCounter(w, "pilot_api_errors_total", count, "endpoint", endpoint)
	}

	// pilot_intent_judge_failures_total (GH-4377): pre-flight intent-judge
	// subprocess failures by cause — makes a fully-dead judge visible instead
	// of silently failing open on every issue (previously only a WARN log
	// line with no counter).
	writeHelp(w, "pilot_intent_judge_failures_total", "Total pre-flight intent-judge subprocess failures by cause (context_deadline, external_sigkill, other)")
	writeType(w, "pilot_intent_judge_failures_total", "counter")
	for cause, count := range snap.IntentJudgeFailures {
		writeCounter(w, "pilot_intent_judge_failures_total", count, "cause", cause)
	}

	// pilot_label_cleanups_total
	writeHelp(w, "pilot_label_cleanups_total", "Total label cleanup operations")
	writeType(w, "pilot_label_cleanups_total", "counter")
	for label, count := range snap.LabelCleanups {
		writeCounter(w, "pilot_label_cleanups_total", count, "label", label)
	}

	// pilot_approval_persist_misses_total
	writeHelp(w, "pilot_approval_persist_misses_total", "Total zero-row approval UPDATE misses by kind (request_id, decision)")
	writeType(w, "pilot_approval_persist_misses_total", "counter")
	for kind, count := range snap.ApprovalPersistMisses {
		writeCounter(w, "pilot_approval_persist_misses_total", count, "kind", kind)
	}
	for _, kind := range []string{"request_id", "decision"} {
		if _, exists := snap.ApprovalPersistMisses[kind]; !exists {
			writeCounter(w, "pilot_approval_persist_misses_total", 0, "kind", kind)
		}
	}

	// pilot_approval_submit_failures_total (GH-4380)
	writeHelp(w, "pilot_approval_submit_failures_total", "Total SubmitApprovalRequest errors (unregistered/misrouted approval channel)")
	writeType(w, "pilot_approval_submit_failures_total", "counter")
	writeCounter(w, "pilot_approval_submit_failures_total", snap.ApprovalSubmitFailures)

	// pilot_tokens_consumed_total
	writeHelp(w, "pilot_tokens_consumed_total", "Total tokens consumed by model and direction")
	writeType(w, "pilot_tokens_consumed_total", "counter")
	for k, v := range snap.TokensConsumed {
		writeCounter(w, "pilot_tokens_consumed_total", v, "model", k.Model, "direction", k.Direction)
	}

	// pilot_execution_cost_usd_total
	writeHelp(w, "pilot_execution_cost_usd_total", "Total execution cost in USD by model")
	writeType(w, "pilot_execution_cost_usd_total", "counter")
	for model, cost := range snap.ExecutionCostUSD {
		writeFloatCounter(w, "pilot_execution_cost_usd_total", cost, "model", model)
	}

	// pilot_executions_total
	writeHelp(w, "pilot_executions_total", "Total executions by model and result")
	writeType(w, "pilot_executions_total", "counter")
	for k, v := range snap.ExecutionsByResult {
		writeCounter(w, "pilot_executions_total", v, "model", k.Model, "result", k.Result)
	}

	// pilot_poller_skipped_total
	writeHelp(w, "pilot_poller_skipped_total", "Issues/work-items skipped by the poller before dispatch, by repo and reason")
	writeType(w, "pilot_poller_skipped_total", "counter")
	for k, count := range snap.PollerSkipped {
		writeCounter(w, "pilot_poller_skipped_total", count, "repo", k.Repo, "reason", k.Reason)
	}

	// pilot_poller_dispatched_total
	writeHelp(w, "pilot_poller_dispatched_total", "Issues/work-items dispatched by the poller for execution, by repo")
	writeType(w, "pilot_poller_dispatched_total", "counter")
	for repo, count := range snap.PollerDispatched {
		writeCounter(w, "pilot_poller_dispatched_total", count, "repo", repo)
	}

	// pilot_poller_deferred_scope_overlap_total
	writeHelp(w, "pilot_poller_deferred_scope_overlap_total", "Issues deferred due to overlapping scope with an older issue, by repo")
	writeType(w, "pilot_poller_deferred_scope_overlap_total", "counter")
	for repo, count := range snap.PollerDeferredScopeOverlap {
		writeCounter(w, "pilot_poller_deferred_scope_overlap_total", count, "repo", repo)
	}

	// --- Gauges ---

	// pilot_queue_depth
	writeHelp(w, "pilot_queue_depth", "Number of issues waiting in queue")
	writeType(w, "pilot_queue_depth", "gauge")
	writeGauge(w, "pilot_queue_depth", float64(snap.QueueDepth))

	// pilot_failed_queue_depth
	writeHelp(w, "pilot_failed_queue_depth", "Number of failed issues in queue")
	writeType(w, "pilot_failed_queue_depth", "gauge")
	writeGauge(w, "pilot_failed_queue_depth", float64(snap.FailedQueueDepth))

	// pilot_poller_unsourced_labeled_issues (GH-4488)
	writeHelp(w, "pilot_poller_unsourced_labeled_issues", "Open issues carrying the dispatch label that project board sourcing is not covering (absent from the board, or in a status other than source_status), by repo — nonzero means a board-sourced repo is silently ignoring labeled work")
	writeType(w, "pilot_poller_unsourced_labeled_issues", "gauge")
	for repo, count := range snap.UnsourcedLabeledIssues {
		writeGaugeLabeled(w, "pilot_poller_unsourced_labeled_issues", float64(count), "repo", repo)
	}

	// pilot_active_prs
	writeHelp(w, "pilot_active_prs", "Number of active PRs by stage")
	writeType(w, "pilot_active_prs", "gauge")
	for stage, count := range snap.ActivePRsByStage {
		writeGaugeLabeled(w, "pilot_active_prs", float64(count), "stage", string(stage))
	}
	// Emit zero for every defined stage not present in the snapshot so that
	// Prometheus's 5-min lookback does not hold stale non-zero values after a
	// stage transition. Mirrors the pattern at lines 41-46 (pilot_issues_processed_total)
	// and lines 88-92 (pilot_approval_persist_misses_total).
	for _, stage := range autopilot.AllPRStages() {
		if _, exists := snap.ActivePRsByStage[stage]; !exists {
			writeGaugeLabeled(w, "pilot_active_prs", 0, "stage", string(stage))
		}
	}

	// pilot_active_prs_total
	writeHelp(w, "pilot_active_prs_total", "Total number of active PRs")
	writeType(w, "pilot_active_prs_total", "gauge")
	writeGauge(w, "pilot_active_prs_total", float64(snap.TotalActivePRs))

	// pilot_api_error_rate
	writeHelp(w, "pilot_api_error_rate", "API errors per minute (5m window)")
	writeType(w, "pilot_api_error_rate", "gauge")
	writeGauge(w, "pilot_api_error_rate", snap.APIErrorRate)

	// pilot_success_rate
	writeHelp(w, "pilot_success_rate", "Per-attempt issue processing success rate (0-1), excluding rate_limited attempts. Retries inflate the denominator — prefer pilot_issue_level_success_rate for eventual-delivery health")
	writeType(w, "pilot_success_rate", "gauge")
	writeGauge(w, "pilot_success_rate", snap.SuccessRate)

	// pilot_issues_shipped_total / pilot_issues_attempted_total (TASK-392)
	writeHelp(w, "pilot_issues_shipped_total", "Number of unique issues that reached completed status, deduped by task_id across retry attempts")
	writeType(w, "pilot_issues_shipped_total", "gauge")
	writeGauge(w, "pilot_issues_shipped_total", float64(snap.IssuesShipped))

	writeHelp(w, "pilot_issues_attempted_total", "Number of unique issues attempted, deduped by task_id across retry attempts")
	writeType(w, "pilot_issues_attempted_total", "gauge")
	writeGauge(w, "pilot_issues_attempted_total", float64(snap.IssuesAttempted))

	// pilot_issue_level_success_rate
	writeHelp(w, "pilot_issue_level_success_rate", "Unique-issue success rate (0-1): pilot_issues_shipped_total / pilot_issues_attempted_total, deduped across retries")
	writeType(w, "pilot_issue_level_success_rate", "gauge")
	writeGauge(w, "pilot_issue_level_success_rate", snap.IssueLevelSuccessRate)

	// pilot_issues_shipped_by_model_total / pilot_issues_attempted_by_model_total
	// (GH-4483): per-model counterpart to pilot_issues_shipped_total /
	// pilot_issues_attempted_total above — deduped by task_id within each
	// model, so a task retried on the same model before shipping counts once,
	// not once per attempt. Pairs with pilot_executions_total{model,result},
	// which is per-attempt and dominated by mid-flight retries/rate-limit
	// deaths, to show eventual delivery per model.
	writeHelp(w, "pilot_issues_shipped_by_model_total", "Number of unique issues that reached completed status on a given model, deduped by task_id across retry attempts on that model")
	writeType(w, "pilot_issues_shipped_by_model_total", "gauge")
	for model, count := range snap.IssuesShippedByModel {
		writeGaugeLabeled(w, "pilot_issues_shipped_by_model_total", float64(count), "model", model)
	}

	writeHelp(w, "pilot_issues_attempted_by_model_total", "Number of unique issues attempted on a given model, deduped by task_id across retry attempts on that model")
	writeType(w, "pilot_issues_attempted_by_model_total", "gauge")
	for model, count := range snap.IssuesAttemptedByModel {
		writeGaugeLabeled(w, "pilot_issues_attempted_by_model_total", float64(count), "model", model)
	}

	// pilot_window_* (GH-4735): rolling-window headline cost/success gauges.
	// windowLabel carries the same day-count used to compute snap.Window* on
	// every one of the 4 gauges below, so they're always self-consistent even
	// if the configured window changes between refreshes. Values are refreshed
	// on a periodic ticker (StartWindowStatsRefresher), not per-scrape — see
	// that function's doc comment.
	windowLabel := fmt.Sprintf("%dd", snap.WindowDays)

	writeHelp(w, "pilot_window_cost_usd", "Total execution cost (USD) in the rolling window, canary-excluded, all executions including retries. Refreshed periodically (see window label for length), not per-scrape")
	writeType(w, "pilot_window_cost_usd", "gauge")
	writeGaugeLabeled(w, "pilot_window_cost_usd", snap.WindowCostUSD, "window", windowLabel)

	writeHelp(w, "pilot_window_cost_per_delivered_usd", "Rolling-window cost per delivered issue (window cost / distinct issues with a completed execution in the window); 0 when no issues delivered")
	writeType(w, "pilot_window_cost_per_delivered_usd", "gauge")
	writeGaugeLabeled(w, "pilot_window_cost_per_delivered_usd", snap.WindowCostPerDeliveredUSD, "window", windowLabel)

	writeHelp(w, "pilot_window_delivery_rate", "Rolling-window delivery rate (0-1): distinct issues delivered / distinct issues attempted in the window, canary-excluded; 0 when no issues attempted")
	writeType(w, "pilot_window_delivery_rate", "gauge")
	writeGaugeLabeled(w, "pilot_window_delivery_rate", snap.WindowDeliveryRate, "window", windowLabel)

	writeHelp(w, "pilot_window_attempt_success_rate", "Rolling-window per-attempt success rate (0-1): completed / (completed + failed) executions in the window. Neutral statuses (no_op, infra, skipped, declined, stalled, rate_limited) are excluded from both numerator and denominator; 0 when no completed/failed attempts")
	writeType(w, "pilot_window_attempt_success_rate", "gauge")
	writeGaugeLabeled(w, "pilot_window_attempt_success_rate", snap.WindowAttemptSuccessRate, "window", windowLabel)

	// --- Histograms ---

	// pilot_pr_time_to_merge_seconds
	writeHistogram(w, "pilot_pr_time_to_merge_seconds",
		"Time from PR creation to merge",
		hist.PRTimeToMerge,
		[]float64{60, 300, 600, 1800, 3600, 7200, 14400, 28800, 86400}) // 1m, 5m, 10m, 30m, 1h, 2h, 4h, 8h, 24h

	// pilot_execution_duration_seconds
	writeHistogram(w, "pilot_execution_duration_seconds",
		"Task execution duration",
		hist.ExecutionDurations,
		[]float64{10, 30, 60, 120, 300, 600, 1200, 1800, 3600}) // 10s, 30s, 1m, 2m, 5m, 10m, 20m, 30m, 1h

	// pilot_ci_wait_duration_seconds
	writeHistogram(w, "pilot_ci_wait_duration_seconds",
		"CI wait duration",
		hist.CIWaitDurations,
		[]float64{30, 60, 120, 300, 600, 900, 1200, 1800, 3600}) // 30s, 1m, 2m, 5m, 10m, 15m, 20m, 30m, 1h

	// pilot_time_to_pr_seconds (GH-4128 registered the histogram; GH-4130
	// observes it in autopilot.Controller.OnPRCreated)
	writeHistogram(w, "pilot_time_to_pr_seconds",
		"Time from issue pickup to PR creation",
		hist.TimeToPRDurations,
		[]float64{60, 300, 600, 1200, 1800, 3600, 7200, 14400, 28800}) // 1m, 5m, 10m, 20m, 30m, 1h, 2h, 4h, 8h

	// pilot_queue_wait_seconds (GH-4128 registered the histogram; GH-4130
	// observes it in autopilot.Controller.OnPRCreated)
	writeHistogram(w, "pilot_queue_wait_seconds",
		"Time an issue spent queued before pickup",
		hist.QueueWaitDurations,
		[]float64{60, 300, 600, 1800, 3600, 7200, 14400, 28800, 86400}) // 1m, 5m, 10m, 30m, 1h, 2h, 4h, 8h, 24h

	// pilot_approval_wait_seconds (GH-4128 registered the histogram; GH-4130
	// observes it in autopilot.Controller.applyApprovalDecision)
	writeHistogram(w, "pilot_approval_wait_seconds",
		"Time a PR spent awaiting human approval",
		hist.ApprovalWaitDurations,
		[]float64{60, 300, 600, 1800, 3600, 7200, 14400, 28800, 86400}) // 1m, 5m, 10m, 30m, 1h, 2h, 4h, 8h, 24h

	// pilot_panics_total — goroutine panics recovered by SafeGo, per component
	writeHelp(w, "pilot_panics_total", "Total goroutine panics recovered by SafeGo, by component")
	writeType(w, "pilot_panics_total", "counter")
	if e.panicCtr != nil {
		for component, count := range e.panicCtr.snapshot() {
			writeCounter(w, "pilot_panics_total", count, "component", component)
		}
	}

	// --- Alert metrics (optional; only emitted when an AlertMetricsSource is wired) ---
	if e.alertsSource != nil {
		asnap := e.alertsSource.AlertSnapshot()

		// alerts_fired_total{rule, severity}
		writeHelp(w, "alerts_fired_total", "Total alerts fired by rule and severity")
		writeType(w, "alerts_fired_total", "counter")
		for k, v := range asnap.FiredTotal {
			writeCounter(w, "alerts_fired_total", v, "rule", k.Rule, "severity", k.Severity)
		}

		// alert_delivery_total{channel, type, result}
		writeHelp(w, "alert_delivery_total", "Total alert delivery attempts by channel, type, and result")
		writeType(w, "alert_delivery_total", "counter")
		for k, v := range asnap.DeliveryTotal {
			writeCounter(w, "alert_delivery_total", v, "channel", k.Channel, "type", k.Type, "result", k.Result)
		}

		// alert_events_dropped_total
		writeHelp(w, "alert_events_dropped_total", "Total alert events dropped due to a full event queue")
		writeType(w, "alert_events_dropped_total", "counter")
		writeCounter(w, "alert_events_dropped_total", asnap.DroppedTotal)

		// alert_queue_depth
		writeHelp(w, "alert_queue_depth", "Current number of events waiting in the alert event queue")
		writeType(w, "alert_queue_depth", "gauge")
		writeGauge(w, "alert_queue_depth", float64(asnap.QueueDepth))
	}

	return nil
}

// writeHelp writes a HELP line for a metric.
func writeHelp(w io.Writer, name, help string) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n", name, help)
}

// writeType writes a TYPE line for a metric.
func writeType(w io.Writer, name, metricType string) {
	_, _ = fmt.Fprintf(w, "# TYPE %s %s\n", name, metricType)
}

// writeCounter writes a counter metric line.
func writeCounter(w io.Writer, name string, value int64, labelPairs ...string) {
	if len(labelPairs) == 0 {
		_, _ = fmt.Fprintf(w, "%s %d\n", name, value)
		return
	}
	labels := formatLabels(labelPairs)
	_, _ = fmt.Fprintf(w, "%s{%s} %d\n", name, labels, value)
}

// writeFloatCounter writes a counter metric line with a float64 value.
func writeFloatCounter(w io.Writer, name string, value float64, labelPairs ...string) {
	if len(labelPairs) == 0 {
		_, _ = fmt.Fprintf(w, "%s %g\n", name, value)
		return
	}
	labels := formatLabels(labelPairs)
	_, _ = fmt.Fprintf(w, "%s{%s} %g\n", name, labels, value)
}

// writeGauge writes a gauge metric line.
func writeGauge(w io.Writer, name string, value float64) {
	_, _ = fmt.Fprintf(w, "%s %g\n", name, value)
}

// writeGaugeLabeled writes a gauge metric with labels.
func writeGaugeLabeled(w io.Writer, name string, value float64, labelPairs ...string) {
	labels := formatLabels(labelPairs)
	_, _ = fmt.Fprintf(w, "%s{%s} %g\n", name, labels, value)
}

// writeHistogram writes a histogram metric with buckets.
func writeHistogram(w io.Writer, name, help string, samples []time.Duration, buckets []float64) {
	writeHelp(w, name, help)
	writeType(w, name, "histogram")

	// Convert samples to seconds
	seconds := make([]float64, len(samples))
	var sum float64
	for i, d := range samples {
		s := d.Seconds()
		seconds[i] = s
		sum += s
	}

	// Sort for bucket counting
	sort.Float64s(seconds)

	// Write bucket lines
	count := len(seconds)
	for _, bucket := range buckets {
		// Count samples <= bucket
		bucketCount := 0
		for _, s := range seconds {
			if s <= bucket {
				bucketCount++
			}
		}
		_, _ = fmt.Fprintf(w, "%s_bucket{le=\"%g\"} %d\n", name, bucket, bucketCount)
	}
	// +Inf bucket
	_, _ = fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n", name, count)

	// Sum and count
	_, _ = fmt.Fprintf(w, "%s_sum %g\n", name, sum)
	_, _ = fmt.Fprintf(w, "%s_count %d\n", name, count)
}

// formatLabels formats label key-value pairs for Prometheus output.
func formatLabels(pairs []string) string {
	if len(pairs) == 0 {
		return ""
	}
	result := ""
	for i := 0; i < len(pairs); i += 2 {
		if i > 0 {
			result += ","
		}
		key := pairs[i]
		value := ""
		if i+1 < len(pairs) {
			value = pairs[i+1]
		}
		result += fmt.Sprintf("%s=\"%s\"", key, escapeLabel(value))
	}
	return result
}

// escapeLabel escapes special characters in label values.
func escapeLabel(s string) string {
	result := ""
	for _, c := range s {
		switch c {
		case '\\':
			result += "\\\\"
		case '"':
			result += "\\\""
		case '\n':
			result += "\\n"
		default:
			result += string(c)
		}
	}
	return result
}
