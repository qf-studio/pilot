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
