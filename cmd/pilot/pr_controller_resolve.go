package main

import (
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
)

// prRegistrar is the slice of *autopilot.Controller a non-GitHub poller's
// OnPRCreated callback needs. It exists so tests can count calls.
type prRegistrar interface {
	OnPRCreated(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string)
}

// githubPRURLRe matches only a GitHub PR URL: github.com/<owner>/<repo>/pull/<n>.
var githubPRURLRe = regexp.MustCompile(`^https?://(?:www\.)?github\.com/([^/\s]+)/([^/\s]+)/pull/(\d+)(?:[/?#].*)?$`)

// parseGitHubPRURL returns owner/repo and the PR number for a GitHub PR URL.
func parseGitHubPRURL(prURL string) (repoFullName string, prNumber int, ok bool) {
	m := githubPRURLRe.FindStringSubmatch(strings.TrimSpace(prURL))
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return m[1] + "/" + m[2], n, true
}

// resolvePRController picks the autopilot controller that owns the PR a
// non-GitHub poller (Linear, GH-5584) just created. The task's project — not
// the daemon's default repo — decides which repo the PR lives in, so the repo
// is derived from prURL and looked up in ctrls (keyed by owner/repo, matched
// case-insensitively). defaultCtrl is used only when the parsed repo equals
// defaultRepo; it is never a blanket fallback. Returns the controller and the
// PR number; ctrl is nil (after one INFO line) when nothing may be registered:
// a non-GitHub URL (e.g. a GitLab merge request), a malformed URL, or a repo
// with no controller.
func resolvePRController(log *slog.Logger, taskID, prURL, defaultRepo string, defaultCtrl prRegistrar, ctrls map[string]prRegistrar) (ctrl prRegistrar, prNumber int) {
	skip := func(reason string) (prRegistrar, int) {
		log.Info("Not registering PR with autopilot",
			slog.String("task_id", taskID),
			slog.String("pr_url", prURL),
			slog.String("reason", reason),
		)
		return nil, 0
	}

	repo, n, ok := parseGitHubPRURL(prURL)
	if !ok {
		return skip("not a GitHub PR URL")
	}
	for k, c := range ctrls {
		if c != nil && strings.EqualFold(k, repo) {
			return c, n
		}
	}
	if defaultCtrl != nil && defaultRepo != "" && strings.EqualFold(defaultRepo, repo) {
		return defaultCtrl, n
	}
	return skip("no autopilot controller for repo " + repo)
}

// newLinearOnPRCreated builds the Linear poller's OnPRCreated callback (GH-5584).
// PRs are registered with the controller of the repo they were opened in, never
// the default repo's by default.
func newLinearOnPRCreated(cfg *config.Config, defaultCtrl *autopilot.Controller, ctrls map[string]*autopilot.Controller) func(sdkcore.PRCreatedEvent) {
	var def prRegistrar
	if defaultCtrl != nil {
		def = defaultCtrl
	}
	byRepo := make(map[string]prRegistrar, len(ctrls))
	for k, c := range ctrls {
		if c != nil {
			byRepo[k] = c
		}
	}
	defaultRepo := ""
	if cfg != nil && cfg.Adapters != nil && cfg.Adapters.GitHub != nil {
		defaultRepo = cfg.Adapters.GitHub.Repo
	}
	return linearOnPRCreated(defaultRepo, def, byRepo)
}

func linearOnPRCreated(defaultRepo string, def prRegistrar, byRepo map[string]prRegistrar) func(sdkcore.PRCreatedEvent) {
	return func(prEv sdkcore.PRCreatedEvent) {
		ctrl, n := resolvePRController(logging.WithComponent("linear"), prEv.IssueID, prEv.PRURL, defaultRepo, def, byRepo)
		if ctrl == nil {
			return
		}
		ctrl.OnPRCreated(n, prEv.PRURL, 0, prEv.HeadSHA, prEv.BranchName, "")
	}
}
