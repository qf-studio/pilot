// Package observability wires OpenTelemetry providers (traces, metrics, logs)
// into Pilot. It is opt-in via the `observability:` config section and falls
// back to no-op providers when disabled, so instrumentation added elsewhere in
// the codebase is always safe to call.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	runtimeinstrument "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

// Config controls the OTel SDK setup. All fields are optional; sensible
// defaults mirror the OTLP spec (localhost:4317 gRPC).
type Config struct {
	Enabled        bool              `yaml:"enabled"`
	ServiceName    string            `yaml:"service_name"`
	ServiceVersion string            `yaml:"service_version"`
	Exporter       string            `yaml:"exporter"` // otlp | stdout | none
	Endpoint       string            `yaml:"endpoint"`
	Protocol       string            `yaml:"protocol"` // grpc | http
	Insecure       bool              `yaml:"insecure"`
	Headers        map[string]string `yaml:"headers"`
	Sampling       float64           `yaml:"sampling"`

	Traces  SignalConfig `yaml:"traces"`
	Metrics SignalConfig `yaml:"metrics"`
	Logs    SignalConfig `yaml:"logs"`
}

// SignalConfig toggles an individual signal (traces/metrics/logs).
type SignalConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"` // metrics export interval; ignored for traces/logs
}

// DefaultConfig returns sensible off-by-default settings. Callers who want
// OTel must flip Enabled to true (and typically set an endpoint).
func DefaultConfig() *Config {
	return &Config{
		Enabled:     false,
		ServiceName: "pilot",
		Exporter:    "otlp",
		Endpoint:    "localhost:4317",
		Protocol:    "grpc",
		Insecure:    true,
		Sampling:    1.0,
		Traces:      SignalConfig{Enabled: true},
		Metrics:     SignalConfig{Enabled: true, Interval: 60 * time.Second},
		Logs:        SignalConfig{Enabled: true},
	}
}

// Shutdown flushes any buffered telemetry and releases exporter resources.
// It is always safe to call, even when Init failed or was a no-op.
type Shutdown func(context.Context) error

// noopShutdown is returned when OTel is disabled so callers can always defer.
func noopShutdown(context.Context) error { return nil }

// Init configures global OTel providers from cfg. When cfg is nil or
// Enabled=false, it returns a no-op Shutdown and does nothing else.
//
// When enabled, it sets up a TracerProvider, MeterProvider, and LoggerProvider
// with the selected exporter, installs them as process globals, and wires a
// slog→OTel logs handler via slogHandlerSink. The returned Shutdown flushes
// all three providers; call it before process exit.
func Init(ctx context.Context, cfg *Config, slogHandlerSink func(slog.Handler)) (Shutdown, error) {
	if cfg == nil || !cfg.Enabled {
		return noopShutdown, nil
	}

	res, err := buildResource(cfg)
	if err != nil {
		return noopShutdown, fmt.Errorf("build resource: %w", err)
	}

	var shutdowns []func(context.Context) error

	if cfg.Traces.Enabled {
		tp, err := newTracerProvider(ctx, cfg, res)
		if err != nil {
			return noopShutdown, fmt.Errorf("tracer provider: %w", err)
		}
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{}, propagation.Baggage{},
		))
		shutdowns = append(shutdowns, tp.Shutdown)
	}

	if cfg.Metrics.Enabled {
		mp, err := newMeterProvider(ctx, cfg, res)
		if err != nil {
			return noopShutdown, fmt.Errorf("meter provider: %w", err)
		}
		otel.SetMeterProvider(mp)
		shutdowns = append(shutdowns, mp.Shutdown)

		// Start Go runtime metrics (GC, goroutines, mem). Best-effort.
		if err := runtimeinstrument.Start(runtimeinstrument.WithMeterProvider(mp)); err != nil {
			// Non-fatal — log via slog and continue.
			slog.Warn("otel: runtime metrics failed to start", "error", err)
		}
	}

	if cfg.Logs.Enabled {
		lp, err := newLoggerProvider(ctx, cfg, res)
		if err != nil {
			return noopShutdown, fmt.Errorf("logger provider: %w", err)
		}
		shutdowns = append(shutdowns, lp.Shutdown)
		if slogHandlerSink != nil {
			slogHandlerSink(otelslog.NewHandler(cfg.ServiceName, otelslog.WithLoggerProvider(lp)))
		}
	}

	return func(sctx context.Context) error {
		var errs []error
		for _, fn := range shutdowns {
			if err := fn(sctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}, nil
}

// HTTPTransport wraps base with otelhttp.NewTransport so outbound HTTP calls
// emit client spans and metrics. If base is nil, http.DefaultTransport is used.
// Safe to call even when OTel is disabled — the global TracerProvider is a
// no-op in that case and the transport overhead is negligible.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
}

