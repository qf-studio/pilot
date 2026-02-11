package gateway

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/alekspetrov/pilot/internal/autopilot"
)

// PrometheusExporter formats metrics for Prometheus scraping.
type PrometheusExporter struct {
	metricsSource MetricsSource
}

// MetricsSource provides metrics data for the exporter.
type MetricsSource interface {
	Snapshot() autopilot.MetricsSnapshot
	HistogramSnapshot() autopilot.HistogramData
}

// NewPrometheusExporter creates a new Prometheus exporter.
func NewPrometheusExporter(source MetricsSource) *PrometheusExporter {
	return &PrometheusExporter{metricsSource: source}
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

	// pilot_prs_merged_total
	writeHelp(w, "pilot_prs_merged_total", "Total PRs successfully merged")
	writeType(w, "pilot_prs_merged_total", "counter")
	writeCounter(w, "pilot_prs_merged_total", snap.PRsMerged)

	// pilot_prs_failed_total
	writeHelp(w, "pilot_prs_failed_total", "Total PRs that failed")
	writeType(w, "pilot_prs_failed_total", "counter")
	writeCounter(w, "pilot_prs_failed_total", snap.PRsFailed)

	// pilot_prs_conflicting_total
	writeHelp(w, "pilot_prs_conflicting_total", "Total PRs with merge conflicts")
	writeType(w, "pilot_prs_conflicting_total", "counter")
	writeCounter(w, "pilot_prs_conflicting_total", snap.PRsConflicting)

	// pilot_circuit_breaker_trips_total
	writeHelp(w, "pilot_circuit_breaker_trips_total", "Total circuit breaker trips")
	writeType(w, "pilot_circuit_breaker_trips_total", "counter")
	writeCounter(w, "pilot_circuit_breaker_trips_total", snap.CircuitBreakerTrips)

	// pilot_api_errors_total
	writeHelp(w, "pilot_api_errors_total", "Total API errors by endpoint")
	writeType(w, "pilot_api_errors_total", "counter")
	for endpoint, count := range snap.APIErrors {
		writeCounter(w, "pilot_api_errors_total", count, "endpoint", endpoint)
	}

	// pilot_label_cleanups_total
	writeHelp(w, "pilot_label_cleanups_total", "Total label cleanup operations")
	writeType(w, "pilot_label_cleanups_total", "counter")
	for label, count := range snap.LabelCleanups {
		writeCounter(w, "pilot_label_cleanups_total", count, "label", label)
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

	// pilot_active_prs
	writeHelp(w, "pilot_active_prs", "Number of active PRs by stage")
	writeType(w, "pilot_active_prs", "gauge")
	for stage, count := range snap.ActivePRsByStage {
		writeGaugeLabeled(w, "pilot_active_prs", float64(count), "stage", string(stage))
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
	writeHelp(w, "pilot_success_rate", "Issue processing success rate (0-1)")
	writeType(w, "pilot_success_rate", "gauge")
	writeGauge(w, "pilot_success_rate", snap.SuccessRate)

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
