package leakscan

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
)

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ageFile(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// TestScanOnce_LeakInRotatedLog: the daemon log is renamed to a backup by
// internal/logging; bytes written after the last tick live only in the backup.
func TestScanOnce_LeakInRotatedLog(t *testing.T) {
	tests := []struct {
		name string
		// newLog is the content of the live file after rotation.
		newLog     string
		wantKinds  []string // kinds of the events, in order
		wantInFile []string // base names of the event files, in order
	}{
		{
			name:       "new file shorter than the offset",
			newLog:     "fresh\n",
			wantKinds:  []string{"ghp"},
			wantInFile: []string{"daemon.20260101-000000.log"},
		},
		{
			name:       "new file already longer than the offset is detected by inode",
			newLog:     strings.Repeat("a long fresh line without a secret\n", 40),
			wantKinds:  []string{"ghp"},
			wantInFile: []string{"daemon.20260101-000000.log"},
		},
		{
			name:       "leak in the backup and in the new file",
			newLog:     "new " + configured + "\n",
			wantKinds:  []string{"ghp", "configured_value"},
			wantInFile: []string{"daemon.20260101-000000.log", "daemon.log"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "daemon.log")
			appendTo(t, logPath, "line before startup\n")
			s, sink := newScanner(t, logPath, "")

			// Written after the last tick, then rotated before the next one.
			appendTo(t, logPath, "leak "+tokenShape+"\n")
			backup := filepath.Join(dir, "daemon.20260101-000000.log")
			if err := os.Rename(logPath, backup); err != nil {
				t.Fatal(err)
			}
			writeFile(t, logPath, tt.newLog)
			s.ScanOnce()

			evs := sink.got()
			if len(evs) != len(tt.wantKinds) {
				t.Fatalf("got %d events, want %d: %+v", len(evs), len(tt.wantKinds), evs)
			}
			for i, ev := range evs {
				if base := filepath.Base(ev.Metadata["file"]); ev.Metadata["kind"] != tt.wantKinds[i] || base != tt.wantInFile[i] {
					t.Errorf("event %d = kind %s file %s, want %s in %s", i, ev.Metadata["kind"], base, tt.wantKinds[i], tt.wantInFile[i])
				}
			}
			st, err := os.Stat(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if s.logOffset != st.Size() {
				t.Errorf("offset = %d, want the new file end %d", s.logOffset, st.Size())
			}
			// The backup is not read again.
			s.ScanOnce()
			if n := len(sink.got()); n != len(tt.wantKinds) {
				t.Errorf("rescan raised events: total %d", n)
			}
		})
	}
}

// TestScanOnce_LeakSplitAcrossReads: a value cut in two by a tick boundary is
// found once, and a value that sits whole in the overlap is not found twice.
func TestScanOnce_LeakSplitAcrossReads(t *testing.T) {
	half := len(tokenShape) / 2
	tests := []struct {
		name   string
		reads  []string
		events int
	}{
		{"split in the middle", []string{`{"t":"x ` + tokenShape[:half], tokenShape[half:] + `"}` + "\n"}, 1},
		{"split one byte from the end", []string{`{"t":"` + tokenShape[:len(tokenShape)-1], tokenShape[len(tokenShape)-1:] + `"}` + "\n"}, 1},
		{"whole value then clean append", []string{tokenShape + "\n", "clean\n"}, 1},
		{"split registered value", []string{"v=" + configured[:5], configured[5:] + "\n"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := filepath.Join(t.TempDir(), "recordings")
			run := filepath.Join(rec, "TASK-1")
			if err := os.MkdirAll(run, 0o700); err != nil {
				t.Fatal(err)
			}
			s, sink := newScanner(t, "", rec)
			for _, r := range tt.reads {
				appendTo(t, filepath.Join(run, "stream.jsonl"), r)
				s.ScanOnce()
			}
			if got := len(sink.got()); got != tt.events {
				t.Fatalf("got %d events, want %d: %+v", got, tt.events, sink.got())
			}
		})
	}
}

// TestScanFile_PrefixOverlap pins the overlap at the scanFile level, including
// a match that was whole in the prefix and then grew into the new bytes.
func TestScanFile_PrefixOverlap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	first := "pad " + tokenShape[:10]
	writeFile(t, path, first+tokenShape[10:])
	res1, err := scanFile(path, 0, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res1.hits) != 1 {
		t.Fatalf("first read hits = %+v", res1.hits)
	}
	if len(res1.tail) > tickOverlap {
		t.Fatalf("tail = %d bytes, want at most %d", len(res1.tail), tickOverlap)
	}
	appendTo(t, path, " more\n")
	res2, err := scanFile(path, res1.next, false, res1.tail)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.hits) != 0 {
		t.Errorf("second read re-reported %+v", res2.hits)
	}
}

