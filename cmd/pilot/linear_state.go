package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	linearSDK "github.com/qf-studio/studio-sdk/sdk/integrations/linear"

	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
)

// GH-5701: Linear native workflow-state write-back. The Linear leg used to be
// labels + a start comment only, so an issue Pilot had finished stayed in
// Backlog. The mover below turns a lifecycle leg into a state transition,
// keeping the notifier-owns-the-transition shape of the Jira leg at the call
// sites (the handler closure and autopilot's merge path) without an SDK change.

// linearLeg names a task lifecycle point that may move the Linear issue.
type linearLeg string

const (
	legStart  linearLeg = "start"  // dispatch → in_progress (default: started-type state)
	legPR     linearLeg = "pr"     // PR/MR opened → in_review (default: no move)
	legMerge  linearLeg = "merge"  // PR merged → done (default: completed-type state)
	legFailed linearLeg = "failed" // task failed → failed (default: no move)

	// legRestore is not configurable: it names the GH-5703 move of a
	// success-without-PR issue back to the state recorded by the start leg.
	legRestore linearLeg = "restore"
)

// errNoTransition means the leg has no configured state and no type default;
// the caller skips the move (comment only).
var errNoTransition = errors.New("no state transition configured for leg")

const (
	linearStateTypeStarted   = "started"
	linearStateTypeCompleted = "completed"

	// linearFailureReasonMax bounds the failure reason posted as a comment.
	linearFailureReasonMax = 200
)

var linearUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// linearStateAPI is the slice of *linearSDK.Client the mover needs.
type linearStateAPI interface {
	Execute(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error
	GetIssue(ctx context.Context, id string) (*linearSDK.Issue, error)
	GetTeamDoneStateID(ctx context.Context, teamKey string) (string, error)
}

// linearTransitioner is the slice of *linearSDK.SyncAdapter the mover needs.
// TransitionState passes a UUID straight through and resolves a name against
// the issue's team.
type linearTransitioner interface {
	TransitionState(ctx context.Context, nativeID, providerState string) error
}

// linearStateMover moves one workspace's Linear issues between workflow
// states. Each workspace has its own client/API key (GH-4717), so there is one
// mover per workspace.
type linearStateMover struct {
	api         linearStateAPI
	transitions linearTransitioner
	cfg         linear.Transitions
	log         *slog.Logger

	mu            sync.Mutex
	startedByTeam map[string]string // team key → started-type state id
	priorState    map[string]string // issue UUID → state id before the start leg (GH-5703)
}

func newLinearStateMover(api linearStateAPI, transitions linearTransitioner, cfg linear.Transitions) *linearStateMover {
	return &linearStateMover{
		api:           api,
		transitions:   transitions,
		cfg:           cfg,
		log:           logging.WithComponent("linear"),
		startedByTeam: make(map[string]string),
		priorState:    make(map[string]string),
	}
}

// configured returns the operator-set target for leg ("" = unset).
func (m *linearStateMover) configured(leg linearLeg) string {
	switch leg {
	case legStart:
		return m.cfg.InProgress
	case legPR:
		return m.cfg.InReview
	case legMerge:
		return m.cfg.Done
	case legFailed:
		return m.cfg.Failed
	}
	return ""
}

// Move transitions the issue (Linear UUID) for leg. issueLabel is the
// human-readable identifier used in logs. Every attempted transition logs INFO
// "Linear state transition"; a failure logs WARN and is returned — callers
// must not let it alter dispatch, results, or a merge. errNoTransition (the
// leg has no state to move to) logs DEBUG and is returned for the caller to
// skip.
func (m *linearStateMover) Move(ctx context.Context, issueUUID, issueLabel string, leg linearLeg) error {
	return m.move(ctx, nil, issueUUID, issueLabel, leg)
}

// move is Move with an optional already-fetched issue, so MoveIdentifier does
// not read the same issue twice (GH-5703).
func (m *linearStateMover) move(ctx context.Context, issue *linearSDK.Issue, issueUUID, issueLabel string, leg linearLeg) error {
	// Any leg after start ends the prior-state bookkeeping for this issue
	// (a PR, a failure or a merge supersedes "go back to where it was").
	if leg != legStart {
		m.forgetPrior(issueUUID)
	}

	target := m.configured(leg)
	to := target
	if target == "" {
		switch leg {
		case legStart:
			to = "type:" + linearStateTypeStarted
		case legMerge:
			to = "type:" + linearStateTypeCompleted
		default:
			m.log.Debug("Linear state transition skipped: leg not configured",
				slog.String("issue", issueLabel),
				slog.String("leg", string(leg)),
			)
			return errNoTransition
		}
	}

	// GH-5703: remember the state the issue had before the start leg so a
	// success-without-PR run can put it back. Best effort: a read failure only
	// loses the restore, never the start move.
	if leg == legStart {
		if issue == nil {
			if got, gerr := m.api.GetIssue(ctx, issueUUID); gerr == nil {
				issue = got
			}
		}
		if issue != nil && issue.State.ID != "" {
			m.mu.Lock()
			m.priorState[issueUUID] = issue.State.ID
			m.mu.Unlock()
		}
	}

	m.log.Info("Linear state transition",
		slog.String("issue", issueLabel),
		slog.String("to", to),
		slog.String("leg", string(leg)),
	)

	if err := m.transition(ctx, issue, issueUUID, leg, target); err != nil {
		m.log.Warn("Linear state transition failed",
			slog.String("issue", issueLabel),
			slog.String("to", to),
			slog.String("leg", string(leg)),
			slog.Any("error", err),
		)
		return err
	}
	return nil
}

func (m *linearStateMover) forgetPrior(issueUUID string) {
	m.mu.Lock()
	delete(m.priorState, issueUUID)
	m.mu.Unlock()
}

// MoveNoPR closes out a run that succeeded without producing a PR/MR (no
// changes needed, issue closed mid-run, operator cancel — GH-5703): the issue
// goes to the configured failed state when set, otherwise back to the state the
// start leg recorded. errNoTransition means there is nowhere to move it (no
// failed state configured and no recorded prior state).
func (m *linearStateMover) MoveNoPR(ctx context.Context, issueUUID, issueLabel string) error {
	if m.configured(legFailed) != "" {
		return m.Move(ctx, issueUUID, issueLabel, legFailed)
	}

	m.mu.Lock()
	prior := m.priorState[issueUUID]
	delete(m.priorState, issueUUID)
	m.mu.Unlock()
	if prior == "" {
		m.log.Debug("Linear state restore skipped: no prior state recorded",
			slog.String("issue", issueLabel),
		)
		return errNoTransition
	}

	m.log.Info("Linear state transition",
		slog.String("issue", issueLabel),
		slog.String("to", "prior:"+prior),
		slog.String("leg", string(legRestore)),
	)
	if err := m.transitions.TransitionState(ctx, issueUUID, prior); err != nil {
		m.log.Warn("Linear state transition failed",
			slog.String("issue", issueLabel),
			slog.String("to", "prior:"+prior),
			slog.String("leg", string(legRestore)),
			slog.Any("error", err),
		)
		return err
	}
	return nil
}

func (m *linearStateMover) transition(ctx context.Context, issue *linearSDK.Issue, issueUUID string, leg linearLeg, target string) error {
	if target != "" {
		return m.transitions.TransitionState(ctx, issueUUID, target)
	}

	// Type default: the team comes from the issue itself, so the workspace's
	// team_id may be either a key or a UUID.
	if issue == nil {
		var err error
		issue, err = m.api.GetIssue(ctx, issueUUID)
		if err != nil {
			return fmt.Errorf("resolve issue team: %w", err)
		}
	}
	teamKey := issue.Team.Key
	if teamKey == "" {
		return fmt.Errorf("issue %s has no team key", issueUUID)
	}

	var (
		stateID string
		err     error
	)
	switch leg {
	case legStart:
		stateID, err = m.startedStateID(ctx, teamKey)
	case legMerge:
		stateID, err = m.api.GetTeamDoneStateID(ctx, teamKey)
	default:
		return errNoTransition
	}
	if err != nil {
		return err
	}
	if stateID == "" {
		return fmt.Errorf("no %s state resolved for team %s", leg, teamKey)
	}
	return m.transitions.TransitionState(ctx, issueUUID, stateID)
}

// startedStateID returns the team's first started-type state (lowest
// position), cached per team key.
func (m *linearStateMover) startedStateID(ctx context.Context, teamKey string) (string, error) {
	m.mu.Lock()
	id, ok := m.startedByTeam[teamKey]
	m.mu.Unlock()
	if ok {
		return id, nil
	}

	states, err := listLinearTeamStates(ctx, m.api, "key", teamKey, linearStateTypeStarted)
	if err != nil {
		return "", err
	}
	if len(states) == 0 {
		return "", fmt.Errorf("no %s state found for team %s", linearStateTypeStarted, teamKey)
	}
	sort.SliceStable(states, func(i, j int) bool { return states[i].Position < states[j].Position })

	m.mu.Lock()
	m.startedByTeam[teamKey] = states[0].ID
	m.mu.Unlock()
	return states[0].ID, nil
}

// MoveIdentifier resolves a Linear identifier ("QF-295") to the issue UUID via
// GetIssue — Linear's issue(id:) accepts either form — then moves it.
func (m *linearStateMover) MoveIdentifier(ctx context.Context, identifier string, leg linearLeg) error {
	issue, err := m.api.GetIssue(ctx, identifier)
	if err != nil {
		// Not logged here: with several workspaces a miss is expected and the
		// caller (autopilot's notifyLinearDone) WARNs if every workspace missed.
		return fmt.Errorf("resolve issue %s: %w", identifier, err)
	}
	return m.move(ctx, issue, issue.ID, identifier, leg)
}

type linearTeamState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Position float64 `json:"position"`
}

