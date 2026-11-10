package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/secretalerts"
)

// secretAlertRepos lists the GitHub repos to watch: every project with a
// github owner/repo plus the adapters.github.repo default, deduplicated.
func secretAlertRepos(cfg *config.Config) []secretalerts.Repo {
	var repos []secretalerts.Repo
	seen := make(map[secretalerts.Repo]bool)
	add := func(owner, name string) {
		r := secretalerts.Repo{Owner: owner, Name: name}
		if owner == "" || name == "" || seen[r] {
			return
		}
		seen[r] = true
		repos = append(repos, r)
	}
	for _, proj := range cfg.Projects {
		if proj != nil && proj.GitHub != nil {
			add(proj.GitHub.Owner, proj.GitHub.Repo)
		}
	}
	if cfg.Adapters != nil && cfg.Adapters.GitHub != nil {
		if parts := strings.SplitN(cfg.Adapters.GitHub.Repo, "/", 2); len(parts) == 2 {
			add(parts[0], parts[1])
		}
	}
	return repos
}

// secretAlertSeenPath is the seen-numbers file under the Pilot data directory
// (memory.path); empty when no data directory is configured.
func secretAlertSeenPath(cfg *config.Config) string {
	if cfg.Memory == nil || cfg.Memory.Path == "" {
		return ""
	}
	return filepath.Join(cfg.Memory.Path, secretalerts.SeenFileName)
}

// startSecretAlertWatch runs the GitHub secret-scanning alert watcher in the
// background (GH-6264). GitHub kept the alert for a leaked bot token open for
// seven months because nothing routed it to a person.
//
// The watcher needs the GitHub adapter and an alerts engine: without an engine
// every event would be lost, so it does not start and says why in one Warn line
// (GH-6273).
func startSecretAlertWatch(ctx context.Context, cfg *config.Config, alertsEngine *alerts.Engine) {
	startSecretAlertWatchWith(ctx, cfg, alertsEngine, logging.WithComponent("secretalerts"))
}

// startSecretAlertWatchWith is startSecretAlertWatch with the logger passed in.
// It reports whether the watcher started. main calls it on the polling and the
// gateway-only path alike, so the missing-engine Warn shows in both (GH-6279).
func startSecretAlertWatchWith(ctx context.Context, cfg *config.Config, alertsEngine *alerts.Engine, log *slog.Logger) bool {
	if !cfg.GitHubAlertWatchEnabled() {
		return false
	}
	if cfg.Adapters == nil || cfg.Adapters.GitHub == nil || !cfg.Adapters.GitHub.Enabled {
		return false
	}
	repos := secretAlertRepos(cfg)
	if len(repos) == 0 {
		return false
	}
	if token, _ := resolveGitHubToken(cfg); token == "" {
		return false
	}
	if alertsEngine == nil {
		log.Warn("github secret alert watch not started: no alerts engine (alerts disabled or the engine failed to start), so an alert could not be delivered",
			slog.Int("repos", len(repos)))
		return false
	}
	// One client per repo, resolved the way the poller resolves its own
	// (newGitHubClient re-reads the token on every request). Config holds one
	// GitHub credential today, so every client resolves the same token; the
	// per-repo seam keeps a future per-project credential out of the watcher.
	clients := make(map[secretalerts.Repo]secretalerts.Lister, len(repos))
	for _, r := range repos {
		clients[r] = newGitHubClient(cfg)
	}
	watcher := secretalerts.NewWatcher(secretalerts.Config{
		ListerFor: func(r secretalerts.Repo) secretalerts.Lister { return clients[r] },
		Sink:      alertsEngine,
		Repos:     repos,
		SeenPath:  secretAlertSeenPath(cfg),
		Interval:  cfg.GitHubAlertWatchInterval(),
		Logger:    log,
	})
	logging.SafeGo("secretalerts", func() { watcher.Run(ctx) })
	logging.WithComponent("start").Info("github secret alert watch started",
		slog.Duration("interval", cfg.GitHubAlertWatchInterval()),
		slog.Int("repos", len(repos)))
	return true
}
