package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewRotatingWriter(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *RotationConfig
		wantError bool
	}{
		{
			name:      "nil config uses defaults",
			cfg:       nil,
			wantError: false,
		},
		{
			name: "valid config",
			cfg: &RotationConfig{
				MaxSize:    "10MB",
				MaxAge:     "7d",
				MaxBackups: 5,
			},
			wantError: false,
		},
		{
			name: "invalid max_size",
			cfg: &RotationConfig{
				MaxSize: "invalid",
			},
			wantError: true,
		},
		{
			name: "invalid max_age",
			cfg: &RotationConfig{
				MaxAge: "invalid",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			logFile := filepath.Join(tmpDir, "test.log")

			writer, err := newRotatingWriter(logFile, tt.cfg)
			if tt.wantError {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Clean up
			if rw, ok := writer.(*rotatingWriter); ok {
				_ = rw.Close()
			}
		})
	}
}

func TestRotatingWriterWrite(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	writer, err := newRotatingWriter(logFile, nil)
	if err != nil {
		t.Fatalf("failed to create rotating writer: %v", err)
	}

	rw := writer.(*rotatingWriter)
	defer func() { _ = rw.Close() }()

	// Write some data
	msg := "test log message\n"
	n, err := rw.Write([]byte(msg))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(msg) {
		t.Errorf("expected to write %d bytes, wrote %d", len(msg), n)
	}

	// Verify file content
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	if string(content) != msg {
		t.Errorf("expected content '%s', got '%s'", msg, content)
	}
}

func TestRotatingWriterRotation(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	// Use a small max size to trigger rotation quickly
	cfg := &RotationConfig{
		MaxSize:    "100B",
		MaxAge:     "1d",
		MaxBackups: 2,
	}

	writer, err := newRotatingWriter(logFile, cfg)
	if err != nil {
		t.Fatalf("failed to create rotating writer: %v", err)
	}

	rw := writer.(*rotatingWriter)
	defer func() { _ = rw.Close() }()

	// Write enough data to trigger rotation
	msg := strings.Repeat("x", 50) + "\n"

	// First write (fills up to 51 bytes)
	_, err = rw.Write([]byte(msg))
	if err != nil {
		t.Fatalf("first write failed: %v", err)
	}

	// Second write (should trigger rotation at 102 bytes > 100 byte limit)
	_, err = rw.Write([]byte(msg))
	if err != nil {
		t.Fatalf("second write failed: %v", err)
	}

	// Give async cleanup time to run
	time.Sleep(100 * time.Millisecond)

	// Third write (triggers another rotation)
	_, err = rw.Write([]byte(msg))
	if err != nil {
		t.Fatalf("third write failed: %v", err)
	}

	// Wait for async cleanup
	time.Sleep(100 * time.Millisecond)

	// Check that backup files exist
	matches, err := filepath.Glob(filepath.Join(tmpDir, "test.*.log"))
	if err != nil {
		t.Fatalf("failed to glob backup files: %v", err)
	}

	// Should have at least one backup
	if len(matches) < 1 {
		t.Errorf("expected at least 1 backup file, found %d", len(matches))
	}
}

func TestRotatingWriterClose(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	writer, err := newRotatingWriter(logFile, nil)
	if err != nil {
		t.Fatalf("failed to create rotating writer: %v", err)
	}

	rw := writer.(*rotatingWriter)

	// Write something first
	_, err = rw.Write([]byte("test\n"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Close should work
	err = rw.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Second close should not error (file already nil)
	err = rw.Close()
	if err != nil {
		t.Errorf("Second Close failed: %v", err)
	}
}

func TestRotatingWriterDirectoryCreation(t *testing.T) {
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "nested", "deep", "logs")
	logFile := filepath.Join(nestedDir, "test.log")

	writer, err := newRotatingWriter(logFile, nil)
	if err != nil {
		t.Fatalf("failed to create rotating writer: %v", err)
	}

	rw := writer.(*rotatingWriter)
	defer func() { _ = rw.Close() }()

	// Verify directory was created
	if _, err := os.Stat(nestedDir); os.IsNotExist(err) {
		t.Error("expected nested directory to be created")
	}
}

func TestParseSizeEdgeCases(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		hasError bool
	}{
		{"0", 0, false},
		{"0KB", 0, false},
		{"  100MB  ", 100 * 1024 * 1024, false}, // with whitespace
		{"1gb", 1024 * 1024 * 1024, false},      // lowercase
		{"10b", 10, false},                      // bytes lowercase
		{"", 0, true},                           // empty string
		{"-1", -1, false},                       // negative (parsing succeeds)
		{"abc", 0, true},                        // non-numeric
		{"1.5MB", 0, true},                      // float not supported
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
			if !tt.hasError && result != tt.expected {
				t.Errorf("parseSize(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseDurationEdgeCases(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		hasError bool
	}{
		{"1d", "24h0m0s", false},
		{"0d", "0s", false},
		{"30d", "720h0m0s", false},
		{"  7d  ", "168h0m0s", false}, // with whitespace
		{"3w", "504h0m0s", false},     // 3 weeks
		{"1h", "1h0m0s", false},       // standard duration
		{"30m", "30m0s", false},       // minutes
		{"60s", "1m0s", false},        // seconds
		{"", "", true},                // empty string
		{"1x", "", true},              // invalid suffix
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
				} else if result.String() != tt.expected {
					t.Errorf("parseDuration(%q) = %v, want %v", tt.input, result.String(), tt.expected)
				}
			}
		})
	}
}

