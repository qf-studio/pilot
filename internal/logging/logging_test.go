package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"invalid", slog.LevelInfo}, // defaults to info
		{"", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseLevel(tt.input)
			if result != tt.expected {
				t.Errorf("parseLevel(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		hasError bool
	}{
		{"100", 100, false},
		{"100B", 100, false},
		{"100KB", 100 * 1024, false},
		{"100MB", 100 * 1024 * 1024, false},
		{"1GB", 1024 * 1024 * 1024, false},
		{"100mb", 100 * 1024 * 1024, false}, // case insensitive
		{"invalid", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseSize(tt.input)
			if tt.hasError && err == nil {
				t.Errorf("parseSize(%q) expected error", tt.input)
			}
			if !tt.hasError && err != nil {
				t.Errorf("parseSize(%q) unexpected error: %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("parseSize(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		hasError bool
	}{
		{"7d", "168h0m0s", false},
		{"1w", "168h0m0s", false},
		{"2w", "336h0m0s", false},
		{"24h", "24h0m0s", false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseDuration(tt.input)
			if tt.hasError && err == nil {
				t.Errorf("parseDuration(%q) expected error", tt.input)
			}
			if !tt.hasError {
				if err != nil {
					t.Errorf("parseDuration(%q) unexpected error: %v", tt.input, err)
				}
				if result.String() != tt.expected {
					t.Errorf("parseDuration(%q) = %v, want %v", tt.input, result.String(), tt.expected)
				}
			}
		})
	}
}

func TestInit(t *testing.T) {
	t.Run("default config", func(t *testing.T) {
		err := Init(nil)
		if err != nil {
			t.Fatalf("Init(nil) failed: %v", err)
		}
	})

	t.Run("json format", func(t *testing.T) {
		err := Init(&Config{
			Level:  "debug",
			Format: "json",
			Output: "stdout",
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
	})

	t.Run("text format", func(t *testing.T) {
		err := Init(&Config{
			Level:  "info",
			Format: "text",
			Output: "stderr",
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
	})
}

func TestContextPropagation(t *testing.T) {
	ctx := context.Background()
	ctx = ContextWithTaskID(ctx, "TASK-123")
	ctx = ContextWithComponent(ctx, "executor")
	ctx = ContextWithProject(ctx, "pilot")

	if taskID := ctx.Value(taskIDKey); taskID != "TASK-123" {
		t.Errorf("expected task_id=TASK-123, got %v", taskID)
	}
	if component := ctx.Value(componentKey); component != "executor" {
		t.Errorf("expected component=executor, got %v", component)
	}
	if project := ctx.Value(projectKey); project != "pilot" {
		t.Errorf("expected project=pilot, got %v", project)
	}
}

func TestJSONOutput(t *testing.T) {
	var buf bytes.Buffer

	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := slog.New(handler)

	logger.Info("test message",
		slog.String("component", "test"),
		slog.String("task_id", "TASK-001"),
		slog.Int("tokens", 5000),
	)

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if result["msg"] != "test message" {
		t.Errorf("expected msg='test message', got %v", result["msg"])
	}
	if result["component"] != "test" {
		t.Errorf("expected component='test', got %v", result["component"])
	}
	if result["task_id"] != "TASK-001" {
		t.Errorf("expected task_id='TASK-001', got %v", result["task_id"])
	}
	if result["level"] != "INFO" {
		t.Errorf("expected level='INFO', got %v", result["level"])
	}
}

func TestWithComponent(t *testing.T) {
	var buf bytes.Buffer

	handler := slog.NewJSONHandler(&buf, nil)
	loggerMu.Lock()
	defaultLogger = slog.New(handler)
	loggerMu.Unlock()

	WithComponent("gateway").Info("test message")

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if result["component"] != "gateway" {
		t.Errorf("expected component='gateway', got %v", result["component"])
	}
}

func TestWithTask(t *testing.T) {
	var buf bytes.Buffer

	handler := slog.NewJSONHandler(&buf, nil)
	loggerMu.Lock()
	defaultLogger = slog.New(handler)
	loggerMu.Unlock()

	WithTask("TASK-456").Info("task started")

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if result["task_id"] != "TASK-456" {
		t.Errorf("expected task_id='TASK-456', got %v", result["task_id"])
	}
}

func TestLogLevels(t *testing.T) {
	var buf bytes.Buffer

	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	loggerMu.Lock()
	defaultLogger = slog.New(handler)
	loggerMu.Unlock()

	tests := []struct {
		logFunc func(string, ...any)
		level   string
	}{
		{Debug, "DEBUG"},
		{Info, "INFO"},
		{Warn, "WARN"},
		{Error, "ERROR"},
	}

	for _, tt := range tests {
		buf.Reset()
		tt.logFunc("test message")

		var result map[string]interface{}
		if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output for %s: %v", tt.level, err)
		}

		if result["level"] != tt.level {
			t.Errorf("expected level=%s, got %v", tt.level, result["level"])
		}
	}
}

func TestFileOutput(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	err := Init(&Config{
		Level:  "info",
		Format: "text",
		Output: logFile,
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	Info("test file output")

	// Give a moment for async operations
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	if !strings.Contains(string(content), "test file output") {
		t.Errorf("log file does not contain expected message")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Level != "info" {
		t.Errorf("expected level=info, got %s", cfg.Level)
	}
	if cfg.Format != "text" {
		t.Errorf("expected format=text, got %s", cfg.Format)
	}
	if cfg.Output != "stdout" {
		t.Errorf("expected output=stdout, got %s", cfg.Output)
	}
}

func TestLogger(t *testing.T) {
	// Initialize with known config
	err := Init(&Config{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	logger := Logger()
	if logger == nil {
		t.Error("Logger() returned nil")
	}
}

func TestWith(t *testing.T) {
	var buf bytes.Buffer

	handler := slog.NewJSONHandler(&buf, nil)
	loggerMu.Lock()
	defaultLogger = slog.New(handler)
	loggerMu.Unlock()

	With("custom_key", "custom_value").Info("test message")

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if result["custom_key"] != "custom_value" {
		t.Errorf("expected custom_key='custom_value', got %v", result["custom_key"])
	}
}

func TestWithContext(t *testing.T) {
	tests := []struct {
		name           string
		setupContext   func(context.Context) context.Context
		expectedFields map[string]string
	}{
		{
			name: "with task_id only",
			setupContext: func(ctx context.Context) context.Context {
				return ContextWithTaskID(ctx, "TASK-001")
			},
			expectedFields: map[string]string{
				"task_id": "TASK-001",
			},
		},
		{
			name: "with component only",
			setupContext: func(ctx context.Context) context.Context {
				return ContextWithComponent(ctx, "gateway")
			},
			expectedFields: map[string]string{
				"component": "gateway",
			},
		},
		{
			name: "with project only",
			setupContext: func(ctx context.Context) context.Context {
				return ContextWithProject(ctx, "pilot")
			},
			expectedFields: map[string]string{
				"project": "pilot",
			},
		},
		{
			name: "with all fields",
			setupContext: func(ctx context.Context) context.Context {
				ctx = ContextWithTaskID(ctx, "TASK-002")
				ctx = ContextWithComponent(ctx, "executor")
				ctx = ContextWithProject(ctx, "my-project")
				return ctx
			},
			expectedFields: map[string]string{
				"task_id":   "TASK-002",
				"component": "executor",
				"project":   "my-project",
			},
		},
		{
			name: "with empty context",
			setupContext: func(ctx context.Context) context.Context {
				return ctx
			},
			expectedFields: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			handler := slog.NewJSONHandler(&buf, nil)
			loggerMu.Lock()
			defaultLogger = slog.New(handler)
			loggerMu.Unlock()

			ctx := tt.setupContext(context.Background())
			WithContext(ctx).Info("test message")

			var result map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
				t.Fatalf("failed to parse JSON output: %v", err)
			}

			for key, expectedValue := range tt.expectedFields {
				if result[key] != expectedValue {
					t.Errorf("expected %s='%s', got %v", key, expectedValue, result[key])
				}
			}
		})
	}
}

func TestContextLoggingFunctions(t *testing.T) {
	tests := []struct {
		name    string
		logFunc func(context.Context, string, ...any)
		level   string
	}{
		{"DebugContext", DebugContext, "DEBUG"},
		{"InfoContext", InfoContext, "INFO"},
		{"WarnContext", WarnContext, "WARN"},
		{"ErrorContext", ErrorContext, "ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
				Level: slog.LevelDebug,
			})
			loggerMu.Lock()
			defaultLogger = slog.New(handler)
			loggerMu.Unlock()

			ctx := ContextWithTaskID(context.Background(), "TASK-CTX")
			tt.logFunc(ctx, "context test message")

			var result map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
				t.Fatalf("failed to parse JSON output for %s: %v", tt.name, err)
			}

			if result["level"] != tt.level {
				t.Errorf("expected level=%s, got %v", tt.level, result["level"])
			}
			if result["task_id"] != "TASK-CTX" {
				t.Errorf("expected task_id='TASK-CTX', got %v", result["task_id"])
			}
		})
	}
}

func TestInitWithDebugLevel(t *testing.T) {
	// Test that debug level enables AddSource option
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "debug.log")

	err := Init(&Config{
		Level:  "debug",
		Format: "json",
		Output: logFile,
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	Debug("debug message with source")

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	// Debug level should include source information
	if !strings.Contains(string(content), "source") {
		t.Errorf("expected source info in debug log, content: %s", content)
	}
}

func TestInitWithEmptyOutput(t *testing.T) {
	// Empty output should default to stdout
	err := Init(&Config{
		Level:  "info",
		Format: "text",
		Output: "",
	})
	if err != nil {
		t.Fatalf("Init with empty output failed: %v", err)
	}
}

func TestLogLevelsFiltering(t *testing.T) {
	tests := []struct {
		name          string
		configLevel   string
		logLevel      string
		logFunc       func(string, ...any)
		shouldAppear  bool
	}{
		{"info config allows info", "info", "INFO", Info, true},
		{"info config allows warn", "info", "WARN", Warn, true},
		{"info config allows error", "info", "ERROR", Error, true},
		{"warn config blocks info", "warn", "INFO", Info, false},
		{"warn config allows warn", "warn", "WARN", Warn, true},
		{"error config blocks warn", "error", "WARN", Warn, false},
		{"error config allows error", "error", "ERROR", Error, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
				Level: parseLevel(tt.configLevel),
			})
			loggerMu.Lock()
			defaultLogger = slog.New(handler)
			loggerMu.Unlock()

			tt.logFunc("test message")

			hasContent := buf.Len() > 0
			if hasContent != tt.shouldAppear {
				t.Errorf("expected message to appear: %v, but got: %v", tt.shouldAppear, hasContent)
			}
		})
	}
}

func TestWithMultipleAttributes(t *testing.T) {
	var buf bytes.Buffer

	handler := slog.NewJSONHandler(&buf, nil)
	loggerMu.Lock()
	defaultLogger = slog.New(handler)
	loggerMu.Unlock()

	With(
		"key1", "value1",
		"key2", 42,
		"key3", true,
	).Info("multiple attributes")

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if result["key1"] != "value1" {
		t.Errorf("expected key1='value1', got %v", result["key1"])
	}
	if result["key2"] != float64(42) {
		t.Errorf("expected key2=42, got %v", result["key2"])
	}
	if result["key3"] != true {
		t.Errorf("expected key3=true, got %v", result["key3"])
	}
}

func TestLogLevelsWithArguments(t *testing.T) {
	var buf bytes.Buffer

	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	loggerMu.Lock()
	defaultLogger = slog.New(handler)
	loggerMu.Unlock()

	tests := []struct {
		logFunc func(string, ...any)
		level   string
	}{
		{Debug, "DEBUG"},
		{Info, "INFO"},
		{Warn, "WARN"},
		{Error, "ERROR"},
	}

	for _, tt := range tests {
		buf.Reset()
		tt.logFunc("test message", "extra_key", "extra_value")

		var result map[string]interface{}
		if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
			t.Fatalf("failed to parse JSON output for %s: %v", tt.level, err)
		}

		if result["extra_key"] != "extra_value" {
			t.Errorf("expected extra_key='extra_value', got %v", result["extra_key"])
		}
	}
}
