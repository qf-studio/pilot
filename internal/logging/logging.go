// Package logging provides structured logging for Pilot using Go's slog.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
)

// contextKey is a type for context keys to avoid collisions.
type contextKey string

const (
	// Context keys for log fields
	taskIDKey        contextKey = "task_id"
	componentKey     contextKey = "component"
	projectKey       contextKey = "project"
	correlationIDKey contextKey = "correlation_id"
)

var (
	// defaultLogger is the global logger instance
	defaultLogger *slog.Logger
	loggerMu      sync.RWMutex
)

func init() {
	// Initialize with a basic text handler for development
	defaultLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// Config holds logging configuration.
type Config struct {
	Level    string          `yaml:"level"`    // debug, info, warn, error
	Format   string          `yaml:"format"`   // json, text
	Output   string          `yaml:"output"`   // stdout, stderr, or file path
	Rotation *RotationConfig `yaml:"rotation"` // Log rotation settings
}

// RotationConfig holds log rotation settings.
type RotationConfig struct {
	MaxSize    string `yaml:"max_size"`    // e.g., "100MB"
	MaxAge     string `yaml:"max_age"`     // e.g., "7d"
	MaxBackups int    `yaml:"max_backups"` // Number of backup files
}

// DefaultConfig returns sensible defaults for logging.
func DefaultConfig() *Config {
	return &Config{
		Level:  "info",
		Format: "text",
		Output: "stdout",
	}
}

// Init initializes the global logger with the given configuration.
func Init(cfg *Config) error {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	level := parseLevel(cfg.Level)
	writer, err := getWriter(cfg)
	if err != nil {
		return err
	}

	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: level == slog.LevelDebug,
	}

	var handler slog.Handler
	switch cfg.Format {
	case "json":
		handler = slog.NewJSONHandler(writer, opts)
	default:
		handler = slog.NewTextHandler(writer, opts)
	}

	loggerMu.Lock()
	defaultLogger = slog.New(handler)
	loggerMu.Unlock()

	return nil
}

// Suppress redirects all logging to io.Discard, effectively silencing logs.
// Use this when running in TUI dashboard mode to prevent log output from
// corrupting the terminal display.
func Suppress() {
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))

	loggerMu.Lock()
	defaultLogger = discardLogger
	loggerMu.Unlock()

	// Also set the global slog default to suppress any direct slog.Info() calls
	slog.SetDefault(discardLogger)
}

// parseLevel converts a string level to slog.Level.
func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// getWriter returns the appropriate io.Writer based on config.
func getWriter(cfg *Config) (io.Writer, error) {
	switch cfg.Output {
	case "stdout", "":
		return os.Stdout, nil
	case "stderr":
		return os.Stderr, nil
	default:
		// File output with optional rotation
		return newRotatingWriter(cfg.Output, cfg.Rotation)
	}
}

// Logger returns the global logger.
func Logger() *slog.Logger {
	loggerMu.RLock()
	defer loggerMu.RUnlock()
	return defaultLogger
}

// With returns a logger with additional attributes.
func With(args ...any) *slog.Logger {
	return Logger().With(args...)
}

// WithComponent returns a logger with a component attribute.
func WithComponent(component string) *slog.Logger {
	return Logger().With(slog.String("component", component))
}

// WithTask returns a logger with task context.
func WithTask(taskID string) *slog.Logger {
	return Logger().With(slog.String("task_id", taskID))
}

// WithCorrelationID returns a logger with a correlation ID for request tracing.
func WithCorrelationID(correlationID string) *slog.Logger {
	return Logger().With(slog.String("correlation_id", correlationID))
}

// WithContext returns a logger with values from context.
func WithContext(ctx context.Context) *slog.Logger {
	logger := Logger()

	if taskID := ctx.Value(taskIDKey); taskID != nil {
		logger = logger.With(slog.String("task_id", taskID.(string)))
	}
	if component := ctx.Value(componentKey); component != nil {
		logger = logger.With(slog.String("component", component.(string)))
	}
	if project := ctx.Value(projectKey); project != nil {
		logger = logger.With(slog.String("project", project.(string)))
	}
	if correlationID := ctx.Value(correlationIDKey); correlationID != nil {
		logger = logger.With(slog.String("correlation_id", correlationID.(string)))
	}

	return logger
}

// ContextWithTaskID adds a task ID to the context.
func ContextWithTaskID(ctx context.Context, taskID string) context.Context {
	return context.WithValue(ctx, taskIDKey, taskID)
}

// ContextWithComponent adds a component name to the context.
func ContextWithComponent(ctx context.Context, component string) context.Context {
	return context.WithValue(ctx, componentKey, component)
}

// ContextWithProject adds a project name to the context.
func ContextWithProject(ctx context.Context, project string) context.Context {
	return context.WithValue(ctx, projectKey, project)
}

// ContextWithCorrelationID adds a correlation ID to the context for request tracing.
func ContextWithCorrelationID(ctx context.Context, correlationID string) context.Context {
	return context.WithValue(ctx, correlationIDKey, correlationID)
}

// Convenience functions that use the default logger

// Debug logs at debug level.
func Debug(msg string, args ...any) {
	Logger().Debug(msg, args...)
}

// Info logs at info level.
func Info(msg string, args ...any) {
	Logger().Info(msg, args...)
}

// Warn logs at warn level.
func Warn(msg string, args ...any) {
	Logger().Warn(msg, args...)
}

// Error logs at error level.
func Error(msg string, args ...any) {
	Logger().Error(msg, args...)
}

// DebugContext logs at debug level with context.
func DebugContext(ctx context.Context, msg string, args ...any) {
	WithContext(ctx).DebugContext(ctx, msg, args...)
}

// InfoContext logs at info level with context.
func InfoContext(ctx context.Context, msg string, args ...any) {
	WithContext(ctx).InfoContext(ctx, msg, args...)
}

// WarnContext logs at warn level with context.
func WarnContext(ctx context.Context, msg string, args ...any) {
	WithContext(ctx).WarnContext(ctx, msg, args...)
}

// ErrorContext logs at error level with context.
func ErrorContext(ctx context.Context, msg string, args ...any) {
	WithContext(ctx).ErrorContext(ctx, msg, args...)
}
