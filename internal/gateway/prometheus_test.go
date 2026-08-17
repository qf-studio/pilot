package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/memory"
)

// mockMetricsSource implements MetricsSource for testing.
type mockMetricsSource struct {
	snapshot          autopilot.MetricsSnapshot
	histogramSnapshot autopilot.HistogramData
}

func (m *mockMetricsSource) Snapshot() autopilot.MetricsSnapshot {
	return m.snapshot
}

func (m *mockMetricsSource) HistogramSnapshot() autopilot.HistogramData {
	return m.histogramSnapshot
}

func TestPrometheusExporter_WritePrometheus(t *testing.T) {
	tests := []struct {
		name     string
		source   *mockMetricsSource
		contains []string
	}{
		{
			name: "empty metrics",
			source: &mockMetricsSource{
				snapshot: autopilot.MetricsSnapshot{
					IssuesProcessed:  make(map[string]int64),
					APIErrors:        make(map[string]int64),
					LabelCleanups:    make(map[string]int64),
					ActivePRsByStage: make(map[autopilot.PRStage]int),
				},
				histogramSnapshot: autopilot.HistogramData{},
			},
			contains: []string{
				"# HELP pilot_issues_processed_total",
				"# TYPE pilot_issues_processed_total counter",
				`pilot_issues_processed_total{result="success"} 0`,
				`pilot_issues_processed_total{result="failed"} 0`,
				"# HELP pilot_prs_merged_total",
				"pilot_prs_merged_total 0",
				// GH-4134: closed label set (pass/fail) emitted as zero even
				// before any CI verdict has been recorded.
				"# HELP pilot_ci_runs_total",
				"# TYPE pilot_ci_runs_total counter",
				`pilot_ci_runs_total{result="pass"} 0`,
				`pilot_ci_runs_total{result="fail"} 0`,
				"# HELP pilot_queue_depth",
				"pilot_queue_depth 0",
				"# HELP pilot_pr_time_to_merge_seconds",
				"# TYPE pilot_pr_time_to_merge_seconds histogram",
				`pilot_pr_time_to_merge_seconds_bucket{le="+Inf"} 0`,
				"pilot_pr_time_to_merge_seconds_sum 0",
				"pilot_pr_time_to_merge_seconds_count 0",
				// GH-4128: registered-but-empty series ahead of any Record* call site.
				"# HELP pilot_time_to_pr_seconds",
				"# TYPE pilot_time_to_pr_seconds histogram",
				`pilot_time_to_pr_seconds_bucket{le="+Inf"} 0`,
				"pilot_time_to_pr_seconds_sum 0",
				"pilot_time_to_pr_seconds_count 0",
				"# HELP pilot_queue_wait_seconds",
				"# TYPE pilot_queue_wait_seconds histogram",
				`pilot_queue_wait_seconds_bucket{le="+Inf"} 0`,
				"pilot_queue_wait_seconds_sum 0",
				"pilot_queue_wait_seconds_count 0",
				"# HELP pilot_approval_wait_seconds",
				"# TYPE pilot_approval_wait_seconds histogram",
				`pilot_approval_wait_seconds_bucket{le="+Inf"} 0`,
				"pilot_approval_wait_seconds_sum 0",
				"pilot_approval_wait_seconds_count 0",
			},
		},
		{
			name: "populated counters",
			source: &mockMetricsSource{
				snapshot: autopilot.MetricsSnapshot{
					IssuesProcessed: map[string]int64{
						"success": 42,
						"failed":  5,
					},
					PRsMerged:         35,
					PRsFailed:         3,
					PRsMergedLifetime: 135,
					PRsFailedLifetime: 13,
					PRsConflicting:    2,
					CIRuns: map[string]int64{
						"pass": 30,
						"fail": 4,
					},
					CircuitBreakerTrips: 1,
					APIErrors: map[string]int64{
						"GetPR":   10,
						"MergePR": 2,
					},
					LabelCleanups: map[string]int64{
						"pilot-in-progress": 8,
					},
					IntentJudgeFailures: map[string]int64{
						"context_deadline": 6,
						"external_sigkill": 1,
					},
					ActivePRsByStage: make(map[autopilot.PRStage]int),
				},
				histogramSnapshot: autopilot.HistogramData{},
			},
			contains: []string{
				`pilot_issues_processed_total{result="success"} 42`,
				`pilot_issues_processed_total{result="failed"} 5`,
				"pilot_prs_merged_total 35",
				"pilot_prs_failed_total 3",
				// GH-4511: lifetime gauges are the all-time counterpart to the
				// session-only counters above.
				"# HELP pilot_prs_merged_lifetime",
				"# TYPE pilot_prs_merged_lifetime gauge",
				"pilot_prs_merged_lifetime 135",
				"# HELP pilot_prs_failed_lifetime",
				"# TYPE pilot_prs_failed_lifetime gauge",
				"pilot_prs_failed_lifetime 13",
				"pilot_prs_conflicting_total 2",
				`pilot_ci_runs_total{result="pass"} 30`,
				`pilot_ci_runs_total{result="fail"} 4`,
				"pilot_circuit_breaker_trips_total 1",
				`pilot_api_errors_total{endpoint="GetPR"} 10`,
				`pilot_api_errors_total{endpoint="MergePR"} 2`,
				`pilot_label_cleanups_total{label="pilot-in-progress"} 8`,
				// GH-4377: judge failures broken out by cause.
				"# HELP pilot_intent_judge_failures_total",
				"# TYPE pilot_intent_judge_failures_total counter",
				`pilot_intent_judge_failures_total{cause="context_deadline"} 6`,
				`pilot_intent_judge_failures_total{cause="external_sigkill"} 1`,
			},
		},
		{
			name: "populated gauges",
			source: &mockMetricsSource{
				snapshot: autopilot.MetricsSnapshot{
					IssuesProcessed: make(map[string]int64),
					APIErrors:       make(map[string]int64),
					LabelCleanups:   make(map[string]int64),
					ActivePRsByStage: map[autopilot.PRStage]int{
						autopilot.StageWaitingCI: 3,
						autopilot.StageMerging:   1,
					},
					TotalActivePRs:        4,
					QueueDepth:            7,
					FailedQueueDepth:      2,
					APIErrorRate:          1.5,
					SuccessRate:           0.85,
					IssuesShipped:         9,
					IssuesAttempted:       10,
					IssueLevelSuccessRate: 0.9,
					// GH-4735: rolling-window headline cost/success gauges.
					WindowDays:                30,
					WindowCostUSD:             12.5,
					WindowCostPerDeliveredUSD: 2.5,
					WindowDeliveryRate:        0.8,
					WindowAttemptSuccessRate:  0.75,
				},
				histogramSnapshot: autopilot.HistogramData{},
			},
			contains: []string{
				"pilot_queue_depth 7",
				"pilot_failed_queue_depth 2",
				`pilot_active_prs{stage="waiting_ci"} 3`,
				`pilot_active_prs{stage="merging"} 1`,
				"pilot_active_prs_total 4",
				"pilot_api_error_rate 1.5",
				"pilot_success_rate 0.85",
				"pilot_issues_shipped_total 9",
				"pilot_issues_attempted_total 10",
				"pilot_issue_level_success_rate 0.9",
				// GH-4735: window gauges, each labeled window="30d".
				"# HELP pilot_window_cost_usd",
				"# TYPE pilot_window_cost_usd gauge",
				`pilot_window_cost_usd{window="30d"} 12.5`,
				"# HELP pilot_window_cost_per_delivered_usd",
				"# TYPE pilot_window_cost_per_delivered_usd gauge",
				`pilot_window_cost_per_delivered_usd{window="30d"} 2.5`,
				"# HELP pilot_window_delivery_rate",
				"# TYPE pilot_window_delivery_rate gauge",
				`pilot_window_delivery_rate{window="30d"} 0.8`,
				"# HELP pilot_window_attempt_success_rate",
				"# TYPE pilot_window_attempt_success_rate gauge",
				`pilot_window_attempt_success_rate{window="30d"} 0.75`,
			},
		},
		{
			name: "absent stages emitted as zero",
			source: &mockMetricsSource{
				snapshot: autopilot.MetricsSnapshot{
					IssuesProcessed: make(map[string]int64),
					APIErrors:       make(map[string]int64),
					LabelCleanups:   make(map[string]int64),
					ActivePRsByStage: map[autopilot.PRStage]int{
						autopilot.StageCIPassed: 1,
					},
				},
				histogramSnapshot: autopilot.HistogramData{},
			},
			contains: []string{
				`pilot_active_prs{stage="ci_passed"} 1`,
				`pilot_active_prs{stage="waiting_ci"} 0`,
				`pilot_active_prs{stage="merging"} 0`,
				`pilot_active_prs{stage="merged"} 0`,
			},
		},
		{
			name: "histogram with samples",
			source: &mockMetricsSource{
				snapshot: autopilot.MetricsSnapshot{
					IssuesProcessed:  make(map[string]int64),
					APIErrors:        make(map[string]int64),
					LabelCleanups:    make(map[string]int64),
					ActivePRsByStage: make(map[autopilot.PRStage]int),
				},
				histogramSnapshot: autopilot.HistogramData{
					PRTimeToMerge: []time.Duration{
						30 * time.Second,  // in 60s bucket
						90 * time.Second,  // in 300s bucket
						400 * time.Second, // in 600s bucket
						700 * time.Second, // in 1800s bucket
					},
					ExecutionDurations: []time.Duration{
						5 * time.Second,
						25 * time.Second,
						45 * time.Second,
					},
					CIWaitDurations: []time.Duration{
						120 * time.Second,
					},
				},
			},
			contains: []string{
				// PR time to merge histogram
				`pilot_pr_time_to_merge_seconds_bucket{le="60"} 1`,
				`pilot_pr_time_to_merge_seconds_bucket{le="300"} 2`,
				`pilot_pr_time_to_merge_seconds_bucket{le="600"} 3`,
				`pilot_pr_time_to_merge_seconds_bucket{le="1800"} 4`,
				`pilot_pr_time_to_merge_seconds_bucket{le="+Inf"} 4`,
				"pilot_pr_time_to_merge_seconds_count 4",
				// Execution duration histogram
				`pilot_execution_duration_seconds_bucket{le="10"} 1`,
				`pilot_execution_duration_seconds_bucket{le="30"} 2`,
				`pilot_execution_duration_seconds_bucket{le="60"} 3`,
				"pilot_execution_duration_seconds_count 3",
				// CI wait histogram
				"pilot_ci_wait_duration_seconds_count 1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter := NewPrometheusExporter(tt.source)
			var buf bytes.Buffer

			err := exporter.WritePrometheus(&buf)
			if err != nil {
				t.Fatalf("WritePrometheus() error = %v", err)
			}

			output := buf.String()
			for _, want := range tt.contains {
				if !strings.Contains(output, want) {
					t.Errorf("Output missing expected string: %q\nGot:\n%s", want, output)
				}
			}
		})
	}
}

