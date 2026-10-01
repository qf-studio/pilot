package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	linearSDK "github.com/qf-studio/studio-sdk/sdk/integrations/linear"

	"github.com/qf-studio/pilot/internal/adapters/skipreason"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
)

// linearStatusLabels are the pilot-* labels the SDK poller creates/uses for
// lifecycle tracking (see studio-sdk's Poller.cacheLabelIDs). They degrade to
// Warn-and-continue mid-poll if misconfigured, unlike the trigger label.
var linearStatusLabels = []string{"pilot-in-progress", "pilot-done", "pilot-failed"}

// linearLabelClassifier abstracts studio-sdk's *linear.Client.ClassifyLabel
// so the preflight can be exercised with a stub in tests.
type linearLabelClassifier interface {
	ClassifyLabel(ctx context.Context, teamRef, labelName string) (*linearSDK.LabelClassificationResult, error)
}

// preflightLinearLabels classifies the trigger label and every pilot-*
// status label for one workspace, once at poller startup, and logs a line
// for any label that isn't cleanly team-scoped (GH-5092).
//
// Message/severity matrix — pinned to studio-sdk v0.36.0's label-lookup
// behavior (sdk/integrations/linear/client.go: GetLabelByName's workspace
// fallback via getWorkspaceLabelByName, and GetOrCreateLabel's auto-create).
// Re-derive this matrix against the new lookup code whenever the studio-sdk
// pin moves — v0.35.2 and earlier had neither the workspace fallback nor
// the auto-create, so a workspace-scoped trigger label was ERROR and a
// missing status label was WARN under that version (GH-5118 follow-up to
// PR#5113).
//
//	classification    | trigger label                    | status label
//	------------------|-----------------------------------|------------------------------------
//	team_scoped       | OK, silent                        | OK, silent
//	workspace_scoped  | WARN — GetLabelByName's workspace  | WARN — GetOrCreateLabel resolves
//	                  | fallback resolves it; still no     | it via the same fallback; still
//	                  | team-scope precedence              | no team-scope precedence
//	another_team      | ERROR — the fallback only matches  | WARN — GetLabelByName fails the
//	                  | a label with a nil team, so this   | same way, so GetOrCreateLabel
//	                  | never resolves; startup will fail  | falls through to CreateLabel and
//	                  |                                     | mints a duplicate under this team
//	missing           | ERROR — no label anywhere;         | INFO — GetOrCreateLabel
//	                  | startup will fail                  | auto-creates it on first use
//
// If the classification call itself errors (network/API failure), that is
// logged at Warn and the label is skipped — the preflight must never block
// startup on its own failure.
func preflightLinearLabels(ctx context.Context, log *slog.Logger, classifier linearLabelClassifier, teamRef, triggerLabel string, statusLabels []string) {
	classify := func(label string, isTrigger bool) {
		result, err := classifier.ClassifyLabel(ctx, teamRef, label)
		if err != nil {
			log.Warn("Linear label preflight: classification call failed, skipping",
				slog.String("label", label),
				slog.Any("error", err),
			)
			return
		}

		if result.Classification == linearSDK.LabelTeamScoped {
			return
		}

		fields := []any{
			slog.String("label", label),
			slog.String("classification", string(result.Classification)),
			slog.String("remedy", result.Remedy),
		}

		if isTrigger {
			if result.Classification == linearSDK.LabelWorkspaceScoped {
				log.Warn("Linear trigger label is workspace-scoped; resolves via workspace fallback but has no team-scope precedence", fields...)
				return
			}
			log.Error("Linear trigger label is not team-scoped; poller startup will fail", fields...)
			return
		}

		switch result.Classification {
		case linearSDK.LabelMissing:
			log.Info("Linear status label does not exist yet; will be auto-created on first use", fields...)
		case linearSDK.LabelWorkspaceScoped:
			log.Warn("Linear status label is workspace-scoped; resolves via workspace fallback but has no team-scope precedence", fields...)
		default: // LabelAnotherTeam
			log.Warn("Linear status label belongs to another team; a duplicate will be created under this team instead of reusing it", fields...)
		}
	}

	classify(triggerLabel, true)
	for _, label := range statusLabels {
		classify(label, false)
	}
}

// linearRepoLabelPrefix is the Linear label prefix that routes a polled issue
// to a Pilot project by name (repo:<pilot-project-name>), GH-5570.
const linearRepoLabelPrefix = "repo:"

