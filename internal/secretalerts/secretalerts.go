// Package secretalerts routes GitHub secret-scanning alerts to a person
// (GH-6264).
//
// GitHub found the leaked Telegram bot token on 2026-04-07 and kept the alert
// open for seven months, because nothing routed it anywhere. The watcher polls
// the open alerts of every configured GitHub repo and sends one security_event
// per alert number it has not seen before. An alert counts as seen only after
// the sink accepted its event, so a refused or dropped event is retried on the
// next tick. Seen numbers persist per repo with the alert's updated_at in a
// 0600 JSON file, so a restart does not resend, and an alert that is reopened
// (newer updated_at) fires again. Numbers that are no longer open leave the file.
//
// The API's "secret" field is never decoded (see github.SecretScanningAlert),
// so the live credential cannot reach an event, a log line or the seen file.
package secretalerts

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/alerts"
)

const (
	// DefaultInterval is the poll period when Config.Interval is unset.
	DefaultInterval = time.Hour

	// SeenFileName is the seen-numbers file inside the Pilot data directory.
	SeenFileName = "secret-alerts-seen.json"

	// permissionHint tells the operator what a 403/404 needs.
	permissionHint = "grant secret_scanning_alerts: read (GitHub App) or the security_events / repo scope (token), and enable secret scanning on the repository"
)

// unavailableEventEvery is the minimum gap between the security_events that
// report a 403/404 for one repo, so the gap is visible outside the daemon log
// without paging every poll.
const unavailableEventEvery = 7 * 24 * time.Hour

// submitTimeout bounds one sink submission.
const submitTimeout = 10 * time.Second

// Lister lists a repository's secret-scanning alerts. *github.Client
// implements it. capped is true when pagination stopped at the page cap, so the
// list is partial.
type Lister interface {
	ListSecretScanningAlertsCapped(ctx context.Context, owner, repo, state string) (list []*github.SecretScanningAlert, capped bool, err error)
}

// EventSink receives the security events. *alerts.Engine implements it.
// SubmitEvent reports whether the event was accepted; a false return makes the
// watcher retry the alert on the next tick.
type EventSink interface {
	SubmitEvent(ctx context.Context, event alerts.Event) bool
}

// Repo names one GitHub repository to watch.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Config configures a Watcher.
type Config struct {
	// Lister serves every repo that ListerFor does not cover.
	Lister Lister
	// ListerFor, when set, returns the lister for one repo (nil falls back to
	// Lister), so each repo is polled with its own resolved client.
	ListerFor func(Repo) Lister
	Sink      EventSink
	Repos     []Repo
	// SeenPath is the seen-numbers JSON file. Empty keeps the seen set in
	// memory only (a restart resends).
	SeenPath string
	// Interval between polls; zero means DefaultInterval.
	Interval time.Duration
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Now defaults to time.Now; tests override it for the daily Warn.
	Now func() time.Time
}

// Watcher polls secret-scanning alerts and sends an event per new one.
type Watcher struct {
	cfg Config
	log *slog.Logger

	mu        sync.Mutex
	seen      map[string]map[int]time.Time // repo -> alert number -> updated_at when its event was accepted
	loaded    bool
	warnedDay map[string]string    // repo -> UTC day of the last permission Warn
	eventedAt map[string]time.Time // repo -> last accepted "unavailable" security_event
}

// NewWatcher builds a watcher. The seen file is read on the first poll.
func NewWatcher(cfg Config) *Watcher {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{
		cfg:       cfg,
		log:       log.With(slog.String("component", "secretalerts")),
		seen:      make(map[string]map[int]time.Time),
		warnedDay: make(map[string]string),
		eventedAt: make(map[string]time.Time),
	}
}

// Run polls immediately and then every Interval until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	w.PollOnce(ctx)
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.PollOnce(ctx)
		}
	}
}

// PollOnce lists the open alerts of every repo and sends an event for each
// number not seen before. A failing repo never stops the others.
func (w *Watcher) PollOnce(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()
	for _, repo := range w.cfg.Repos {
		if ctx.Err() != nil {
			return
		}
		w.pollRepo(ctx, repo)
	}
}

func (w *Watcher) listerFor(repo Repo) Lister {
	if w.cfg.ListerFor != nil {
		if l := w.cfg.ListerFor(repo); l != nil {
			return l
		}
	}
	return w.cfg.Lister
}

func (w *Watcher) pollRepo(ctx context.Context, repo Repo) {
	key := repo.String()
	list, capped, err := w.listerFor(repo).ListSecretScanningAlertsCapped(ctx, repo.Owner, repo.Name, "open")
	if err != nil {
		w.logListError(ctx, key, err)
		return
	}
	delete(w.warnedDay, key) // the repo answers again: warn afresh if it stops
	if capped {
		w.log.Warn("secret scanning alert list stopped at the page cap; later alerts are not watched",
			slog.String("repo", key), slog.Int("listed", len(list)))
	}

	seen := w.seen[key]
	if seen == nil {
		seen = make(map[int]time.Time)
		w.seen[key] = seen
	}
	changed := false
	open := make(map[int]bool, len(list))
	for _, a := range list {
		if a == nil {
			continue
		}
		open[a.Number] = true
		prev, known := seen[a.Number]
		if known && prev.IsZero() {
			// Seen before updated_at was stored: adopt it without a resend.
			if !a.UpdatedAt.IsZero() {
				seen[a.Number] = a.UpdatedAt
				changed = true
			}
			continue
		}
		if known && !a.UpdatedAt.After(prev) {
			continue
		}
		// Seen only after the sink accepted the event: a refused or dropped
		// event stays unseen and goes out again on the next tick (GH-6273).
		if !w.send(ctx, key, a, known) {
			continue
		}
		seen[a.Number] = a.UpdatedAt
		changed = true
	}
	// A resolved alert leaves the file. A capped list is partial, so it proves
	// nothing about the numbers it lacks.
	if !capped {
		for n := range seen {
			if !open[n] {
				delete(seen, n)
				changed = true
			}
		}
	}
	if changed {
		w.save()
	}
}

