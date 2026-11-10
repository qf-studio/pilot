package leakscan

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

type captureSink struct {
	mu     sync.Mutex
	events []alerts.Event
}

func (c *captureSink) ProcessEvent(e alerts.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *captureSink) got() []alerts.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]alerts.Event(nil), c.events...)
}

// Built at runtime so no secret-shaped literal sits in the source.
var (
	tokenShape = "ghp_" + strings.Repeat("a1B2c3", 6)
	configured = "cfg-secret-" + strings.Repeat("Zq9", 4)
)

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func newScanner(t *testing.T, logPath, recPath string) (*LeakScanner, *captureSink) {
	t.Helper()
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	secretpatterns.RegisterSecretValues(configured)
	sink := &captureSink{}
	return NewLeakScanner(Config{LogPath: logPath, RecordingsPath: recPath, Sink: sink}), sink
}

func TestScanOnce_Log(t *testing.T) {
	tests := []struct {
		name     string
		appended string
		wantKind string // empty: no event
	}{
		{"token shape", "level=info msg=\"token " + tokenShape + "\"\n", "ghp"},
		{"registered value", "level=warn msg=\"v=" + configured + "\"\n", secretpatterns.KindConfiguredValue},
		{"clean append", "level=info msg=\"all good\"\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "daemon.log")
			appendTo(t, logPath, "old line "+tokenShape+"\n") // before startup: ignored
			s, sink := newScanner(t, logPath, "")

			appendTo(t, logPath, tt.appended)
			s.ScanOnce()

			evs := sink.got()
			if tt.wantKind == "" {
				if len(evs) != 0 {
					t.Fatalf("clean append raised %d events", len(evs))
				}
				return
			}
			if len(evs) != 1 {
				t.Fatalf("got %d events, want 1: %+v", len(evs), evs)
			}
			ev := evs[0]
			if ev.Type != alerts.EventTypeSecurityEvent || ev.Source != "leakscan" {
				t.Errorf("type/source = %s/%s", ev.Type, ev.Source)
			}
			if ev.Metadata["kind"] != tt.wantKind || ev.Metadata["file"] != logPath {
				t.Errorf("metadata = %v", ev.Metadata)
			}
			if !strings.HasSuffix(ev.Metadata["sample"], "…") || len([]rune(ev.Metadata["sample"])) != 5 {
				t.Errorf("sample = %q, want 4 chars + ellipsis", ev.Metadata["sample"])
			}
			if ev.Metadata["offset"] == "" {
				t.Error("offset missing")
			}

			// Not re-reported on the next tick.
			s.ScanOnce()
			if n := len(sink.got()); n != 1 {
				t.Errorf("rescan raised events: total %d", n)
			}
		})
	}
}

func TestScanOnce_Recording(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "recordings")
	run := filepath.Join(rec, "TASK-1")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	s, sink := newScanner(t, "", rec)
	appendTo(t, filepath.Join(run, "stream.jsonl"), `{"text":"key `+configured+`"}`+"\n")
	appendTo(t, filepath.Join(run, "other.txt"), tokenShape) // not a scanned artifact
	s.ScanOnce()

	evs := sink.got()
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(evs), evs)
	}
	if evs[0].Metadata["kind"] != secretpatterns.KindConfiguredValue || !strings.HasSuffix(evs[0].Metadata["file"], "stream.jsonl") {
		t.Errorf("metadata = %v", evs[0].Metadata)
	}

	// An unchanged file is not rescanned; a clean append adds nothing.
	appendTo(t, filepath.Join(run, "stream.jsonl"), `{"text":"fine"}`+"\n")
	s.ScanOnce()
	if n := len(sink.got()); n != 1 {
		t.Errorf("total events = %d, want 1", n)
	}
}

func TestScanOnce_FoldsHitsPerFileAndKind(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "daemon.log")
	s, sink := newScanner(t, logPath, "")
	appendTo(t, logPath, tokenShape+"\n"+tokenShape+"\n"+configured+"\n")
	s.ScanOnce()

	evs := sink.got()
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2 (one per kind): %+v", len(evs), evs)
	}
	for _, ev := range evs {
		if ev.Metadata["kind"] == "ghp" && ev.Metadata["count"] != "2" {
			t.Errorf("ghp count = %q, want 2", ev.Metadata["count"])
		}
	}
}

