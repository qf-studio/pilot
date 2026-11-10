// Package secretalerts routes GitHub secret-scanning alerts to a person
// (GH-6264).
//
// GitHub found the leaked Telegram bot token on 2026-04-07 and kept the alert
// open for seven months, because nothing routed it anywhere. The watcher polls
// the open alerts of every configured GitHub repo and sends one security_event
// per alert number it has not seen before. Seen numbers persist per repo in a
// 0600 JSON file, so a restart does not resend.
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
	"sort"
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

// Lister lists a repository's secret-scanning alerts. *github.Client
// implements it.
type Lister interface {
	ListSecretScanningAlerts(ctx context.Context, owner, repo, state string) ([]*github.SecretScanningAlert, error)
}

// EventSink receives the security events. *alerts.Engine implements it.
type EventSink interface {
	ProcessEvent(alerts.Event)
}

// Repo names one GitHub repository to watch.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Config configures a Watcher.
type Config struct {
	Lister Lister
	Sink   EventSink
	Repos  []Repo
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
	seen      map[string]map[int]bool // repo -> alert numbers already sent
	loaded    bool
	warnedDay map[string]string // repo -> UTC day of the last permission Warn
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
		seen:      make(map[string]map[int]bool),
		warnedDay: make(map[string]string),
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

func (w *Watcher) pollRepo(ctx context.Context, repo Repo) {
	key := repo.String()
	list, err := w.cfg.Lister.ListSecretScanningAlerts(ctx, repo.Owner, repo.Name, "open")
	if err != nil {
		w.logListError(key, err)
		return
	}
	delete(w.warnedDay, key) // the repo answers again: warn afresh if it stops

	seen := w.seen[key]
	if seen == nil {
		seen = make(map[int]bool)
		w.seen[key] = seen
	}
	changed := false
	for _, a := range list {
		if a == nil || seen[a.Number] {
			continue
		}
		w.send(key, a)
		seen[a.Number] = true
		changed = true
	}
	if changed {
		w.save()
	}
}

// logListError logs a 403/404 (missing permission, scanning off) once per repo
// per day with the permission hint; any other failure is logged every poll.
func (w *Watcher) logListError(key string, err error) {
	var apiErr *github.APIError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusForbidden || apiErr.StatusCode == http.StatusNotFound) {
		day := w.cfg.Now().UTC().Format("2006-01-02")
		if w.warnedDay[key] == day {
			return
		}
		w.warnedDay[key] = day
		w.log.Warn("secret scanning alerts unavailable",
			slog.String("repo", key),
			slog.Int("status", apiErr.StatusCode),
			slog.String("hint", permissionHint))
		return
	}
	w.log.Warn("list secret scanning alerts failed",
		slog.String("repo", key), slog.Any("error", err))
}

func (w *Watcher) send(key string, a *github.SecretScanningAlert) {
	w.log.Warn("new secret scanning alert",
		slog.String("repo", key),
		slog.Int("number", a.Number),
		slog.String("type", a.SecretType),
		slog.String("url", a.HTMLURL))
	if w.cfg.Sink == nil {
		return
	}
	w.cfg.Sink.ProcessEvent(alerts.Event{
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
	var stored map[string][]int
	if err := json.Unmarshal(data, &stored); err != nil {
		w.log.Warn("parse secret alert seen file failed", slog.String("file", w.cfg.SeenPath), slog.Any("error", err))
		return
	}
	for repo, nums := range stored {
		m := make(map[int]bool, len(nums))
		for _, n := range nums {
			m[n] = true
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
	stored := make(map[string][]int, len(w.seen))
	for repo, m := range w.seen {
		nums := make([]int, 0, len(m))
		for n := range m {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		stored[repo] = nums
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
