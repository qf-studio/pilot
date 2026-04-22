package observability

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

func TestInit_DisabledReturnsNoop(t *testing.T) {
	shutdown, err := Init(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Init(nil) returned error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init(nil) returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("noop shutdown returned error: %v", err)
	}

	cfg := &Config{Enabled: false}
	shutdown, err = Init(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Init(disabled) returned error: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("disabled shutdown returned error: %v", err)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Error("default config should be disabled")
	}
	if cfg.ServiceName != "pilot" {
		t.Errorf("service_name = %q, want pilot", cfg.ServiceName)
	}
	if cfg.Protocol != "grpc" {
		t.Errorf("protocol = %q, want grpc", cfg.Protocol)
	}
	if cfg.Metrics.Interval != 60*time.Second {
		t.Errorf("metrics interval = %v, want 60s", cfg.Metrics.Interval)
	}
}

func TestHTTPTransport_WrapsNilWithDefault(t *testing.T) {
	rt := HTTPTransport(nil)
	if rt == nil {
		t.Fatal("HTTPTransport(nil) returned nil")
	}
	// Also must wrap a supplied transport without panicking.
	_ = HTTPTransport(http.DefaultTransport)
}

func TestStartSpan_NoopWhenDisabled(t *testing.T) {
	ctx, end := StartSpan(context.Background(), "test.span")
	if ctx == nil {
		t.Fatal("StartSpan returned nil ctx")
	}
	end(nil) // must not panic with the noop tracer
}

func TestInit_StdoutExporterEnabled(t *testing.T) {
	// Sanity check that enabling all three signals with stdout exporters
	// succeeds and shutdown returns without error. Uses stdout so no network.
	var captured slog.Handler
	cfg := &Config{
		Enabled:     true,
		ServiceName: "pilot-test",
		Exporter:    "stdout",
		Protocol:    "grpc",
		Sampling:    1.0,
		Traces:      SignalConfig{Enabled: true},
		Metrics:     SignalConfig{Enabled: true, Interval: 1 * time.Second},
		Logs:        SignalConfig{Enabled: true},
	}
	shutdown, err := Init(context.Background(), cfg, func(h slog.Handler) { captured = h })
	if err != nil {
		t.Fatalf("Init(enabled, stdout) failed: %v", err)
	}
	if captured == nil {
		t.Error("logs bridge did not call slog handler sink")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Errorf("shutdown returned error: %v", err)
	}
}
