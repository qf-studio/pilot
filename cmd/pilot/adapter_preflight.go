package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/asana"
	"github.com/qf-studio/pilot/internal/adapters/azuredevops"
	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/gitlab"
	"github.com/qf-studio/pilot/internal/adapters/jira"
	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/health/verify"
	"github.com/qf-studio/pilot/internal/logging"
)

// preflightVerifyTimeout bounds each adapter's Verify(ctx) call during
// daemon startup so an unreachable API can't hang `pilot start`.
const preflightVerifyTimeout = 8 * time.Second

// buildAdapterVerifiers resolves a verify.Verifiable for every enabled
// adapter that has a resolvable credential, skipping adapters with no token
// configured (nothing to verify — already flagged by doctor's presence
// checks). GH-3769: generalizes the GitHub-only preflight (validateGitHubToken)
// across every adapter wired up in subtasks 1-2 of GH-3760.
func buildAdapterVerifiers(cfg *config.Config) []verify.Verifiable {
	if cfg == nil || cfg.Adapters == nil {
		return nil
	}
	a := cfg.Adapters
	var verifiers []verify.Verifiable

	if a.Telegram != nil && a.Telegram.Enabled && a.Telegram.BotToken != "" {
		verifiers = append(verifiers, telegram.NewClient(a.Telegram.BotToken))
	}
	if a.Slack != nil && a.Slack.Enabled && a.Slack.BotToken != "" {
		verifiers = append(verifiers, slack.NewClient(a.Slack.BotToken))
	}
	if a.GitHub != nil && a.GitHub.Enabled {
		if token, source := resolveGitHubToken(cfg); token != "" {
			// GH-4747: registerAdapterReadiness wires this verifier into
			// /ready and calls it repeatedly for the daemon's lifetime — a
			// client built from a static token would keep re-checking the
			// boot-time credential and start reporting false-unready 401s
			// once an App-auth installation token rotates.
			verifiers = append(verifiers, github.NewVerifier(newGitHubClient(cfg), string(source)))
		}
	}
	if a.Linear != nil && a.Linear.Enabled {
		if wss := a.Linear.GetWorkspaces(); len(wss) > 0 && wss[0].APIKey != "" {
			verifiers = append(verifiers, linear.NewClient(wss[0].APIKey))
		}
	}
	if a.Jira != nil && a.Jira.Enabled && a.Jira.BaseURL != "" && a.Jira.APIToken != "" {
		verifiers = append(verifiers, jira.NewClient(a.Jira.BaseURL, a.Jira.Username, a.Jira.APIToken, a.Jira.Platform))
	}
	if a.GitLab != nil && a.GitLab.Enabled && a.GitLab.Token != "" {
		verifiers = append(verifiers, gitlab.NewClient(a.GitLab.Token, a.GitLab.Project))
	}
	if a.AzureDevOps != nil && a.AzureDevOps.Enabled && a.AzureDevOps.PAT != "" {
		verifiers = append(verifiers, azuredevops.NewClient(a.AzureDevOps.PAT, a.AzureDevOps.Organization, a.AzureDevOps.Project))
	}
	if a.Asana != nil && a.Asana.Enabled && a.Asana.AccessToken != "" {
		verifiers = append(verifiers, asana.NewClient(a.Asana.AccessToken, a.Asana.WorkspaceID))
	}

	return verifiers
}

// runAdapterPreflight calls Verify(ctx) on every verifier concurrently,
// each bounded by preflightVerifyTimeout, and returns the per-adapter
// result. A failure logs ERROR and emits an alerts.EventTypeConfigError
// event (when alertsEngine is non-nil) but never blocks startup — the
// caller is expected to ignore the returned errors for control flow.
// verify.ErrProbeNotImplemented is treated as "nothing to report" (not
// logged as an error) since it reflects an adapter with no live probe
// wired up yet, not a broken credential.
func runAdapterPreflight(ctx context.Context, verifiers []verify.Verifiable, alertsEngine *alerts.Engine) map[string]error {
	results := make(map[string]error, len(verifiers))
	if len(verifiers) == 0 {
		return results
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	log := logging.WithComponent("preflight")

	for _, v := range verifiers {
		wg.Add(1)
		go func(v verify.Verifiable) {
			defer wg.Done()

			vctx, cancel := context.WithTimeout(ctx, preflightVerifyTimeout)
			defer cancel()
			err := v.Verify(vctx)

			mu.Lock()
			results[v.Name()] = err
			mu.Unlock()

			switch {
			case err == nil:
				log.Info("adapter verified", slog.String("adapter", v.Name()))
			case errors.Is(err, verify.ErrProbeNotImplemented):
				// No live probe yet — not evidence of a broken credential.
			default:
				log.Error("adapter verification failed at startup — this adapter will silently fail until fixed",
					slog.String("adapter", v.Name()),
					slog.Any("error", err),
				)
				if alertsEngine != nil {
					alertsEngine.ProcessEvent(alerts.Event{
						Type:      alerts.EventTypeConfigError,
						Error:     fmt.Sprintf("%s adapter verification failed: %v", v.Name(), err),
						Timestamp: time.Now(),
					})
				}
			}
		}(v)
	}
	wg.Wait()

	return results
}

// registerAdapterReadiness wraps each verifier as a gateway.ReadinessChecker
// and registers it with gwServer so /ready reports real per-adapter status
// (GH-3769). No-op when gwServer is nil (gateway disabled).
func registerAdapterReadiness(gwServer *gateway.Server, verifiers []verify.Verifiable, timeout time.Duration) {
	if gwServer == nil {
		return
	}
	for _, v := range verifiers {
		gwServer.RegisterReadinessChecker(verify.NewReadinessAdapter(v, timeout))
	}
}