// TestScanOnce_LargeRecordingFinishedAcrossTicks: a recording over the per-tick
// cap is finished on later ticks although its mtime no longer changes.
func TestScanOnce_LargeRecordingFinishedAcrossTicks(t *testing.T) {
	old := maxPerTick
	maxPerTick = 256
	t.Cleanup(func() { maxPerTick = old })

	rec := filepath.Join(t.TempDir(), "recordings")
	run := filepath.Join(rec, "TASK-1")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	s, sink := newScanner(t, "", rec)
	stream := filepath.Join(run, "stream.jsonl")
	writeFile(t, stream, strings.Repeat("0123456789abcdef\n", 60)+"leak "+tokenShape+"\n")
	ageFile(t, stream)
	ageFile(t, run)
	// The file is new to the scanner, so the first tick may skip it by mtime
	// only if it is old; make it fresh for the first tick, then age it.
	now := time.Now()
	if err := os.Chtimes(stream, now, now); err != nil {
		t.Fatal(err)
	}

	s.ScanOnce()
	if n := len(sink.got()); n != 0 {
		t.Fatalf("leak past the cap reported on the first tick: %d", n)
	}
	ageFile(t, stream)
	ageFile(t, run)
	for i := 0; i < 20 && len(sink.got()) == 0; i++ {
		s.ScanOnce()
	}
	if n := len(sink.got()); n != 1 {
		t.Fatalf("got %d events after the follow-up ticks, want 1", n)
	}
	st, err := os.Stat(stream)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.recOffsets[stream]; got != st.Size() {
		t.Errorf("offset = %d, want the file end %d", got, st.Size())
	}
}

