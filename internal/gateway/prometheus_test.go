package gateway

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/autopilot"
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
				"# HELP pilot_queue_depth",
				"pilot_queue_depth 0",
				"# HELP pilot_pr_time_to_merge_seconds",
				"# TYPE pilot_pr_time_to_merge_seconds histogram",
				`pilot_pr_time_to_merge_seconds_bucket{le="+Inf"} 0`,
				"pilot_pr_time_to_merge_seconds_sum 0",
				"pilot_pr_time_to_merge_seconds_count 0",
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
					PRsMerged:           35,
					PRsFailed:           3,
					PRsConflicting:      2,
					CircuitBreakerTrips: 1,
					APIErrors: map[string]int64{
						"GetPR":   10,
						"MergePR": 2,
					},
					LabelCleanups: map[string]int64{
						"pilot-in-progress": 8,
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
				"pilot_prs_conflicting_total 2",
				"pilot_circuit_breaker_trips_total 1",
				`pilot_api_errors_total{endpoint="GetPR"} 10`,
				`pilot_api_errors_total{endpoint="MergePR"} 2`,
				`pilot_label_cleanups_total{label="pilot-in-progress"} 8`,
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
					TotalActivePRs:   4,
					QueueDepth:       7,
					FailedQueueDepth: 2,
					APIErrorRate:     1.5,
					SuccessRate:      0.85,
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