func TestPrometheusExporter_TokenCostExecutionMetrics(t *testing.T) {
	m := autopilot.NewMetrics()
	m.RecordTokens("claude-sonnet-4-5", "input", 1000)
	m.RecordTokens("claude-sonnet-4-5", "output", 250)
	m.RecordCost("claude-sonnet-4-5", 0.005)
	m.RecordExecution("claude-sonnet-4-5", "success")
	m.RecordExecution("claude-sonnet-4-5", "failed")

	exporter := NewPrometheusExporter(m)
	var buf bytes.Buffer
	if err := exporter.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus() error = %v", err)
	}
	output := buf.String()

	for _, want := range []string{
		"# HELP pilot_tokens_consumed_total Total tokens consumed by model and direction",
		"# TYPE pilot_tokens_consumed_total counter",
		`pilot_tokens_consumed_total{model="claude-sonnet-4-5",direction="input"} 1000`,
		`pilot_tokens_consumed_total{model="claude-sonnet-4-5",direction="output"} 250`,
		"# HELP pilot_execution_cost_usd_total Total execution cost in USD by model",
		"# TYPE pilot_execution_cost_usd_total counter",
		`pilot_execution_cost_usd_total{model="claude-sonnet-4-5"} 0.005`,
		"# HELP pilot_executions_total Total executions by model and result",
		"# TYPE pilot_executions_total counter",
		`pilot_executions_total{model="claude-sonnet-4-5",result="success"} 1`,
		`pilot_executions_total{model="claude-sonnet-4-5",result="failed"} 1`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("Output missing expected string: %q\nGot:\n%s", want, output)
		}
	}
}

