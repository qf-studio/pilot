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
	mu      sync.Mutex
	events  []alerts.Event
	refuse  bool // SubmitEvent returns false and records nothing
	refused int
}

func (s *sinkRecorder) SubmitEvent(_ context.Context, e alerts.Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse {
		s.refused++
		return false
	}
	s.events = append(s.events, e)
	return true
}

func (s *sinkRecorder) setRefuse(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = v
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
	updated map[int]string // number -> updated_at; default baseUpdated
	status  int            // non-zero: answer every request with it
}

const baseUpdated = "2026-04-07T10:00:00Z"

func (f *fakeGitHub) set(numbers ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.numbers = numbers
}

func (f *fakeGitHub) setUpdated(number int, ts string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updated == nil {
		f.updated = make(map[int]string)
	}
	f.updated[number] = ts
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
		updated := baseUpdated
		if u, ok := f.updated[n]; ok {
			updated = u
		}
		items = append(items, fmt.Sprintf(
			`{"number":%d,"secret_type":"telegram_bot_token","secret_type_display_name":"Telegram Bot Token","secret":%q,"created_at":"2026-04-07T10:00:00Z","updated_at":%q,"html_url":"https://github.com/o/r/security/secret-scanning/%d","push_protection_bypassed":false}`,
			n, fakeSecretValue, updated, n))
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
	// One security_event per repo per week carries the gap beyond the log.
	if got := h.sink.count(); got != 1 {
		t.Fatalf("403: %d events, want 1", got)
	}
	ev := h.sink.events[0]
	if ev.Type != alerts.EventTypeSecurityEvent || ev.Source != alerts.SecurityEventSourceGitHubSecretScanning {
		t.Errorf("403 event type/source = %s/%s", ev.Type, ev.Source)
	}
	if ev.Metadata["repo"] != "o/r" || ev.Metadata["status"] != "403" || !strings.Contains(ev.Error, "secret_scanning_alerts: read") {
		t.Errorf("403 event = %+v", ev)
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
	if got := h.sink.count(); got != 1 {
		t.Errorf("next day: %d events, want still 1 (weekly)", got)
	}

	now = now.Add(7 * 24 * time.Hour)
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 2 {
		t.Errorf("a week later: %d events, want 2", got)
	}

	// The loop is not failed: once access returns, alerts flow.
	h.fake.status = 0
	h.fake.set(5)
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 3 {
		t.Errorf("after recovery: %d events, want 3", got)
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

// TestSecretAlert_RefusingSinkNotMarkedSeen: an event the sink refuses (full
// queue, alerts engine down) leaves the alert unseen, so the next tick sends it.
func TestSecretAlert_RefusingSinkNotMarkedSeen(t *testing.T) {
	h := newHarness(t)
	h.fake.set(1, 2)
	w := h.watcher(nil)
	ctx := context.Background()

	h.sink.setRefuse(true)
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 0 {
		t.Fatalf("refused: %d events recorded, want 0", got)
	}
	if h.sink.refused != 2 {
		t.Fatalf("refused submissions = %d, want 2", h.sink.refused)
	}
	if data, err := os.ReadFile(h.seen); err == nil && strings.Contains(string(data), `"1"`) {
		t.Errorf("refused alert written to the seen file: %s", data)
	}

	h.sink.setRefuse(false)
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 2 {
		t.Fatalf("next tick: %d events, want 2", got)
	}
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 2 {
		t.Fatalf("after acceptance: %d events, want still 2", got)
	}
}

// TestSecretAlert_NilSinkNotMarkedSeen: with no sink nothing is delivered and
// nothing is remembered, so a later watcher on the same file still sends.
func TestSecretAlert_NilSinkNotMarkedSeen(t *testing.T) {
	h := newHarness(t)
	h.fake.set(1)
	ctx := context.Background()

	NewWatcher(Config{
		Lister:   github.NewClientWithBaseURL("test-github-token", h.srv.URL),
		Repos:    []Repo{{Owner: "o", Name: "r"}},
		SeenPath: h.seen,
		Logger:   slog.New(slog.NewTextHandler(h.logs, nil)),
	}).PollOnce(ctx)

	h.watcher(nil).PollOnce(ctx)
	if got := h.sink.count(); got != 1 {
		t.Fatalf("after nil-sink poll: %d events, want 1", got)
	}
}

// TestSecretAlert_ReopenedAlertFiresAgain: an open alert whose updated_at moved
// past the stored one is sent again; an unchanged one is not.
func TestSecretAlert_ReopenedAlertFiresAgain(t *testing.T) {
	h := newHarness(t)
	h.fake.set(1)
	w := h.watcher(nil)
	ctx := context.Background()

	w.PollOnce(ctx)
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 1 {
		t.Fatalf("unchanged: %d events, want 1", got)
	}

	h.fake.setUpdated(1, "2026-11-10T09:00:00Z")
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 2 {
		t.Fatalf("reopened: %d events, want 2", got)
	}
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 2 {
		t.Fatalf("reopened, next tick: %d events, want still 2", got)
	}

	// The new updated_at survives a restart.
	h.watcher(nil).PollOnce(ctx)
	if got := h.sink.count(); got != 2 {
		t.Fatalf("after restart: %d events, want still 2", got)
	}
}

// TestSecretAlert_ResolvedAlertLeavesSeenFile: a number that is no longer open
// is dropped from the file, so the file does not grow.
func TestSecretAlert_ResolvedAlertLeavesSeenFile(t *testing.T) {
	h := newHarness(t)
	h.fake.set(1, 2)
	w := h.watcher(nil)
	ctx := context.Background()

	w.PollOnce(ctx)
	h.fake.set(2)
	w.PollOnce(ctx)

	data, err := os.ReadFile(h.seen)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]map[string]string
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("seen file: %v\n%s", err, data)
	}
	if _, ok := stored["o/r"]["1"]; ok {
		t.Errorf("resolved alert 1 still in the seen file: %s", data)
	}
	if _, ok := stored["o/r"]["2"]; !ok {
		t.Errorf("open alert 2 missing from the seen file: %s", data)
	}

	// A resolved alert that returns (reopened) is new again.
	h.fake.set(1, 2)
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 3 {
		t.Errorf("returning alert: %d events, want 3", got)
	}
}

// TestSecretAlert_LegacySeenFileAdoptedWithoutResend: the old file format (a
// bare list of numbers) counts as seen and is upgraded in place.
func TestSecretAlert_LegacySeenFileAdoptedWithoutResend(t *testing.T) {
	h := newHarness(t)
	h.fake.set(1, 2)
	if err := os.MkdirAll(filepath.Dir(h.seen), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.seen, []byte(`{"o/r":[1,2]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w := h.watcher(nil)
	ctx := context.Background()

	w.PollOnce(ctx)
	if got := h.sink.count(); got != 0 {
		t.Fatalf("legacy file: %d events, want 0", got)
	}
	data, err := os.ReadFile(h.seen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), baseUpdated) {
		t.Errorf("legacy entries not upgraded with updated_at: %s", data)
	}
	h.fake.setUpdated(2, "2026-11-10T09:00:00Z")
	w.PollOnce(ctx)
	if got := h.sink.count(); got != 1 {
		t.Errorf("reopen after upgrade: %d events, want 1", got)
	}
}

// pagedLister serves a fixed list and a capped flag.
type pagedLister struct {
	list   []*github.SecretScanningAlert
	capped bool
	calls  int
}

func (p *pagedLister) ListSecretScanningAlertsCapped(context.Context, string, string, string) ([]*github.SecretScanningAlert, bool, error) {
	p.calls++
	return p.list, p.capped, nil
}

// TestSecretAlert_PageCapWarnsAndKeepsSeen: a capped list is partial, so it
// logs one Warn and prunes nothing.
func TestSecretAlert_PageCapWarnsAndKeepsSeen(t *testing.T) {
	h := newHarness(t)
	l := &pagedLister{list: []*github.SecretScanningAlert{{Number: 1}, {Number: 2}}}
	w := NewWatcher(Config{
		Lister:   l,
		Sink:     h.sink,
		Repos:    []Repo{{Owner: "o", Name: "r"}},
		SeenPath: h.seen,
		Logger:   slog.New(slog.NewTextHandler(h.logs, nil)),
	})
	ctx := context.Background()

	w.PollOnce(ctx)
	l.list = l.list[:1]
	l.capped = true
	h.logs.Reset()
	w.PollOnce(ctx)

	if n := strings.Count(h.logs.String(), "page cap"); n != 1 {
		t.Errorf("page cap warns = %d, want 1\n%s", n, h.logs.String())
	}
	if _, ok := w.seen["o/r"][2]; !ok {
		t.Error("alert 2 pruned from a capped (partial) list")
	}
}

// TestSecretAlert_ListerForPerRepo: each repo is polled with its own lister.
func TestSecretAlert_ListerForPerRepo(t *testing.T) {
	h := newHarness(t)
	a := &pagedLister{list: []*github.SecretScanningAlert{{Number: 1}}}
	b := &pagedLister{list: []*github.SecretScanningAlert{{Number: 7}}}
	byRepo := map[Repo]Lister{{Owner: "o", Name: "a"}: a, {Owner: "o", Name: "b"}: b}
	w := NewWatcher(Config{
		ListerFor: func(r Repo) Lister { return byRepo[r] },
		Sink:      h.sink,
		Repos:     []Repo{{Owner: "o", Name: "a"}, {Owner: "o", Name: "b"}},
		Logger:    slog.New(slog.NewTextHandler(h.logs, nil)),
	})
	w.PollOnce(context.Background())
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("calls a=%d b=%d, want 1 each", a.calls, b.calls)
	}
	if got := h.sink.count(); got != 2 {
		t.Fatalf("%d events, want 2", got)
	}
	if h.sink.events[1].Metadata["repo"] != "o/b" || h.sink.events[1].Metadata["number"] != "7" {
		t.Errorf("second event = %+v", h.sink.events[1].Metadata)
	}
}