// listLinearTeamStates lists a team's workflow states, optionally restricted
// to one state type. teamField is "key" or "id".
func listLinearTeamStates(ctx context.Context, api interface {
	Execute(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error
}, teamField, teamRef, stateType string) ([]linearTeamState, error) {
	// TeamFilter.key compares Strings, TeamFilter.id compares IDs.
	teamVarType := "String!"
	if teamField == "id" {
		teamVarType = "ID!"
	}
	query := fmt.Sprintf(`
		query PilotTeamWorkflowStates($team: %s) {
			workflowStates(filter: { team: { %s: { eq: $team } } }, first: 100) {
				nodes { id name type position }
			}
		}
	`, teamVarType, teamField)
	if stateType != "" {
		query = fmt.Sprintf(`
		query PilotTeamWorkflowStates($team: %s, $type: String) {
			workflowStates(filter: { team: { %s: { eq: $team } }, type: { eq: $type } }, first: 100) {
				nodes { id name type position }
			}
		}
	`, teamVarType, teamField)
	}
	vars := map[string]interface{}{"team": teamRef}
	if stateType != "" {
		vars["type"] = stateType
	}
	var result struct {
		WorkflowStates struct {
			Nodes []linearTeamState `json:"nodes"`
		} `json:"workflowStates"`
	}
	if err := api.Execute(ctx, query, vars, &result); err != nil {
		return nil, err
	}
	return result.WorkflowStates.Nodes, nil
}

// preflightLinearTransitions WARNs once at startup for every configured
// transition target that does not resolve to a workflow state of the team
// (GH-5701, GH-5092 style). It never blocks startup: a listing failure is
// logged and skipped.
func preflightLinearTransitions(ctx context.Context, log *slog.Logger, api interface {
	Execute(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error
}, teamRef string, t linear.Transitions) {
	targets := []struct{ key, value string }{
		{"in_progress", t.InProgress},
		{"in_review", t.InReview},
		{"done", t.Done},
		{"failed", t.Failed},
	}
	if teamRef == "" {
		return
	}
	configured := false
	for _, tg := range targets {
		if tg.value != "" {
			configured = true
		}
	}
	if !configured {
		return
	}

	field := "key"
	if linearUUIDRe.MatchString(teamRef) {
		field = "id"
	}
	states, err := listLinearTeamStates(ctx, api, field, teamRef, "")
	if err != nil {
		log.Warn("Linear transitions preflight: listing workflow states failed, skipping",
			slog.String("team", teamRef),
			slog.Any("error", err),
		)
		return
	}
	for _, tg := range targets {
		if tg.value == "" {
			continue
		}
		found := false
		for _, s := range states {
			if (linearUUIDRe.MatchString(tg.value) && strings.EqualFold(s.ID, tg.value)) || s.Name == tg.value {
				found = true
				break
			}
		}
		if !found {
			log.Warn("Linear transition target does not match any workflow state of the team; the leg will fail until fixed",
				slog.String("team", teamRef),
				slog.String("transition", tg.key),
				slog.String("value", tg.value),
			)
		}
	}
}

// linearLifecycleNotifier is the slice of the Linear notifier the state legs use.
type linearLifecycleNotifier interface {
	NotifyTaskCompleted(ctx context.Context, issueID, prURL, summary string) error
	NotifyTaskFailed(ctx context.Context, issueID, reason string) error
	// NotifyTaskEndedWithoutPR posts the terminal comment for a run that
	// succeeded without a PR/MR (GH-5703).
	NotifyTaskEndedWithoutPR(ctx context.Context, issueID string) error
}

// linearLegNotifier adds the GH-5703 no-PR comment to the SDK notifier, which
// only has completed/failed shapes (its completed comment promises a PR).
type linearLegNotifier struct {
	*linearSDK.Notifier
	client *linearSDK.Client
}

func newLinearLegNotifier(client *linearSDK.Client) *linearLegNotifier {
	return &linearLegNotifier{Notifier: linearSDK.NewNotifier(client), client: client}
}

const linearNoPRComment = "ℹ️ **Pilot finished this task without opening a pull request**\n\n" +
	"No changes were needed, the issue was closed during the run, or the run was canceled. " +
	"Nothing is waiting for review.\n\n" +
	"_Reopen or re-label the issue if work is still expected._"

// NotifyTaskEndedWithoutPR implements linearLifecycleNotifier.
func (n *linearLegNotifier) NotifyTaskEndedWithoutPR(ctx context.Context, issueID string) error {
	if err := n.client.AddComment(ctx, issueID, linearNoPRComment); err != nil {
		return fmt.Errorf("failed to add no-PR comment: %w", err)
	}
	return nil
}

// linearMover is the slice of *linearStateMover the handler legs use.
type linearMover interface {
	Move(ctx context.Context, issueUUID, issueLabel string, leg linearLeg) error
	MoveNoPR(ctx context.Context, issueUUID, issueLabel string) error
}

// runLinearStateLegs wraps one Linear dispatch with the native-state legs
// (GH-5701): the start move once the dispatch is admitted, then — on a
// non-skipped result — the completion comment + optional in_review move (a
// PR/MR URL was produced), the failure comment + optional failed move, or
// (GH-5703) the no-PR comment + move to failed/prior state. It never alters
// run's result or error, and a comment or transition failure is only logged.
// Skipped results and admission-gate declines (executor.ErrDispatchGated)
// post and move nothing.
//
// run receives admitted, which it must call exactly when the dispatch won its
// claim, i.e. after every admission gate (IsActive, repick backoff, terminal
// completion, claim-lost). The start move happens inside it, so a gated decline
// never touches the Linear state (GH-5703). admitted is safe to call more than
// once; only the first call moves.
func runLinearStateLegs(ctx context.Context, ev sdkcore.IssueEvent, notifier linearLifecycleNotifier, mover linearMover, run func(admitted func()) (*sdkcore.IssueResult, error)) (*sdkcore.IssueResult, error) {
	log := logging.WithComponent("linear")

	// Failures are logged inside Move; errNoTransition is the expected "comment only" case.
	admitted := sync.OnceFunc(func() {
		_ = mover.Move(ctx, ev.IssueID, ev.SequenceID, legStart)
	})

	res, err := run(admitted)
	if res == nil || res.Skipped || errors.Is(res.Error, executor.ErrDispatchGated) {
		return res, err
	}

	switch {
	case res.Success && res.PRURL != "":
		if nerr := notifier.NotifyTaskCompleted(ctx, ev.IssueID, res.PRURL, ""); nerr != nil {
			log.Warn("Failed to notify task completed",
				slog.String("issue_id", ev.IssueID),
				slog.Any("error", nerr),
			)
		}
		_ = mover.Move(ctx, ev.IssueID, ev.SequenceID, legPR)
	case res.Success:
		// GH-5703: no PR/MR and not a gated decline (no changes needed, issue
		// closed mid-run, operator cancel). Without this the issue would sit in
		// the started-type state forever.
		if nerr := notifier.NotifyTaskEndedWithoutPR(ctx, ev.IssueID); nerr != nil {
			log.Warn("Failed to notify task ended without PR",
				slog.String("issue_id", ev.IssueID),
				slog.Any("error", nerr),
			)
		}
		_ = mover.MoveNoPR(ctx, ev.IssueID, ev.SequenceID)
	default:
		if nerr := notifier.NotifyTaskFailed(ctx, ev.IssueID, linearFailureReason(res.Error)); nerr != nil {
			log.Warn("Failed to notify task failed",
				slog.String("issue_id", ev.IssueID),
				slog.Any("error", nerr),
			)
		}
		_ = mover.Move(ctx, ev.IssueID, ev.SequenceID, legFailed)
	}
	return res, err
}

// linearAbsPathRe matches an absolute filesystem path token (preceded by start,
// whitespace or an opening delimiter) so it can be dropped from public comments.
var linearAbsPathRe = regexp.MustCompile(`(^|[\s"'(=:\[])/[^\s"')\]]+`)

// linearFailureReason renders a handler error for the public failure comment
// (GH-5703): "<class>: <first line>" — the class is the backend error type
// when the chain carries a classified BackendError (rate_limit, timeout, ...),
// else "error". Only the first line is kept, absolute paths (worktrees, /tmp)
// are replaced by "<path>", and the result is capped at linearFailureReasonMax
// runes, so the wrapped error chain and stderr never reach the issue.
func linearFailureReason(err error) string {
	if err == nil {
		return "unknown error"
	}
	class, msg := "error", err.Error()
	var be executor.BackendError
	if errors.As(err, &be) {
		if t := be.ErrorType(); t != "" {
			class = t
		}
		if m := be.ErrorMessage(); strings.TrimSpace(m) != "" {
			msg = m
		}
	}
	msg = strings.TrimSpace(msg)
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	msg = linearAbsPathRe.ReplaceAllString(msg, "${1}<path>")
	if msg == "" {
		return class
	}
	r := []rune(class + ": " + msg)
	if len(r) > linearFailureReasonMax {
		return string(r[:linearFailureReasonMax]) + "…"
	}
	return string(r)
}

// linearDoneWorkspace is one workspace's merge-leg entry.
type linearDoneWorkspace struct {
	teamRef string // the workspace's configured team_id (key or UUID)
	mover   linearIdentifierMover
}

// linearIdentifierMover moves an issue addressed by identifier.
type linearIdentifierMover interface {
	MoveIdentifier(ctx context.Context, identifier string, leg linearLeg) error
}

// linearDoneNotifier is autopilot's LinearDoneNotifier (GH-5701): on a merged
// pilot/LIN-* PR it moves the issue to done. With several workspaces the
// issue's workspace is picked by team key and only that workspace is tried;
// when no team_id matches (e.g. team_id is a UUID), each workspace is tried
// until one can read the issue — a workspace's API key cannot see another
// workspace's issues.
//
// PRs autopilot never observes (today: all GitLab MRs) never reach this leg;
// for those the PR leg's in_review is the terminal state Pilot can reach.
type linearDoneNotifier struct {
	workspaces []linearDoneWorkspace
}

func newLinearDoneNotifier(ws []linearDoneWorkspace) *linearDoneNotifier {
	return &linearDoneNotifier{workspaces: ws}
}

// NotifyMerged implements autopilot.LinearDoneNotifier.
func (n *linearDoneNotifier) NotifyMerged(ctx context.Context, identifier, _ string) error {
	if n == nil || len(n.workspaces) == 0 {
		return errors.New("no Linear workspace configured")
	}
	teamKey := identifier
	if i := strings.LastIndex(identifier, "-"); i > 0 {
		teamKey = identifier[:i]
	}

	// A workspace whose team key matches owns the issue: its error is the
	// answer (GH-5703) — trying other workspaces would only bury it under a
	// "not found" from a key that cannot see the issue. Only when no team key
	// matches (e.g. team_id is a UUID) is every workspace tried.
	var matched []linearDoneWorkspace
	for _, w := range n.workspaces {
		if strings.EqualFold(w.teamRef, teamKey) {
			matched = append(matched, w)
		}
	}
	candidates := n.workspaces
	if len(matched) > 0 {
		candidates = matched
	}

	var lastErr error
	for _, w := range candidates {
		err := w.mover.MoveIdentifier(ctx, identifier, legMerge)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}
