package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rotatingWriter implements io.Writer with file rotation support.
type rotatingWriter struct {
	filename   string
	maxSize    int64 // bytes
	maxAge     time.Duration
	maxBackups int

	mu          sync.Mutex
	file        *os.File
	currentSize int64
}

// newRotatingWriter creates a new rotating file writer.
func newRotatingWriter(filename string, cfg *RotationConfig) (io.Writer, error) {
	maxSize := int64(100 * 1024 * 1024) // 100MB default
	maxAge := 7 * 24 * time.Hour        // 7 days default
	maxBackups := 3

	if cfg != nil {
		if cfg.MaxSize != "" {
			size, err := parseSize(cfg.MaxSize)
			if err != nil {
				return nil, fmt.Errorf("invalid max_size: %w", err)
			}
			maxSize = size
		}
		if cfg.MaxAge != "" {
			age, err := parseDuration(cfg.MaxAge)
			if err != nil {
				return nil, fmt.Errorf("invalid max_age: %w", err)
			}
			maxAge = age
		}
		if cfg.MaxBackups > 0 {
			maxBackups = cfg.MaxBackups
		}
	}

	// Ensure directory exists
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	w := &rotatingWriter{
		filename:   filename,
		maxSize:    maxSize,
		maxAge:     maxAge,
		maxBackups: maxBackups,
	}

	if err := w.openFile(); err != nil {
		return nil, err
	}

	// Clean up old logs on startup
	go w.cleanOldLogs()

	return w, nil
}

// Write implements io.Writer.
func (w *rotatingWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		if err := w.openFile(); err != nil {
			return 0, err
		}
	}

	// Check if rotation is needed
	if w.currentSize+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = w.file.Write(p)
	w.currentSize += int64(n)
	return n, err
}

// openFile opens the log file.
func (w *rotatingWriter) openFile() error {
	file, err := os.OpenFile(w.filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("failed to stat log file: %w", err)
	}

	w.file = file
	w.currentSize = info.Size()
	return nil
}

// rotate rotates the log file.
func (w *rotatingWriter) rotate() error {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}

	// Generate backup filename with timestamp
	timestamp := time.Now().Format("20060102-150405")
	ext := filepath.Ext(w.filename)
	base := strings.TrimSuffix(w.filename, ext)
	backupName := fmt.Sprintf("%s.%s%s", base, timestamp, ext)

	// Rename current file to backup
	if err := os.Rename(w.filename, backupName); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to rotate log file: %w", err)
	}

	// Open new file
	if err := w.openFile(); err != nil {
		return err
	}

	// Clean up old backups asynchronously
	go w.cleanOldLogs()

	return nil
}

// cleanOldLogs removes old backup files.
func (w *rotatingWriter) cleanOldLogs() {
	dir := filepath.Dir(w.filename)
	base := filepath.Base(w.filename)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext)

	pattern := filepath.Join(dir, prefix+".*"+ext)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}

	// Sort by modification time (oldest first)
	type fileInfo struct {
		path    string
		modTime time.Time
	}
	var files []fileInfo

	now := time.Now()
	for _, match := range matches {
		if match == w.filename {
			continue
		}
		info, err := os.Stat(match)
		if err != nil {
			continue
		}

		// Skip if too old
		if now.Sub(info.ModTime()) > w.maxAge {
			_ = os.Remove(match)
			continue
		}

		files = append(files, fileInfo{path: match, modTime: info.ModTime()})
	}

	// Sort by mod time, oldest first
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	// Remove excess backups
	for len(files) > w.maxBackups {
		_ = os.Remove(files[0].path)
		files = files[1:]
	}
}

// parseSize parses a size string like "100MB" into bytes.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))

	var multiplier int64 = 1
	if strings.HasSuffix(s, "KB") {
		multiplier = 1024
		s = strings.TrimSuffix(s, "KB")
	} else if strings.HasSuffix(s, "MB") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	} else if strings.HasSuffix(s, "GB") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	} else if strings.HasSuffix(s, "B") {
		s = strings.TrimSuffix(s, "B")
	}

	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}

	return n * multiplier, nil
}

// parseDuration parses a duration string like "7d" into time.Duration.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))

	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	if strings.HasSuffix(s, "w") {
		weeks, err := strconv.Atoi(strings.TrimSuffix(s, "w"))
		if err != nil {
			return 0, err
		}
		return time.Duration(weeks) * 7 * 24 * time.Hour, nil
	}

	// Fall back to standard Go duration parsing
	return time.ParseDuration(s)
}

// Close closes the rotating writer.
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}