// TestPrometheusExporter_CIRunsCounter verifies pilot_ci_runs_total{result}
// reflects RecordCIRun calls on a real *autopilot.Metrics, and that the CI
// pass rate is computable directly from the exported series without the
// pilot_prs_failed_total proxy (GH-4134 acceptance criterion).
func TestPrometheusExporter_CIRunsCounter(t *testing.T) {
	m := autopilot.NewMetrics()
	m.RecordCIRun("pass")
	m.RecordCIRun("pass")
	m.RecordCIRun("fail")

	exporter := NewPrometheusExporter(m)
	var buf bytes.Buffer
	if err := exporter.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus() error = %v", err)
	}
	output := buf.String()

	for _, want := range []string{
		"# HELP pilot_ci_runs_total",
		"# TYPE pilot_ci_runs_total counter",
		`pilot_ci_runs_total{result="pass"} 2`,
		`pilot_ci_runs_total{result="fail"} 1`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("Output missing expected string: %q\nGot:\n%s", want, output)
		}
	}
}

// TestPrometheusExporter_WindowStatsSurviveComposedDaemonWiring is GH-4738's
// regression test. It reproduces the box symptom — pilot_window_* serving
// window="0d" value 0 despite a clean seed and no logged error — by wiring
// the exact same pieces cmd/pilot/main.go's runPollingMode composes at boot,
// instead of testing HydrateWindowStats or AggregateMetrics in isolation
// (both of those unit tests were green while production served zeros, which
// is the gap this test closes):
//
//  1. A real store with an execution in the window.
//  2. autopilot.HydrateWindowStats seeds the DEFAULT controller's *Metrics
//     only (main.go never seeds any other project controller's Metrics).
//  3. A second, unrelated per-project *Metrics never gets window-seeded —
//     mirrors autopilotControllers always containing more than just the
//     default in a multi-project config.
//  4. Both are wrapped in autopilot.NewAggregateMetrics, exactly as
//     autopilotMetricsAggregate is built in main.go.
//  5. gwServer.SetMetricsSource(aggregate) is exactly what wires the
//     aggregate into this package's PrometheusExporter.
//
// Before the GH-4738 fix, step 4's aggregate silently dropped the seeded
// values from step 2, so this test's WritePrometheus output would show
// window="0d" 0 despite the store holding real data.
func TestPrometheusExporter_WindowStatsSurviveComposedDaemonWiring(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&memory.Execution{
		ID: "gh4738-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed",
		CreatedAt: time.Now().UTC(), EstimatedCostUSD: 4.0,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	// Step 2: seed only the default controller's Metrics, as main.go does.
	defaultMetrics := autopilot.NewMetrics()
	if err := autopilot.HydrateWindowStats(store, defaultMetrics, 30); err != nil {
		t.Fatalf("HydrateWindowStats: %v", err)
	}

	// Step 3: a second controller's Metrics that never gets window-seeded.
	projectMetrics := autopilot.NewMetrics()

	// Step 4: the same aggregate main.go builds from fleetMetrics.
	aggregate := autopilot.NewAggregateMetrics(defaultMetrics, projectMetrics)

	// Step 5: the same wiring SetMetricsSource performs.
	srv := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	srv.SetMetricsSource(aggregate)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.handleMetrics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()

	if strings.Contains(body, `pilot_window_cost_usd{window="0d"}`) {
		t.Errorf("regression: pilot_window_cost_usd served window=\"0d\" through the composed daemon wiring:\n%s", body)
	}
	for _, want := range []string{
		`pilot_window_cost_usd{window="30d"} 4`,
		`pilot_window_delivery_rate{window="30d"} 1`,
		`pilot_window_attempt_success_rate{window="30d"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected composed daemon wiring output to contain %q\ngot:\n%s", want, body)
		}
	}
}

// TestPrometheusExporter_IssuesProcessedCounter verifies that a real *autopilot.Metrics
// wired to the exporter surfaces pilot_issues_processed_total, pilot_execution_duration_seconds_count,
// and pilot_success_rate with correct non-zero values after recording one success + one duration.
func TestPrometheusExporter_IssuesProcessedCounter(t *testing.T) {
	m := autopilot.NewMetrics()
	m.RecordIssueProcessed("success")
	m.RecordExecutionDuration(30 * time.Second)

	exporter := NewPrometheusExporter(m)
	var buf bytes.Buffer
	if err := exporter.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus() error = %v", err)
	}
	output := buf.String()

	for _, want := range []string{
		`pilot_issues_processed_total{result="success"} 1`,
		"pilot_execution_duration_seconds_count 1",
		"pilot_success_rate 1",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("Output missing expected string: %q\nGot:\n%s", want, output)
		}
	}
}

func TestEscapeLabel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"with\\backslash", "with\\\\backslash"},
		{`with"quote`, `with\"quote`},
		{"with\nnewline", "with\\nnewline"},
		{`complex\n"test`, `complex\\n\"test`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeLabel(tt.input)
			if got != tt.expected {
				t.Errorf("escapeLabel(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestFormatLabels(t *testing.T) {
	tests := []struct {
		name     string
		pairs    []string
		expected string
	}{
		{"empty", []string{}, ""},
		{"single", []string{"key", "value"}, `key="value"`},
		{"multiple", []string{"a", "1", "b", "2"}, `a="1",b="2"`},
		{"with special chars", []string{"key", `val"ue`}, `key="val\"ue"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatLabels(tt.pairs)
			if got != tt.expected {
				t.Errorf("formatLabels(%v) = %q, want %q", tt.pairs, got, tt.expected)
			}
		})
	}
}

// TestCommitFromVersion covers GH-4864's commit extraction from a
// git-describe-style version string — the same VERSION computation the
// Makefile uses to stamp main.version, so there's no separate commit ldflag
// to read from.
func TestCommitFromVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"dev build with commits since tag", "v2.259.1-29-g35450fea", "35450fea"},
		{"dirty dev build", "v2.259.1-29-g35450fea-dirty", "35450fea-dirty"},
		{"full 40-char hash", "v2.259.1-1-g" + strings.Repeat("a1b2c3d4", 5), strings.Repeat("a1b2c3d4", 5)},
		{"clean tag build", "v2.259.1", "unknown"},
		{"no-ldflags fallback", "1.0.0", "unknown"},
		{"dev fallback (git describe failed)", "dev", "unknown"},
		{"empty", "", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commitFromVersion(tt.version)
			if got != tt.want {
				t.Errorf("commitFromVersion(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

// TestPrometheusExporter_BuildInfo verifies pilot_build_info is registered
// and labeled from SetBuildInfo (GH-4864) — the metric that changes across a
// hot restart (syscall.Exec) with no PID change, unlike ps-based uptime.
func TestPrometheusExporter_BuildInfo(t *testing.T) {
	source := &mockMetricsSource{
		snapshot: autopilot.MetricsSnapshot{
			IssuesProcessed:  make(map[string]int64),
			APIErrors:        make(map[string]int64),
			LabelCleanups:    make(map[string]int64),
			ActivePRsByStage: make(map[autopilot.PRStage]int),
		},
	}
	exporter := NewPrometheusExporter(source)
	exporter.SetBuildInfo("v2.259.1-29-g35450fea", "35450fea")

	var buf bytes.Buffer
	if err := exporter.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus failed: %v", err)
	}
	output := buf.String()

	for _, want := range []string{
		"# HELP pilot_build_info",
		"# TYPE pilot_build_info gauge",
		`pilot_build_info{version="v2.259.1-29-g35450fea",commit="35450fea"} 1`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("Output missing expected string: %q\nGot:\n%s", want, output)
		}
	}
}

// TestMetricsEndpointWiring verifies that /metrics returns Prometheus text when
// SetMetricsSource is called, and the fallback response when it is not.
func TestMetricsEndpointWiring(t *testing.T) {
	t.Run("with metrics source returns 200 and pilot counters", func(t *testing.T) {
		srv := NewServer(&Config{Host: "127.0.0.1", Port: 0})
		m := autopilot.NewMetrics()
		m.RecordIssueProcessed("success")
		srv.SetMetricsSource(m)

		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		w := httptest.NewRecorder()
		srv.handleMetrics(w, req)

		resp := w.Result()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		body := w.Body.String()
		for _, want := range []string{
			"pilot_issues_processed_total",
			"pilot_prs_merged_total",
			"pilot_prs_failed_total",
			"pilot_prs_conflicting_total",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("body missing %q", want)
			}
		}
	})

	t.Run("without metrics source returns 503 fallback", func(t *testing.T) {
		srv := NewServer(&Config{Host: "127.0.0.1", Port: 0})

		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		w := httptest.NewRecorder()
		srv.handleMetrics(w, req)

		resp := w.Result()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", resp.StatusCode)
		}
		if !strings.Contains(w.Body.String(), "Metrics not configured") {
			t.Errorf("expected 'Metrics not configured' in body, got: %s", w.Body.String())
		}
	})
}

// =============================================================================
// Alert metrics tests (TASK-332)
// =============================================================================

// mockAlertSource implements AlertMetricsSource for testing.
type mockAlertSource struct {
	snap alerts.AlertMetricsSnapshot
}

func (s *mockAlertSource) AlertSnapshot() alerts.AlertMetricsSnapshot {
	return s.snap
}

func TestPrometheusExporter_AlertMetrics_NotEmittedWhenNilSource(t *testing.T) {
	exp := NewPrometheusExporter(&mockMetricsSource{})
	var buf bytes.Buffer
	if err := exp.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus error: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "alerts_fired_total") {
		t.Error("alert series should not appear when no AlertMetricsSource is wired")
	}
}

func TestPrometheusExporter_AlertMetrics_SeriesRendered(t *testing.T) {
	m := alerts.NewAlertMetrics()
	m.RecordFired("task-failed-rule", "critical")
	m.RecordFired("task-failed-rule", "critical")
	m.RecordDelivery("slack-ops", "slack", "success")
	m.RecordDelivery("slack-ops", "slack", "failure")
	m.RecordDropped()

	snap := m.Snapshot()
	snap.QueueDepth = 7

	exp := NewPrometheusExporter(&mockMetricsSource{})
	exp.SetAlertsSource(&mockAlertSource{snap: snap})

	var buf bytes.Buffer
	if err := exp.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus error: %v", err)
	}
	out := buf.String()

	expectations := []string{
		`# HELP alerts_fired_total`,
		`# TYPE alerts_fired_total counter`,
		`alerts_fired_total{rule="task-failed-rule",severity="critical"} 2`,
		`# HELP alert_delivery_total`,
		`# TYPE alert_delivery_total counter`,
		`alert_delivery_total{channel="slack-ops",type="slack",result="success"} 1`,
		`alert_delivery_total{channel="slack-ops",type="slack",result="failure"} 1`,
		`# HELP alert_events_dropped_total`,
		`# TYPE alert_events_dropped_total counter`,
		`alert_events_dropped_total 1`,
		`# HELP alert_queue_depth`,
		`# TYPE alert_queue_depth gauge`,
		`alert_queue_depth 7`,
	}
	for _, want := range expectations {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q\ngot:\n%s", want, out)
		}
	}
}

func TestPrometheusExporter_AlertMetrics_SetAlertsSourceViaServer(t *testing.T) {
	srv := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	srv.SetMetricsSource(&mockMetricsSource{})

	m := alerts.NewAlertMetrics()
	m.RecordFired("stuck-task", "warning")
	snap := m.Snapshot()

	srv.SetAlertsMetricsSource(&mockAlertSource{snap: snap})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.handleMetrics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `alerts_fired_total{rule="stuck-task",severity="warning"} 1`) {
		t.Errorf("alert series not found in output:\n%s", w.Body.String())
	}
}

func TestPrometheusExporter_AlertMetrics_SetAlertsSourceBeforeMetricsSource(t *testing.T) {
	// Verify SetAlertsMetricsSource is safe to call before SetMetricsSource
	srv := NewServer(&Config{Host: "127.0.0.1", Port: 0})

	m := alerts.NewAlertMetrics()
	m.RecordDropped()
	snap := m.Snapshot()

	srv.SetAlertsMetricsSource(&mockAlertSource{snap: snap})
	srv.SetMetricsSource(&mockMetricsSource{}) // exporter created after alertsSource is stored

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.handleMetrics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "alert_events_dropped_total 1") {
		t.Errorf("alert_events_dropped_total not found:\n%s", w.Body.String())
	}
}

// =============================================================================
// Eval metrics tests (GH-4922): pilot_eval_tasks_total / pilot_eval_pass_ratio
// replaced the TUI eval stats panel (renderEvalStats, removed).
// =============================================================================

// mockEvalSource implements EvalMetricsSource for testing.
type mockEvalSource struct {
	counts memory.EvalTaskCounts
	err    error
}

func (s *mockEvalSource) GetEvalTaskCounts() (memory.EvalTaskCounts, error) {
	return s.counts, s.err
}

func TestPrometheusExporter_EvalMetrics_NotEmittedWhenNilSource(t *testing.T) {
	exp := NewPrometheusExporter(&mockMetricsSource{})
	var buf bytes.Buffer
	if err := exp.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus error: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "pilot_eval_tasks_total") || strings.Contains(out, "pilot_eval_pass_ratio") {
		t.Error("eval series should not appear when no EvalMetricsSource is wired")
	}
}

func TestPrometheusExporter_EvalMetrics_SeriesRendered(t *testing.T) {
	exp := NewPrometheusExporter(&mockMetricsSource{})
	exp.SetEvalSource(&mockEvalSource{counts: memory.EvalTaskCounts{Passed: 7, Failed: 3}})

	var buf bytes.Buffer
	if err := exp.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus error: %v", err)
	}
	out := buf.String()

	expectations := []string{
		`# HELP pilot_eval_tasks_total`,
		`# TYPE pilot_eval_tasks_total gauge`,
		`pilot_eval_tasks_total{success="true"} 7`,
		`pilot_eval_tasks_total{success="false"} 3`,
		`# HELP pilot_eval_pass_ratio`,
		`# TYPE pilot_eval_pass_ratio gauge`,
		`pilot_eval_pass_ratio 0.7`,
	}
	for _, want := range expectations {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q\ngot:\n%s", want, out)
		}
	}
}