func newCaptureLogger() (*slog.Logger, func() string) {
	var mu sync.Mutex
	var buf bytes.Buffer
	h := slog.NewTextHandler(&lockedWriter{mu: &mu, buf: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// TestScanOnce_RecordingsRoot: a symlinked root is walked; a missing one logs
// a single Warn line however many ticks pass.
func TestScanOnce_RecordingsRoot(t *testing.T) {
	t.Run("symlinked root", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "real")
		if err := os.MkdirAll(filepath.Join(real, "TASK-1"), 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "recordings")
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		s, sink := newScanner(t, "", link)
		appendTo(t, filepath.Join(real, "TASK-1", "stream.jsonl"), "x "+tokenShape+"\n")
		s.ScanOnce()
		if got := len(sink.got()); got != 1 {
			t.Fatalf("got %d events through a symlinked root, want 1", got)
		}
	})

	t.Run("missing root warns once", func(t *testing.T) {
		logger, logged := newCaptureLogger()
		root := filepath.Join(t.TempDir(), "absent")
		s := NewLeakScanner(Config{RecordingsPath: root, Logger: logger})
		for i := 0; i < 3; i++ {
			s.ScanOnce()
		}
		out := logged()
		if n := strings.Count(out, "level=WARN"); n != 1 {
			t.Fatalf("got %d warn lines, want 1:\n%s", n, out)
		}
		if !strings.Contains(out, root) {
			t.Errorf("warning does not name the path: %s", out)
		}
		// The warning re-arms once the directory shows up and goes away again.
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		s.ScanOnce()
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		s.ScanOnce()
		if n := strings.Count(logged(), "level=WARN"); n != 2 {
			t.Errorf("got %d warn lines after the root came and went, want 2", n)
		}
	})
}

// TestScanOnce_WalkChecksContextAndSkipsIdleDirs: a cancelled context stops
// the walk, and a directory that is neither newer than the last tick nor
// recently active is not visited.
func TestScanOnce_WalkChecksContextAndSkipsIdleDirs(t *testing.T) {
	setup := func(t *testing.T) (*LeakScanner, *captureSink, string) {
		rec := filepath.Join(t.TempDir(), "recordings")
		run := filepath.Join(rec, "TASK-1")
		if err := os.MkdirAll(run, 0o700); err != nil {
			t.Fatal(err)
		}
		s, sink := newScanner(t, "", rec)
		s.walkAll = false // as after the first tick
		return s, sink, run
	}

	tests := []struct {
		name   string
		run    func(s *LeakScanner)
		idle   bool // age the directory before the scan
		events int
	}{
		{"fresh directory is scanned", func(s *LeakScanner) { s.ScanOnce() }, false, 1},
		{"idle directory is skipped", func(s *LeakScanner) { s.ScanOnce() }, true, 0},
		{"cancelled context stops the walk", func(s *LeakScanner) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			s.scanOnce(ctx)
		}, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, sink, run := setup(t)
			appendTo(t, filepath.Join(run, "stream.jsonl"), "x "+tokenShape+"\n")
			if tt.idle {
				ageFile(t, run)
			}
			tt.run(s)
			if got := len(sink.got()); got != tt.events {
				t.Fatalf("got %d events, want %d", got, tt.events)
			}
		})
	}
}

// recordingChannel collects what the alert engine delivers.
type recordingChannel struct {
	mu     sync.Mutex
	alerts []string
}

func (c *recordingChannel) Name() string { return "rec" }
func (c *recordingChannel) Type() string { return "webhook" }
func (c *recordingChannel) Send(_ context.Context, a *alerts.Alert) error {
	c.mu.Lock()
	c.alerts = append(c.alerts, a.Message)
	c.mu.Unlock()
	return nil
}

func (c *recordingChannel) got() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.alerts...)
}

// TestScanOnce_AlertsOfSameKindAndFileAreNotDeduplicated: with duplicate
// suppression on, a second leak of the same kind in the same file, found on a
// later tick, must still reach the channel; the offset in the message tells
// the two apart.
func TestScanOnce_AlertsOfSameKindAndFileAreNotDeduplicated(t *testing.T) {
	cfg := &alerts.AlertConfig{
		Enabled:  true,
		Channels: []alerts.ChannelConfig{{Name: "rec", Type: "webhook", Enabled: true}},
		Rules: []alerts.AlertRule{{
			Name: "secret_leak", Type: alerts.AlertTypeSecretLeak, Enabled: true,
			Severity: alerts.SeverityCritical, Channels: []string{"rec"},
		}},
		Defaults: alerts.AlertDefaults{SuppressDuplicates: true},
	}
	ch := &recordingChannel{}
	d := alerts.NewDispatcher(cfg)
	d.RegisterChannel(ch)
	engine := alerts.NewEngine(cfg, alerts.WithDispatcher(d))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "daemon.log")
	appendTo(t, logPath, "start\n")
	s := NewLeakScanner(Config{LogPath: logPath, Sink: engine})
	appendTo(t, logPath, "first "+tokenShape+"\n")
	s.ScanOnce()
	appendTo(t, logPath, "second "+tokenShape+"\n")
	s.ScanOnce()

	deadline := time.Now().Add(3 * time.Second)
	for len(ch.got()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := ch.got()
	if len(got) != 2 {
		t.Fatalf("got %d alerts, want 2: %v", len(got), got)
	}
	if got[0] == got[1] {
		t.Errorf("both alerts read %q", got[0])
	}
	for _, m := range got {
		if !strings.Contains(m, "offset ") {
			t.Errorf("message %q has no offset", m)
		}
	}
}