// logListError logs a 403/404 (missing permission, scanning off) once per repo
// per day with the permission hint, and raises one security_event per repo per
// week so the gap shows outside the daemon log; any other failure is logged
// every poll.
func (w *Watcher) logListError(ctx context.Context, key string, err error) {
	var apiErr *github.APIError
	if !errors.As(err, &apiErr) || (apiErr.StatusCode != http.StatusForbidden && apiErr.StatusCode != http.StatusNotFound) {
		w.log.Warn("list secret scanning alerts failed",
			slog.String("repo", key), slog.Any("error", err))
		return
	}
	now := w.cfg.Now()
	if day := now.UTC().Format("2006-01-02"); w.warnedDay[key] != day {
		w.warnedDay[key] = day
		w.log.Warn("secret scanning alerts unavailable",
			slog.String("repo", key),
			slog.Int("status", apiErr.StatusCode),
			slog.String("hint", permissionHint))
	}
	if last, ok := w.eventedAt[key]; ok && now.Sub(last) < unavailableEventEvery {
		return
	}
	if w.submit(ctx, alerts.Event{
		Type:   alerts.EventTypeSecurityEvent,
		Source: alerts.SecurityEventSourceGitHubSecretScanning,
		Error: "GitHub secret scanning alerts unavailable for " + key +
			" (HTTP " + strconv.Itoa(apiErr.StatusCode) + "): " + permissionHint,
		Metadata: map[string]string{
			"repo":   key,
			"status": strconv.Itoa(apiErr.StatusCode),
			"hint":   permissionHint,
		},
		Timestamp: now,
	}) {
		w.eventedAt[key] = now
	}
}

// send raises the event for one alert and reports whether the sink accepted it.
func (w *Watcher) send(ctx context.Context, key string, a *github.SecretScanningAlert, reopened bool) bool {
	w.log.Warn("new secret scanning alert",
		slog.String("repo", key),
		slog.Int("number", a.Number),
		slog.String("type", a.SecretType),
		slog.String("url", a.HTMLURL),
		slog.Bool("reopened", reopened))
	accepted := w.submit(ctx, alerts.Event{
		Type:   alerts.EventTypeSecurityEvent,
		Source: alerts.SecurityEventSourceGitHubSecretScanning,
		Error:  "GitHub secret scanning alert #" + strconv.Itoa(a.Number) + " in " + key,
		Metadata: map[string]string{
			"repo":       key,
			"number":     strconv.Itoa(a.Number),
			"type":       a.SecretType,
			"created_at": a.CreatedAt.UTC().Format(time.RFC3339),
			"html_url":   a.HTMLURL,
		},
		Timestamp: w.cfg.Now(),
	})
	if !accepted {
		w.log.Warn("secret scanning alert event not accepted; retrying next poll",
			slog.String("repo", key), slog.Int("number", a.Number))
	}
	return accepted
}

// submit hands an event to the sink; a nil sink accepts nothing.
func (w *Watcher) submit(ctx context.Context, event alerts.Event) bool {
	if w.cfg.Sink == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	return w.cfg.Sink.SubmitEvent(ctx, event)
}

// load reads the seen file once. A missing or unreadable file leaves the set
// empty, so every currently open alert counts as new.
func (w *Watcher) load() {
	if w.loaded {
		return
	}
	w.loaded = true
	if w.cfg.SeenPath == "" {
		return
	}
	data, err := os.ReadFile(w.cfg.SeenPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			w.log.Warn("read secret alert seen file failed", slog.String("file", w.cfg.SeenPath), slog.Any("error", err))
		}
		return
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(data, &stored); err != nil {
		w.log.Warn("parse secret alert seen file failed", slog.String("file", w.cfg.SeenPath), slog.Any("error", err))
		return
	}
	for repo, raw := range stored {
		m := make(map[int]time.Time)
		var current map[string]time.Time
		if err := json.Unmarshal(raw, &current); err == nil {
			for num, updated := range current {
				if n, err := strconv.Atoi(num); err == nil {
					m[n] = updated
				}
			}
		} else {
			// Legacy format: a bare list of numbers, no updated_at.
			var nums []int
			if err := json.Unmarshal(raw, &nums); err != nil {
				w.log.Warn("parse secret alert seen entry failed", slog.String("repo", repo), slog.Any("error", err))
				continue
			}
			for _, n := range nums {
				m[n] = time.Time{}
			}
		}
		w.seen[repo] = m
	}
}

// save writes the seen set atomically with mode 0600.
func (w *Watcher) save() {
	path := w.cfg.SeenPath
	if path == "" {
		return
	}
	stored := make(map[string]map[string]time.Time, len(w.seen))
	for repo, m := range w.seen {
		entries := make(map[string]time.Time, len(m))
		for n, updated := range m {
			entries[strconv.Itoa(n)] = updated
		}
		stored[repo] = entries
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		w.log.Warn("encode secret alert seen file failed", slog.Any("error", err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		w.log.Warn("create secret alert seen dir failed", slog.Any("error", err))
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		w.log.Warn("write secret alert seen file failed", slog.String("file", path), slog.Any("error", err))
		return
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		w.log.Warn("chmod secret alert seen file failed", slog.String("file", path), slog.Any("error", err))
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		w.log.Warn("save secret alert seen file failed", slog.String("file", path), slog.Any("error", err))
	}
}