func TestRotatingWriterWriteAfterNilFile(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	writer, err := newRotatingWriter(logFile, nil)
	if err != nil {
		t.Fatalf("failed to create rotating writer: %v", err)
	}

	rw := writer.(*rotatingWriter)

	// Manually set file to nil to simulate closed state
	rw.mu.Lock()
	if rw.file != nil {
		_ = rw.file.Close()
		rw.file = nil
	}
	rw.mu.Unlock()

	// Write should reopen the file
	msg := "test after nil\n"
	n, err := rw.Write([]byte(msg))
	if err != nil {
		t.Fatalf("Write after nil failed: %v", err)
	}
	if n != len(msg) {
		t.Errorf("expected to write %d bytes, wrote %d", len(msg), n)
	}

	_ = rw.Close()
}

func TestGetWriter(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		check  func(t *testing.T, err error)
	}{
		{
			name: "stdout",
			config: &Config{
				Output: "stdout",
			},
			check: func(t *testing.T, err error) {
				if err != nil {
					t.Errorf("unexpected error for stdout: %v", err)
				}
			},
		},
		{
			name: "stderr",
			config: &Config{
				Output: "stderr",
			},
			check: func(t *testing.T, err error) {
				if err != nil {
					t.Errorf("unexpected error for stderr: %v", err)
				}
			},
		},
		{
			name: "empty defaults to stdout",
			config: &Config{
				Output: "",
			},
			check: func(t *testing.T, err error) {
				if err != nil {
					t.Errorf("unexpected error for empty output: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := getWriter(tt.config)
			tt.check(t, err)
		})
	}
}

func TestInitWithRotation(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "rotated.log")

	err := Init(&Config{
		Level:  "info",
		Format: "json",
		Output: logFile,
		Rotation: &RotationConfig{
			MaxSize:    "1MB",
			MaxAge:     "7d",
			MaxBackups: 3,
		},
	})
	if err != nil {
		t.Fatalf("Init with rotation failed: %v", err)
	}

	Info("test with rotation config")

	// Verify file was created
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Error("expected log file to be created")
	}
}

func TestRotatingWriterMaxBackups(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	// Create with very small size and 1 backup
	cfg := &RotationConfig{
		MaxSize:    "50B",
		MaxAge:     "1d",
		MaxBackups: 1,
	}

	writer, err := newRotatingWriter(logFile, cfg)
	if err != nil {
		t.Fatalf("failed to create rotating writer: %v", err)
	}

	rw := writer.(*rotatingWriter)

	// Generate enough writes to create multiple rotations
	msg := strings.Repeat("a", 40) + "\n"

	for i := 0; i < 5; i++ {
		_, err = rw.Write([]byte(msg))
		if err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
		// Small delay to ensure timestamp differs for each backup
		time.Sleep(10 * time.Millisecond)
	}

	_ = rw.Close()

	// Wait for async cleanup
	time.Sleep(200 * time.Millisecond)

	// Check backup count - should not exceed maxBackups
	matches, err := filepath.Glob(filepath.Join(tmpDir, "test.*.log"))
	if err != nil {
		t.Fatalf("failed to glob backup files: %v", err)
	}

	// MaxBackups is 1, so at most 1 backup should exist (might be cleaned up)
	if len(matches) > 1 {
		t.Errorf("expected at most 1 backup file, found %d: %v", len(matches), matches)
	}
}
