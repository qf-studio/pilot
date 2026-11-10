package secretalerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/alerts"
)

const fakeSecretValue = "fake-secret-value-not-real"

type sinkRecorder struct {
	mu     sync.Mutex
	events []alerts.Event
}

func (s *sinkRecorder) ProcessEvent(e alerts.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *sinkRecorder) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// fakeGitHub serves secret-scanning alerts; its response always carries the
// "secret" field the watcher must never surface.
type fakeGitHub struct {
	mu      sync.Mutex
	numbers []int
	status  int // non-zero: answer every request with it
}

func (f *fakeGitHub) set(numbers ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.numbers = numbers
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		return
	}
	var items []string
	for _, n := range f.numbers {
		items = append(items, fmt.Sprintf(
			`{"number":%d,"secret_type":"telegram_bot_token","secret_type_display_name":"Telegram Bot Token","secret":%q,"created_at":"2026-04-07T10:00:00Z","html_url":"https://github.com/o/r/security/secret-scanning/%d","push_protection_bypassed":false}`,
			n, fakeSecretValue, n))
	}
	_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
}

type harness struct {
	fake *fakeGitHub
	srv  *httptest.Server
	sink *sinkRecorder
	logs *bytes.Buffer
	seen string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := &fakeGitHub{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return &harness{
		fake: fake,
		srv:  srv,
		sink: &sinkRecorder{},
		logs: &bytes.Buffer{},
		seen: filepath.Join(t.TempDir(), "data", SeenFileName),
	}
}

func (h *harness) watcher(now func() time.Time) *Watcher {
	return NewWatcher(Config{
		Lister:   github.NewClientWithBaseURL("test-github-token", h.srv.URL),
		Sink:     h.sink,
		Repos:    []Repo{{Owner: "o", Name: "r"}},
		SeenPath: h.seen,
		Logger:   slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:      now,
	})
}

func TestSecretAlert_NewAlertsSentOnceThenQuiet(t *testing.T) {
	h := newHarness(t)
	h.fake.set(1, 2)
	w := h.watcher(nil)
	ctx := context.Background()

	w.PollOnce(ctx)
	if got := h.sink.count(); got != 2 {
		t.Fatalf("first poll: %d events, want 2", got)
	}
	ev := h.sink.events[0]
	if ev.Type != alerts.EventTypeSecurityEvent || ev.Source != "github_secret_scanning" {
		t.Errorf("event type/source = %s/%s", ev.Type, ev.Source)
	}
	want := map[string]string{
		"repo":       "o/r",
		"number":     "1",
		"type":       "telegram_bot_token",
		"created_at": "2026-04-07T10:00:00Z",
		"html_url":   "https://github.com/o/r/security/secret-scanning/1",
	}
	for k, v := range want {
		if ev.Metadata[k] != v {
			t.Errorf("metadata[%s] = %q, want %q", k, ev.Metadata[k], v)
		}
	}

	w.PollOnce(ctx)
	if got := h.sink.count(); got != 2 {
		t.Fatalf("second poll: %d events, want still 2", got)
	}

	h.fake.set(1, 2, 3)
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 3 {
		t.Fatalf("new alert: %d events, want 3", got)
	}
	if n := h.sink.events[2].Metadata["number"]; n != "3" {
		t.Errorf("third event number = %s, want 3", n)
	}
}

func TestSecretAlert_RestartDoesNotResend(t *testing.T) {
	h := newHarness(t)
	h.fake.set(1, 2)
	ctx := context.Background()

	h.watcher(nil).PollOnce(ctx)
	if got := h.sink.count(); got != 2 {
		t.Fatalf("first run: %d events, want 2", got)
	}

	st, err := os.Stat(h.seen)
	if err != nil {
		t.Fatalf("seen file not written: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("seen file mode = %o, want 600", perm)
	}

	// A new watcher on the same file is a restarted daemon.
	restarted := h.watcher(nil)
	restarted.PollOnce(ctx)
	if got := h.sink.count(); got != 2 {
		t.Fatalf("after restart: %d events, want still 2", got)
	}

	h.fake.set(1, 2, 7)
	restarted.PollOnce(ctx)
	if got := h.sink.count(); got != 3 {
		t.Fatalf("new alert after restart: %d events, want 3", got)
	}
}

func TestSecretAlert_ForbiddenWarnsOncePerDay(t *testing.T) {
	h := newHarness(t)
	h.fake.status = http.StatusForbidden
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	w := h.watcher(func() time.Time { return now })
	ctx := context.Background()

	w.PollOnce(ctx)
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 0 {
		t.Fatalf("403: %d events, want 0", got)
	}
	if n := strings.Count(h.logs.String(), "level=WARN"); n != 1 {
		t.Fatalf("same day: %d warns, want 1\n%s", n, h.logs.String())
	}
	for _, want := range []string{"repo=o/r", "secret_scanning_alerts: read", "security_events"} {
		if !strings.Contains(h.logs.String(), want) {
			t.Errorf("warn missing %q:\n%s", want, h.logs.String())
		}
	}

	now = now.Add(24 * time.Hour)
	w.PollOnce(ctx)
	if n := strings.Count(h.logs.String(), "level=WARN"); n != 2 {
		t.Errorf("next day: %d warns, want 2", n)
	}

	// The loop is not failed: once access returns, alerts flow.
	h.fake.status = 0
	h.fake.set(5)
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 1 {
		t.Errorf("after recovery: %d events, want 1", got)
	}
}

// TestSecretAlert_SecretFieldNeverAppears runs the real GitHub client against
// a server that returns the live "secret" value and asserts it reaches no
// event, no log line (the watcher's or the process default) and no file.
func TestSecretAlert_SecretFieldNeverAppears(t *testing.T) {
	h := newHarness(t)
	h.fake.set(1, 2)

	// Capture the process default logger too, so a log line added anywhere on
	// the request path (client included) is seen.
	var global bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&global, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h.watcher(nil).PollOnce(context.Background())

	events, err := json.Marshal(h.sink.events)
	if err != nil {
		t.Fatal(err)
	}
	seenFile, err := os.ReadFile(h.seen)
	if err != nil {
		t.Fatal(err)
	}
	if h.sink.count() != 2 {
		t.Fatalf("%d events, want 2", h.sink.count())
	}
	for name, got := range map[string]string{
		"events":      string(events),
		"watcher log": h.logs.String(),
		"default log": global.String(),
		"seen file":   string(seenFile),
	} {
		if strings.Contains(got, fakeSecretValue) {
			t.Errorf("secret value found in %s: %s", name, got)
		}
	}
}