func TestScanOnce_LogTruncationResetsOffset(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "daemon.log")
	appendTo(t, logPath, strings.Repeat("padding line that makes the file long\n", 20))
	s, sink := newScanner(t, logPath, "")

	if s.logOffset == 0 {
		t.Fatal("offset should start at the file end")
	}
	// Rotate: a new, shorter file that holds a leak at the top.
	if err := os.WriteFile(logPath, []byte(tokenShape+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.ScanOnce()

	if got := len(sink.got()); got != 1 {
		t.Fatalf("got %d events after truncation, want 1", got)
	}
	st, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if s.logOffset != st.Size() {
		t.Errorf("offset = %d, want %d", s.logOffset, st.Size())
	}
}

func TestScanOnce_PartialLineHeldBack(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "daemon.log")
	s, sink := newScanner(t, logPath, "")

	half := len(tokenShape) / 2
	appendTo(t, logPath, "first line\ntoken "+tokenShape[:half])
	s.ScanOnce()
	appendTo(t, logPath, tokenShape[half:]+"\n")
	s.ScanOnce()

	if got := len(sink.got()); got != 1 {
		t.Fatalf("got %d events, want 1 for a token split across ticks", got)
	}
}

func TestScanFile_SecretAcrossChunkBoundary(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	path := filepath.Join(t.TempDir(), "big.log")
	pad := strings.Repeat("x", chunkSize-10)
	appendTo(t, path, pad+tokenShape+"\n")

	hits, _, err := scanFile(path, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].count != 1 || hits[0].offset != int64(chunkSize-10) {
		t.Fatalf("hits = %+v, want one at offset %d", hits, chunkSize-10)
	}
}

// TestEvent_NeverHoldsFullValue is the masking pin: no field of the event, and
// nothing the scanner logs, may carry the full secret.
func TestEvent_NeverHoldsFullValue(t *testing.T) {
	tests := []struct{ name, secret string }{
		{"token shape", tokenShape},
		{"registered value", configured},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "daemon.log")
			s, sink := newScanner(t, logPath, "")
			appendTo(t, logPath, "leak: "+tt.secret+"\n")
			s.ScanOnce()

			evs := sink.got()
			if len(evs) != 1 {
				t.Fatalf("got %d events, want 1", len(evs))
			}
			ev := evs[0]
			text := ev.Error + "|" + ev.TaskTitle
			for k, v := range ev.Metadata {
				text += "|" + k + "=" + v
			}
			if strings.Contains(text, tt.secret) {
				t.Errorf("event holds the full value: %q", text)
			}
			if strings.Contains(text, tt.secret[:8]) {
				t.Errorf("event holds more than 4 characters of the value: %q", text)
			}
			if !strings.Contains(ev.Metadata["sample"], tt.secret[:4]+"…") {
				t.Errorf("sample = %q, want %q", ev.Metadata["sample"], tt.secret[:4]+"…")
			}
		})
	}
}

func TestHits_CountsByKind(t *testing.T) {
	before := Hits()["ghp"]
	dir := t.TempDir()
	logPath := filepath.Join(dir, "daemon.log")
	s, _ := newScanner(t, logPath, "")
	appendTo(t, logPath, tokenShape+"\n"+tokenShape+"\n")
	s.ScanOnce()
	if got := Hits()["ghp"] - before; got != 2 {
		t.Errorf("ghp hits delta = %d, want 2", got)
	}
}

func TestMaskSample(t *testing.T) {
	tests := []struct{ in, want string }{
		{"abcdefgh", "abcd…"},
		{"ab", "ab…"},
		{"日本語テキスト", "日本語テ…"},
	}
	for _, tt := range tests {
		if got := maskSample([]byte(tt.in)); got != tt.want {
			t.Errorf("maskSample(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
