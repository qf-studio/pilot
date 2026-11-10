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
func startSecretAlertWatch(ctx context.Context, cfg *config.Config, alertsEngine *alerts.Engine) {
	if !cfg.GitHubAlertWatchEnabled() {
		return
	}
	repos := secretAlertRepos(cfg)
	if len(repos) == 0 {
		return
	}
	if token, _ := resolveGitHubToken(cfg); token == "" {
		return
	}
	wcfg := secretalerts.Config{
		Lister:   newGitHubClient(cfg),
		Repos:    repos,
		SeenPath: secretAlertSeenPath(cfg),
		Interval: cfg.GitHubAlertWatchInterval(),
		Logger:   logging.WithComponent("secretalerts"),
	}
	if alertsEngine != nil {
		wcfg.Sink = alertsEngine // keep a nil engine out of the interface
	}
	watcher := secretalerts.NewWatcher(wcfg)
	logging.SafeGo("secretalerts", func() { watcher.Run(ctx) })
	logging.WithComponent("start").Info("github secret alert watch started",
		slog.Duration("interval", wcfg.Interval),
		slog.Int("repos", len(repos)))
}