func TestPrometheusExporter_EvalMetrics_EmptyTableIsDeterministic(t *testing.T) {
	exp := NewPrometheusExporter(&mockMetricsSource{})
	exp.SetEvalSource(&mockEvalSource{counts: memory.EvalTaskCounts{Passed: 0, Failed: 0}})

	var buf bytes.Buffer
	if err := exp.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus error: %v", err)
	}
	out := buf.String()

	expectations := []string{
		`pilot_eval_tasks_total{success="true"} 0`,
		`pilot_eval_tasks_total{success="false"} 0`,
		`pilot_eval_pass_ratio 0`,
	}
	for _, want := range expectations {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q\ngot:\n%s", want, out)
		}
	}
}

func TestPrometheusExporter_EvalMetrics_SetEvalMetricsSourceViaServer(t *testing.T) {
	srv := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	srv.SetMetricsSource(&mockMetricsSource{})
	srv.SetEvalMetricsSource(&mockEvalSource{counts: memory.EvalTaskCounts{Passed: 1, Failed: 1}})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.handleMetrics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `pilot_eval_pass_ratio 0.5`) {
		t.Errorf("eval pass ratio not found in output:\n%s", w.Body.String())
	}
}

func TestPrometheusExporter_EvalMetrics_SetEvalMetricsSourceBeforeMetricsSource(t *testing.T) {
	// Verify SetEvalMetricsSource is safe to call before SetMetricsSource,
	// mirroring the alerts-source ordering guarantee above.
	srv := NewServer(&Config{Host: "127.0.0.1", Port: 0})

	srv.SetEvalMetricsSource(&mockEvalSource{counts: memory.EvalTaskCounts{Passed: 2, Failed: 0}})
	srv.SetMetricsSource(&mockMetricsSource{}) // exporter created after evalSource is stored

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.handleMetrics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `pilot_eval_pass_ratio 1`) {
		t.Errorf("pilot_eval_pass_ratio not found:\n%s", w.Body.String())
	}
}
