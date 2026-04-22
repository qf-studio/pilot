package observability

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Meter and Tracer names used across the codebase. Kept as constants so span
// names and metric scopes stay consistent across packages.
const (
	meterName  = "pilot"
	tracerName = "pilot"
)

// Tracer returns the Pilot-scoped tracer. Uses the global provider, so it
// returns a no-op tracer when OTel is disabled.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// Meter returns the Pilot-scoped meter. Uses the global provider, so metric
// instruments it returns are no-ops when OTel is disabled.
func Meter() metric.Meter {
	return otel.Meter(meterName)
}

// StartSpan is a small convenience wrapper around tracer.Start that also
// returns a finisher closure. Usage:
//
//	ctx, end := observability.StartSpan(ctx, "task.execute")
//	defer end(err)
//
// The finisher records err on the span (if non-nil) and ends it.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(err error)) {
	ctx, span := Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// Lazily-initialized instrument cache. All instruments below are safe to call
// even when the global MeterProvider is the no-op default — the returned
// instruments are themselves no-ops in that case.
var (
	instrumentsOnce sync.Once
	metricsSet      *pilotMetrics
)

type pilotMetrics struct {
	taskCount         metric.Int64Counter
	taskDuration      metric.Float64Histogram
	taskPhaseDuration metric.Float64Histogram
	subprocessDur     metric.Float64Histogram
	tokensInput       metric.Int64Histogram
	tokensOutput      metric.Int64Histogram
	ciWaitDuration    metric.Float64Histogram
	autoMergeCount    metric.Int64Counter
}

func metrics() *pilotMetrics {
	instrumentsOnce.Do(func() {
		m := Meter()
		ms := &pilotMetrics{}
		var err error
		if ms.taskCount, err = m.Int64Counter("pilot.task.count", metric.WithDescription("Number of tasks processed")); err != nil {
			slog.Warn("otel: metric init failed", "name", "pilot.task.count", "error", err)
		}
		if ms.taskDuration, err = m.Float64Histogram("pilot.task.duration", metric.WithUnit("s"), metric.WithDescription("End-to-end task duration")); err != nil {
			slog.Warn("otel: metric init failed", "name", "pilot.task.duration", "error", err)
		}
		if ms.taskPhaseDuration, err = m.Float64Histogram("pilot.task.phase.duration", metric.WithUnit("s"), metric.WithDescription("Per-phase task duration")); err != nil {
			slog.Warn("otel: metric init failed", "name", "pilot.task.phase.duration", "error", err)
		}
		if ms.subprocessDur, err = m.Float64Histogram("pilot.subprocess.duration", metric.WithUnit("s"), metric.WithDescription("Claude Code subprocess duration")); err != nil {
			slog.Warn("otel: metric init failed", "name", "pilot.subprocess.duration", "error", err)
		}
		if ms.tokensInput, err = m.Int64Histogram("pilot.tokens.input", metric.WithDescription("Input tokens per task")); err != nil {
			slog.Warn("otel: metric init failed", "name", "pilot.tokens.input", "error", err)
		}
		if ms.tokensOutput, err = m.Int64Histogram("pilot.tokens.output", metric.WithDescription("Output tokens per task")); err != nil {
			slog.Warn("otel: metric init failed", "name", "pilot.tokens.output", "error", err)
		}
		if ms.ciWaitDuration, err = m.Float64Histogram("pilot.ci.wait_duration", metric.WithUnit("s"), metric.WithDescription("Time spent waiting for CI to complete")); err != nil {
			slog.Warn("otel: metric init failed", "name", "pilot.ci.wait_duration", "error", err)
		}
		if ms.autoMergeCount, err = m.Int64Counter("pilot.automerge.count", metric.WithDescription("Auto-merge decisions")); err != nil {
			slog.Warn("otel: metric init failed", "name", "pilot.automerge.count", "error", err)
		}
		metricsSet = ms
	})
	return metricsSet
}

// RecordTask records a completed task's count and duration with a result attr.
func RecordTask(ctx context.Context, project, result string, d time.Duration) {
	m := metrics()
	attrs := metric.WithAttributes(attribute.String("project", project), attribute.String("result", result))
	if m.taskCount != nil {
		m.taskCount.Add(ctx, 1, attrs)
	}
	if m.taskDuration != nil {
		m.taskDuration.Record(ctx, d.Seconds(), attrs)
	}
}

// RecordTaskPhase records the duration of a named phase (worktree, prompt, backend, commit, pr).
func RecordTaskPhase(ctx context.Context, phase string, d time.Duration) {
	m := metrics()
	if m.taskPhaseDuration != nil {
		m.taskPhaseDuration.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("phase", phase)))
	}
}

// RecordSubprocess records the duration of the backend (Claude Code/opencode) subprocess.
func RecordSubprocess(ctx context.Context, backend string, d time.Duration) {
	m := metrics()
	if m.subprocessDur != nil {
		m.subprocessDur.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("backend", backend)))
	}
}

// RecordTokens records input and output tokens for a task, labeled by model.
func RecordTokens(ctx context.Context, model string, input, output int64) {
	m := metrics()
	attrs := metric.WithAttributes(attribute.String("model", model))
	if input > 0 && m.tokensInput != nil {
		m.tokensInput.Record(ctx, input, attrs)
	}
	if output > 0 && m.tokensOutput != nil {
		m.tokensOutput.Record(ctx, output, attrs)
	}
}

// RecordCIWait records how long autopilot waited for CI, labeled by result.
func RecordCIWait(ctx context.Context, result string, d time.Duration) {
	m := metrics()
	if m.ciWaitDuration != nil {
		m.ciWaitDuration.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("result", result)))
	}
}

// RecordAutoMerge records an auto-merge decision outcome (merged/rejected/ci_failed/conflict).
func RecordAutoMerge(ctx context.Context, result string) {
	m := metrics()
	if m.autoMergeCount != nil {
		m.autoMergeCount.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	}
}
