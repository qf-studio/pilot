package logging

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Secret-shaped samples are assembled at runtime so no tracked file holds a
// realistic token (the secret-pattern scan would reject it).
func secretSamples() map[string]string {
	return map[string]string{
		"slack bot":     "xoxb-" + strings.Repeat("1", 12) + "-" + strings.Repeat("2", 12),
		"slack app":     "xoxa-" + strings.Repeat("1", 12) + "-" + strings.Repeat("2", 12),
		"slack user":    "xoxp-" + strings.Repeat("1", 12) + "-" + strings.Repeat("2", 12),
		"openai":        "sk-" + strings.Repeat("a", 40),
		"github pat":    "ghp_" + strings.Repeat("a1B2c3", 6),
		"github oauth":  "gho_" + strings.Repeat("a1B2c3", 6),
		"github user":   "ghu_" + strings.Repeat("a1B2c3", 6),
		"github server": "ghs_" + strings.Repeat("a1B2c3", 6),
		"github fine":   "github_pat_" + strings.Repeat("a", 22) + "_" + strings.Repeat("b", 59),
		"aws":           "AKIA" + strings.Repeat("A", 16),
		"linear":        "lin_api_" + strings.Repeat("a", 40),
		"paddle":        "pdl_live_" + strings.Repeat("a", 24),
		"telegram":      "123456789:AA" + strings.Repeat("a", 33),
	}
}

func TestRedactingWriter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no secret unchanged", "level=INFO msg=\"task started\" task_id=GH-1\n", "level=INFO msg=\"task started\" task_id=GH-1\n"},
		{"empty", "", ""},
	}
	for kind, secret := range secretSamples() {
		tests = append(tests, struct {
			name string
			in   string
			want string
		}{
			name: "redacts " + kind,
			in:   "level=ERROR msg=\"Get \\\"https://api.telegram.org/bot" + secret + "/getUpdates\\\": EOF\"\n",
			want: "level=ERROR msg=\"Get \\\"https://api.telegram.org/bot<redacted>/getUpdates\\\": EOF\"\n",
		})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := newRedactingWriter(&buf)
			n, err := w.Write([]byte(tt.in))
			if err != nil {
				t.Fatalf("Write error: %v", err)
			}
			if n != len(tt.in) {
				t.Errorf("Write returned %d, want %d", n, len(tt.in))
			}
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInit_RedactsLogFileAndCreatesItPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pilot.log")
	if err := Init(&Config{Level: "info", Format: "text", Output: path}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = Init(DefaultConfig()) })

	token := secretSamples()["telegram"]
	Error("Error fetching updates", "error", "Get \"https://api.telegram.org/bot"+token+"/getUpdates\": EOF")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), token) {
		t.Errorf("log file holds the token: %s", data)
	}
	if !strings.Contains(string(data), "<redacted>") {
		t.Errorf("log file lacks <redacted>: %s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("log file mode = %o, want 0600", mode)
	}
}

func TestRedirectToFile_Redacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := RedirectToFile(path, nil); err != nil {
		t.Fatalf("RedirectToFile: %v", err)
	}
	t.Cleanup(func() { _ = Init(DefaultConfig()) })

	token := secretSamples()["github pat"]
	Warn("push failed", "error", "auth "+token)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), token) || !strings.Contains(string(data), "<redacted>") {
		t.Errorf("unexpected log content: %s", data)
	}
}

// mutation pin: Init no longer calls slog.SetDefault, so bare slog.Default() loggers write unredacted -> this test fails
func TestInit_RedactsBareSlogDefault(t *testing.T) {
	prev := slog.Default()
	path := filepath.Join(t.TempDir(), "pilot.log")
	if err := Init(&Config{Level: "info", Format: "text", Output: path}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		_ = Init(DefaultConfig())
		slog.SetDefault(prev)
	})

	token := secretSamples()["telegram"]
	// The shape of internal/autopilot's slog.Default().With(...) logger.
	slog.Default().With("component", "autopilot").Error("failed to send merge notification",
		"error", "Get \"https://api.telegram.org/bot"+token+"/sendMessage\": EOF")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), token) {
		t.Errorf("slog.Default() write holds the token: %s", data)
	}
	if !strings.Contains(string(data), "<redacted>") {
		t.Errorf("log lacks <redacted>: %s", data)
	}
}

// mutation pin: openFile no longer chmods an existing log, so a 0644 daemon.log or backup keeps its mode -> this test fails
func TestRotatingWriter_TightensExistingFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("old line\n"), 0644); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil { // defeat a restrictive umask
		t.Fatalf("chmod seed: %v", err)
	}

	w, err := newRotatingWriter(path, nil)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer func() { _ = w.(*rotatingWriter).Close() }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("existing log mode = %o, want 600", got)
	}
}