// linearIssueFetcher abstracts the one SDK client call the project resolver
// needs (the Linear project id is not on the poll event), so tests can supply
// a fake. *linearSDK.Client satisfies it.
type linearIssueFetcher interface {
	GetIssue(ctx context.Context, id string) (*linearSDK.Issue, error)
}

// resolveLinearProjectPath picks the Pilot project a polled Linear issue
// belongs to (GH-5570). It never falls back to the daemon's default project.
// Signals, in order:
//  1. a repo:<pilot-project-name> label, matched case-insensitively against
//     the project's name — this is what splits one Linear project across repos;
//  2. the issue's Linear project id via cfg.GetProjectByLinearID (first
//     project in config order wins; ambiguity is WARN-logged once per issue);
//  3. the workspace's projects: mapping, when ws is non-nil and it names
//     exactly one Pilot project (GH-5575) — resolved via GetProjectByName.
//     More than one name is ambiguous and falls through to the skip.
//
// On success it returns (path, ""). When no signal resolves it returns
// ("", skipreason.ReasonNoProjectMapping) after a WARN naming the issue and
// the missing signals. A fetch error is logged and treated as "no project id".
func resolveLinearProjectPath(ctx context.Context, cfg *config.Config, ev sdkcore.IssueEvent, ws *linearSDK.WorkspaceConfig, client linearIssueFetcher) (path string, reason string) {
	log := logging.WithComponent("linear")

	// Signal 1: repo:<name> label.
	for _, label := range ev.Labels {
		l := strings.TrimSpace(label)
		if len(l) <= len(linearRepoLabelPrefix) || !strings.EqualFold(l[:len(linearRepoLabelPrefix)], linearRepoLabelPrefix) {
			continue
		}
		name := strings.TrimSpace(l[len(linearRepoLabelPrefix):])
		if name == "" {
			continue
		}
		if proj := cfg.GetProjectByName(name); proj != nil && proj.Path != "" {
			return proj.Path, ""
		}
		log.Warn("Linear repo label names no configured Pilot project; falling through to project id",
			slog.String("issue", ev.SequenceID),
			slog.String("label", label),
		)
	}

	// Signal 2: the issue's Linear project id.
	var projectID string
	if client != nil && ev.IssueID != "" {
		issue, err := client.GetIssue(ctx, ev.IssueID)
		switch {
		case err != nil:
			log.Warn("Failed to fetch Linear issue for project pairing; treating as no project id",
				slog.String("issue", ev.SequenceID),
				slog.Any("error", err),
			)
		case issue != nil && issue.Project != nil:
			projectID = issue.Project.ID
		}
	}
	if projectID != "" {
		matches := 0
		for _, p := range cfg.Projects {
			if p.Linear != nil && p.Linear.ProjectID == projectID {
				matches++
			}
		}
		if proj := cfg.GetProjectByLinearID(projectID); proj != nil && proj.Path != "" {
			if matches > 1 {
				log.Warn("Multiple Pilot projects share this Linear project id; using the first in config order",
					slog.String("issue", ev.SequenceID),
					slog.String("linear_project_id", projectID),
					slog.String("project", proj.Name),
					slog.Int("matches", matches),
				)
			}
			return proj.Path, ""
		}
	}

	// Signal 3: the workspace's projects: mapping, only when unambiguous.
	if ws != nil {
		var names []string
		for _, n := range ws.Projects {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
		switch {
		case len(names) == 1:
			if proj := cfg.GetProjectByName(names[0]); proj != nil && proj.Path != "" {
				return proj.Path, ""
			}
			log.Warn("Linear workspace projects mapping names no configured Pilot project",
				slog.String("issue", ev.SequenceID),
				slog.String("workspace", ws.Name),
				slog.String("project", names[0]),
			)
		case len(names) > 1:
			log.Warn("Linear workspace projects mapping is ambiguous (names more than one Pilot project); not routing by it",
				slog.String("issue", ev.SequenceID),
				slog.String("workspace", ws.Name),
				slog.String("projects", strings.Join(names, ",")),
			)
		}
	}

	log.Warn("Linear issue skipped: no Pilot project mapping",
		slog.String("issue", ev.SequenceID),
		slog.String("missing_repo_label", linearRepoLabelPrefix+"<project-name>"),
		slog.String("missing_project_id_pairing", "linear.project_id"),
		slog.String("missing_workspace_projects_mapping", "single-name projects:"),
		slog.String("linear_project_id", projectID),
	)
	return "", skipreason.ReasonNoProjectMapping
}

// linearNoMappingMarker is the substring that identifies Pilot's "no project
// mapping" comment on a Linear issue. It is visible text (not an HTML comment)
// so it survives Linear's markdown round-trip; the existence check is a plain
// substring match, which tolerates re-formatting of the surrounding text.
const linearNoMappingMarker = "pilot:no_project_mapping"

// linearCommentClient is the slice of *linearSDK.Client the unmapped-issue
// notifier needs: Execute to list existing comments, AddComment to post one.
type linearCommentClient interface {
	Execute(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error
	AddComment(ctx context.Context, issueID, body string) error
}

// linearUnmappedNotifier posts one no_project_mapping comment per Linear issue
// (GH-5576). Without it an unmapped issue labelled pilot silently never runs.
type linearUnmappedNotifier struct {
	client linearCommentClient
	cfg    *config.Config

	mu        sync.Mutex
	commented map[string]struct{} // issue IDs already known to carry the marker
}

func newLinearUnmappedNotifier(client linearCommentClient, cfg *config.Config) *linearUnmappedNotifier {
	return &linearUnmappedNotifier{client: client, cfg: cfg, commented: make(map[string]struct{})}
}

// notify posts the no-mapping comment unless one carrying the marker already
// exists. The in-memory set spares an API read on every poll tick; the marker
// lookup makes the once-per-issue guarantee survive a daemon restart. Failures
// are logged and swallowed: a missed comment is retried on the next poll
// (the issue stays re-pollable) and must never fail the handler.
func (n *linearUnmappedNotifier) notify(ctx context.Context, ev sdkcore.IssueEvent) {
	log := logging.WithComponent("linear")
	if n == nil || n.client == nil || ev.IssueID == "" {
		return
	}

	n.mu.Lock()
	_, done := n.commented[ev.IssueID]
	n.mu.Unlock()
	if done {
		return
	}

	exists, err := n.hasMarkerComment(ctx, ev.IssueID)
	if err != nil {
		// Unknown state: do not post, or a flaky read could duplicate the comment.
		log.Warn("Failed to check existing comments on unmapped Linear issue; not commenting this poll",
			slog.String("issue", ev.SequenceID),
			slog.Any("error", err),
		)
		return
	}
	if !exists {
		if err := n.client.AddComment(ctx, ev.IssueID, linearNoMappingComment(n.cfg)); err != nil {
			log.Warn("Failed to comment on unmapped Linear issue",
				slog.String("issue", ev.SequenceID),
				slog.Any("error", err),
			)
			return
		}
	}

	n.mu.Lock()
	n.commented[ev.IssueID] = struct{}{}
	n.mu.Unlock()
}

// hasMarkerComment reports whether any comment on the issue contains the marker.
func (n *linearUnmappedNotifier) hasMarkerComment(ctx context.Context, issueID string) (bool, error) {
	const query = `
		query IssueComments($id: String!, $first: Int!, $after: String) {
			issue(id: $id) {
				comments(first: $first, after: $after) {
					nodes { body }
					pageInfo { hasNextPage endCursor }
				}
			}
		}
	`
	after := ""
	for {
		vars := map[string]interface{}{"id": issueID, "first": 100}
		if after != "" {
			vars["after"] = after
		}
		var result struct {
			Issue struct {
				Comments struct {
					Nodes    []struct{ Body string } `json:"nodes"`
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"comments"`
			} `json:"issue"`
		}
		if err := n.client.Execute(ctx, query, vars, &result); err != nil {
			return false, err
		}
		for _, c := range result.Issue.Comments.Nodes {
			if strings.Contains(c.Body, linearNoMappingMarker) {
				return true, nil
			}
		}
		if !result.Issue.Comments.PageInfo.HasNextPage || result.Issue.Comments.PageInfo.EndCursor == "" {
			return false, nil
		}
		after = result.Issue.Comments.PageInfo.EndCursor
	}
}

// linearNoMappingComment names the two routing signals the issue lacked, with
// the configured Pilot project names filled into the repo label format.
func linearNoMappingComment(cfg *config.Config) string {
	var labels []string
	if cfg != nil {
		for _, p := range cfg.Projects {
			if p != nil && p.Name != "" && p.Path != "" {
				labels = append(labels, "`"+linearRepoLabelPrefix+p.Name+"`")
			}
		}
	}
	names := "(no Pilot projects are configured)"
	if len(labels) > 0 {
		names = strings.Join(labels, ", ")
	}
	return "**Pilot skipped this issue: no project mapping** (" + linearNoMappingMarker + ")\n\n" +
		"Pilot could not tell which repository this issue belongs to, so it was not started. " +
		"Add either signal below; Pilot re-checks the issue on its next poll, so keep the `pilot` label.\n\n" +
		"1. A repo label in the format `" + linearRepoLabelPrefix + "<project-name>`. Configured projects: " + names + ".\n" +
		"2. A project id pairing: put the issue in a Linear project whose id is set as `linear.project_id` on a Pilot project in `config.yaml`."
}

// routeLinearIssue resolves the issue's Pilot project and calls dispatch with
// its path. An unmapped issue returns a Skipped IssueResult and dispatch is
// never called; onUnmapped (may be nil) runs first so the operator is told why
// (GH-5576). The caller must also un-persist the poller's processed mark.
func routeLinearIssue(ctx context.Context, cfg *config.Config, ev sdkcore.IssueEvent, ws *linearSDK.WorkspaceConfig, client linearIssueFetcher, onUnmapped func(sdkcore.IssueEvent), dispatch func(projectPath string) (*sdkcore.IssueResult, error)) (*sdkcore.IssueResult, error) {
	projectPath, skipReason := resolveLinearProjectPath(ctx, cfg, ev, ws, client)
	if projectPath == "" {
		if onUnmapped != nil {
			onUnmapped(ev)
		}
		return &sdkcore.IssueResult{Success: false, Skipped: true, SkipReason: skipReason}, nil
	}
	return dispatch(projectPath)
}

func newSDKLinearWorkspace(name, apiKey, teamID, triggerLabel string, projectIDs, projects []string, interval time.Duration) *linearSDK.WorkspaceConfig {
	return &linearSDK.WorkspaceConfig{
		Name:         name,
		APIKey:       apiKey,
		TeamID:       teamID,
		TriggerLabel: triggerLabel,
		ProjectIDs:   projectIDs,
		Projects:     projects,
		Polling: &linearSDK.PollingConfig{
			Enabled:  true,
			Interval: interval,
		},
	}
}

func linearPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "linear",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled &&
				cfg.Adapters.Linear.Polling != nil && cfg.Adapters.Linear.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			interval := 30 * time.Second
			if deps.Cfg.Adapters.Linear.Polling.Interval > 0 {
				interval = deps.Cfg.Adapters.Linear.Polling.Interval
			}

			// Map internal workspace configs → SDK workspace configs. Linear
			// supports multiple workspaces, each authenticating with its own
			// API key, so every workspace gets its own client, notifier and
			// handler (GH-4717: a single global notifier would post every
			// workspace's "started" comment using just one workspace's
			// credentials).
			internalWss := deps.Cfg.Adapters.Linear.GetWorkspaces()

			pollerDeps := sdkcore.PollerDeps{}
			if deps.AutopilotStateStore != nil {
				pollerDeps.ProcessedStore = deps.AutopilotStateStore
			}
			if deps.Cfg.Orchestrator.MaxConcurrent > 0 {
				pollerDeps.MaxConcurrent = deps.Cfg.Orchestrator.MaxConcurrent
			}
			// GH-5584: Linear routes issues per project, so the PR may live in a
			// repo other than the default one — register it with the controller
			// of the repo it was actually opened in, never blindly the default's.
			if deps.AutopilotController != nil || len(deps.AutopilotControllers) > 0 {
				pollerDeps.OnPRCreated = newLinearOnPRCreated(deps.Cfg, deps.AutopilotController, deps.AutopilotControllers)
			}

			pollers := make([]sdkcore.Poller, 0, len(internalWss))
			for _, ws := range internalWss {
				triggerLabel := ws.PilotLabel
				if triggerLabel == "" {
					triggerLabel = "pilot"
				}
				wsInterval := interval
				if ws.Polling != nil && ws.Polling.Interval > 0 {
					wsInterval = ws.Polling.Interval
				}

				// GH-5092: classify the trigger label and every pilot-*
				// status label once at startup, before the SDK poller's
				// own (opaque) label lookups run.
				client := linearSDK.NewClient(ws.APIKey)
				preflightLinearLabels(ctx, logging.WithComponent("linear"), client, ws.TeamID, triggerLabel, linearStatusLabels)

				sdkWS := newSDKLinearWorkspace(ws.Name, ws.APIKey, ws.TeamID, triggerLabel, ws.ProjectIDs, ws.Projects, wsInterval)
				notifier := linearSDK.NewNotifier(client)

				// GH-5576: bound after the poller exists. The handler only runs
				// once the poller has started, so the late binding is race-free.
				var clearProcessed func(issueID string)
				wsDeps := pollerDeps
				wsDeps.Handler = newLinearWorkspaceHandler(deps.Cfg, sdkWS, client, newLinearUnmappedNotifier(client, deps.Cfg),
					func(issueID string) {
						if clearProcessed != nil {
							clearProcessed(issueID)
						}
					},
					func(issueCtx context.Context, ev sdkcore.IssueEvent, projectPath string) (*sdkcore.IssueResult, error) {
						// GH-4717: notify Linear the task has started, mirroring
						// GH-2132's Plane wiring (poller_plane.go). Failure is
						// WARN-logged only — a comment failure must never abort
						// dispatch.
						if err := notifier.NotifyTaskStarted(issueCtx, ev.IssueID, ev.SequenceID); err != nil {
							logging.WithComponent("linear").Warn("Failed to notify task started",
								slog.String("issue_id", ev.IssueID),
								slog.Any("error", err),
							)
						}
						return handleLinearIssueWithResult(issueCtx, deps.Cfg, ev, projectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
					})

				// One adapter per workspace: with a single workspace the SDK
				// returns the concrete *linearSDK.Poller, which exposes
				// ClearProcessed (the SDK marks an issue processed — in memory
				// and in the store — before calling the handler).
				poller := linearSDK.New(&linearSDK.Config{
					Enabled:    deps.Cfg.Adapters.Linear.Enabled,
					Workspaces: []*linearSDK.WorkspaceConfig{sdkWS},
					Polling:    &linearSDK.PollingConfig{Enabled: true, Interval: interval},
				}).NewPoller(wsDeps)
				if sp, ok := poller.(*linearSDK.Poller); ok {
					clearProcessed = sp.ClearProcessed
				} else {
					logging.WithComponent("linear").Warn("Linear poller does not expose ClearProcessed; unmapped issues will be commented on but not re-polled after labelling",
						slog.String("workspace", ws.Name),
					)
				}
				pollers = append(pollers, poller)
			}

			linearPoller := &linearMultiPoller{pollers: pollers}

			logging.WithComponent("start").Info("Linear polling enabled",
				slog.String("workspaces", fmt.Sprintf("%d workspace(s)", len(internalWss))),
				slog.Duration("interval", interval),
			)
			deps.SafeAdapterGo(ctx, "linear", func() {
				if err := linearPoller.Start(ctx); err != nil {
					logging.WithComponent("linear").Error("Linear poller failed",
						slog.Any("error", err),
					)
				}
			})
		},
	}
}

// linearMultiPoller runs one poller per workspace; a failing workspace must
// not stop the others (the studio-sdk's own multi-workspace poller is
// unexported, and we need one concrete *linearSDK.Poller per workspace).
type linearMultiPoller struct {
	pollers []sdkcore.Poller
}

func (m *linearMultiPoller) Start(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make([]error, len(m.pollers))
	for i, p := range m.pollers {
		wg.Add(1)
		go func(i int, p sdkcore.Poller) {
			defer wg.Done()
			errs[i] = p.Start(ctx)
		}(i, p)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// newLinearWorkspaceHandler builds one workspace's issue handler (GH-5570,
// GH-5576). It routes the issue to a Pilot project and calls dispatch; an
// unmapped issue is commented on once (see linearUnmappedNotifier) and its
// processed mark is cleared via clearProcessed, so it is re-examined on the
// next poll once the operator adds a routing signal. The SDK poller marks an
// issue processed before calling the handler and persists that mark, so
// without clearProcessed the skip would be permanent.
func newLinearWorkspaceHandler(cfg *config.Config, ws *linearSDK.WorkspaceConfig, fetcher linearIssueFetcher, unmapped *linearUnmappedNotifier, clearProcessed func(issueID string), dispatch func(ctx context.Context, ev sdkcore.IssueEvent, projectPath string) (*sdkcore.IssueResult, error)) sdkcore.IssueHandlerFunc {
	return func(ctx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
		res, err := routeLinearIssue(ctx, cfg, ev, ws, fetcher,
			func(ev sdkcore.IssueEvent) { unmapped.notify(ctx, ev) },
			func(projectPath string) (*sdkcore.IssueResult, error) { return dispatch(ctx, ev, projectPath) })
		if err == nil && res != nil && res.Skipped && res.SkipReason == skipreason.ReasonNoProjectMapping && clearProcessed != nil {
			clearProcessed(ev.IssueID)
		}
		return res, err
	}
}