// HTTPHandler wraps an HTTP handler so inbound requests emit server spans.
// operation is used as the span name prefix (e.g. "pilot.gateway").
func HTTPHandler(h http.Handler, operation string) http.Handler {
	return otelhttp.NewHandler(h, operation)
}

func buildResource(cfg *Config) (*resource.Resource, error) {
	version := cfg.ServiceVersion
	if version == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			version = info.Main.Version
		}
	}
	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(version),
		),
	)
}

func newTracerProvider(ctx context.Context, cfg *Config, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	var exp sdktrace.SpanExporter
	var err error
	switch cfg.Exporter {
	case "stdout":
		exp, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
	case "none":
		return sdktrace.NewTracerProvider(sdktrace.WithResource(res)), nil
	default: // otlp
		exp, err = newOTLPTraceExporter(ctx, cfg)
	}
	if err != nil {
		return nil, err
	}

	sampling := cfg.Sampling
	if sampling <= 0 {
		sampling = 1.0
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampling))),
	), nil
}

func newOTLPTraceExporter(ctx context.Context, cfg *Config) (sdktrace.SpanExporter, error) {
	if cfg.Protocol == "http" {
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
		}
		return otlptrace.New(ctx, otlptracehttp.NewClient(opts...))
	}
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
	}
	return otlptrace.New(ctx, otlptracegrpc.NewClient(opts...))
}

func newMeterProvider(ctx context.Context, cfg *Config, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	interval := cfg.Metrics.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}

	var reader sdkmetric.Reader
	switch cfg.Exporter {
	case "stdout":
		exp, err := stdoutmetric.New()
		if err != nil {
			return nil, err
		}
		reader = sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(interval))
	case "none":
		return sdkmetric.NewMeterProvider(sdkmetric.WithResource(res)), nil
	default:
		exp, err := newOTLPMetricExporter(ctx, cfg)
		if err != nil {
			return nil, err
		}
		reader = sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(interval))
	}

	return sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	), nil
}

func newOTLPMetricExporter(ctx context.Context, cfg *Config) (sdkmetric.Exporter, error) {
	if cfg.Protocol == "http" {
		opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlpmetrichttp.WithHeaders(cfg.Headers))
		}
		return otlpmetrichttp.New(ctx, opts...)
	}
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(cfg.Headers))
	}
	return otlpmetricgrpc.New(ctx, opts...)
}

func newLoggerProvider(ctx context.Context, cfg *Config, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	var exp sdklog.Exporter
	var err error
	switch cfg.Exporter {
	case "stdout":
		exp, err = stdoutlog.New()
	case "none":
		return sdklog.NewLoggerProvider(sdklog.WithResource(res)), nil
	default:
		exp, err = newOTLPLogExporter(ctx, cfg)
	}
	if err != nil {
		return nil, err
	}
	return sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	), nil
}

func newOTLPLogExporter(ctx context.Context, cfg *Config) (sdklog.Exporter, error) {
	if cfg.Protocol == "http" {
		opts := []otlploghttp.Option{otlploghttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlploghttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlploghttp.WithHeaders(cfg.Headers))
		}
		return otlploghttp.New(ctx, opts...)
	}
	opts := []otlploggrpc.Option{otlploggrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlploggrpc.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlploggrpc.WithHeaders(cfg.Headers))
	}
	return otlploggrpc.New(ctx, opts...)
}
