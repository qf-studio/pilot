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
	linearStateTypeCanceled  = "canceled"

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
	// Every leg ends the prior-state bookkeeping for this issue (a PR, a
	// failure or a merge supersedes "go back to where it was"); the start leg
	// re-records it below. Without this a superseded run, which makes no
	// closing move, would leave a stale entry for a later run to restore.
	m.forgetPrior(issueUUID)

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

	return m.apply(issueLabel, to, leg, func() error {
		return m.transition(ctx, issue, issueUUID, leg, target)
	})
}

// apply runs one transition with the shared log pair: INFO "Linear state
// transition" before, WARN "Linear state transition failed" (and the error
// returned) after a failure.
func (m *linearStateMover) apply(issueLabel, to string, leg linearLeg, do func() error) error {
	m.log.Info("Linear state transition",
		slog.String("issue", issueLabel),
		slog.String("to", to),
		slog.String("leg", string(leg)),
	)
	if err := do(); err != nil {
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
// changes needed, operator cancel — GH-5703; a superseded run goes through
// MoveSuperseded, which leaves a human-closed issue alone, GH-5708/GH-5715):
// the issue goes to the configured failed state when set, otherwise back to the state the
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

	return m.apply(issueLabel, "prior:"+prior, legRestore, func() error {
		return m.transitions.TransitionState(ctx, issueUUID, prior)
	})
}

// MoveSuperseded closes out a superseded run (GH-5715). The dispatcher also
// finishes rows as superseded mechanically (borrowed origin PR already merged,
// sibling already shipped) with no human involved, which would strand the issue
// in the started state. So: when the issue's current state is completed or
// canceled a human closed it and nothing moves; otherwise it is restored like a
// no_op/canceled run. If the state cannot be read a human close cannot be ruled
// out, so nothing moves and the prior-state entry is kept: the read failure is
// transient, and forgetting the prior would strand the issue in the started
// state with no way back. Nothing retries it yet; that is a separate decision.
// Both closed/restored outcomes forget the entry.
func (m *linearStateMover) MoveSuperseded(ctx context.Context, issueUUID, issueLabel string) error {
	issue, err := m.api.GetIssue(ctx, issueUUID)
	if err != nil {
		m.log.Warn("Linear superseded restore skipped: cannot read issue state",
			slog.String("issue", issueLabel),
			slog.Any("error", err),
		)
		return err
	}
	if t := issue.State.Type; t == linearStateTypeCompleted || t == linearStateTypeCanceled {
		m.forgetPrior(issueUUID)
		m.log.Debug("Linear superseded restore skipped: issue already closed",
			slog.String("issue", issueLabel),
			slog.String("state_type", t),
		)
		return errNoTransition
	}
	return m.MoveNoPR(ctx, issueUUID, issueLabel)
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
	// NotifyTaskEnded posts the terminal comment for a run that ended without
	// a PR/MR (GH-5703, GH-5705). branch names the task branch ("" = no branch
	// was pushed; linearEndCompleted); pushed is holdPushedBranch's push result
	// (linearEndHeld: nil = nothing pushed); detail is the execution row's error
	// text, shown only by linearEndHeld.
	NotifyTaskEnded(ctx context.Context, issueID string, kind linearEndKind, branch string, pushed *bool, detail string) error
}

// linearEndKind names a classified non-failure outcome without a PR/MR.
type linearEndKind string

const (
	linearEndCompleted  linearEndKind = "completed" // completed row, no PR/MR (GH-5703)
	linearEndNoOp       linearEndKind = "no_op"
	linearEndHeld       linearEndKind = "held"
	linearEndCanceled   linearEndKind = "canceled"
	linearEndSuperseded linearEndKind = "superseded"
)

// linearLegNotifier adds the GH-5703/GH-5705 terminal comments to the SDK
// notifier, which only has completed/failed shapes (its completed comment
// promises a PR).
type linearLegNotifier struct {
	*linearSDK.Notifier
	client *linearSDK.Client
}

func newLinearLegNotifier(client *linearSDK.Client) *linearLegNotifier {
	return &linearLegNotifier{Notifier: linearSDK.NewNotifier(client), client: client}
}

// linearEndComment renders the terminal comment for kind. branch is rendered
// for linearEndHeld and linearEndCompleted ("" on a completed run = no branch
// was pushed), pushed and detail (the row error) for linearEndHeld only.
func linearEndComment(kind linearEndKind, branch string, pushed *bool, detail string) string {
	switch kind {
	case linearEndNoOp:
		return "ℹ️ **Pilot finished this task without opening a pull request**\n\n" +
			"No changes were needed. Nothing is waiting for review.\n\n" +
			"_Reopen or re-label the issue if work is still expected._"
	case linearEndHeld:
		return linearHeldComment(branch, pushed, detail)
	case linearEndCanceled:
		return "ℹ️ **Pilot stopped this task: it was canceled**\n\n" +
			"An operator canceled the run. No pull request was opened and nothing is waiting for review.\n\n" +
			"_Re-label the issue if work is still expected._"
	case linearEndSuperseded:
		// Comment only: the issue state is left to whoever closed or replaced it.
		return "ℹ️ **Pilot stopped this task: it was superseded**\n\n" +
			"The issue was closed or replaced before the run finished. No pull request was opened and nothing is waiting for review."
	default: // linearEndCompleted
		ran := "The run completed; no branch was pushed,"
		if branch != "" {
			ran = fmt.Sprintf("The run completed on branch `%s`", branch)
		}
		return "ℹ️ **Pilot finished this task without opening a pull request**\n\n" +
			ran + " but produced no pull request. Nothing is waiting for review.\n\n" +
			"_Reopen or re-label the issue if work is still expected._"
	}
}

// linearHeldComment renders the needs_human hand-off. holdPushedBranch sets
// needs_human whether or not the push worked and persists the push result
// (executions.branch_pushed), so the comment never claims a push that did not
// happen: pushed (true), push failed (false, with the row's one-line reason) or
// nothing pushed (nil: direct-commit mode, no push attempted).
func linearHeldComment(branch string, pushed *bool, detail string) string {
	reason := linearDetailText(detail)

	var b strings.Builder
	b.WriteString("⏸️ **Pilot held this task for human review**\n\n")
	switch {
	case pushed == nil:
		b.WriteString("No branch was pushed: the work was held without a push. No pull request was opened.")
	case *pushed:
		fmt.Fprintf(&b, "Branch `%s` is pushed and held for human review. No pull request was opened.", branch)
	default:
		fmt.Fprintf(&b, "Branch `%s` is NOT on the remote: pushing it failed, so the work exists only on the Pilot host. No pull request was opened.", branch)
	}
	if reason != "" {
		b.WriteString("\n\n**Reason**: " + reason)
	}
	switch {
	case pushed == nil:
		b.WriteString("\n\n_Check the Pilot host for the work, then open a pull request or re-label the issue to retry._")
	case *pushed:
		b.WriteString("\n\n_Review the branch, then open a pull request or re-label the issue to retry._")
	default:
		b.WriteString("\n\n_Recover the work from the Pilot host before it is cleaned up, then open a pull request or re-label the issue to retry._")
	}
	return b.String()
}

// NotifyTaskEnded implements linearLifecycleNotifier.
func (n *linearLegNotifier) NotifyTaskEnded(ctx context.Context, issueID string, kind linearEndKind, branch string, pushed *bool, detail string) error {
	if err := n.client.AddComment(ctx, issueID, linearEndComment(kind, branch, pushed, detail)); err != nil {
		return fmt.Errorf("failed to add %s comment: %w", kind, err)
	}
	return nil
}

// linearMover is the slice of *linearStateMover the handler legs use.
type linearMover interface {
	Move(ctx context.Context, issueUUID, issueLabel string, leg linearLeg) error
	MoveNoPR(ctx context.Context, issueUUID, issueLabel string) error
	MoveSuperseded(ctx context.Context, issueUUID, issueLabel string) error
}

// linearRun is a Linear dispatch's result plus the classified outcome the
// state legs need (GH-5705): sdkcore.IssueResult alone only carries
// Success/PRURL/Error, which cannot tell a no_op from a failure or a
// needs_human hold from a success.
type linearRun struct {
	Result *sdkcore.IssueResult
	// Outcome is the classified execution outcome: the runner's Outcome tag
	// when set, else the persisted execution status ("" on the direct-runner
	// path without a tag and on gated declines).
	Outcome string
	// BranchName is the task branch. It is only named in a comment when it
	// exists: held (pushed or not) always, completed only with a commit or a
	// pushed branch (hasBranch).
	BranchName string
	// CommitSHA is the run's last commit ("" = none recorded).
	CommitSHA string
	// BranchPushed is holdPushedBranch's persisted push result (nil = no push
	// attempted). GH-5715.
	BranchPushed *bool
	// Detail is the execution row's error text, used when Result.Error is nil
	// (a rate_limited, stalled or needs_human row carries its message in the
	// execution row, not as a Go error).
	Detail string
}

// hasBranch reports whether the run left a branch behind: it carries a commit
// or the branch was pushed.
func (r *linearRun) hasBranch() bool {
	return r.CommitSHA != "" || (r.BranchPushed != nil && *r.BranchPushed)
}

// runLinearStateLegs wraps one Linear dispatch with the native-state legs
// (GH-5701), driven by the classified outcome (GH-5705) rather than
// Success/PRURL:
//
//	completed + PR/MR      completion comment, in_review leg
//	completed, no PR/MR    no-PR comment, failed/prior-state move (GH-5703)
//	no_op                  "no changes were needed" comment, failed/prior-state move
//	needs_human            held-branch comment keyed on the persisted push
//	                       result (pushed / push failed / nothing pushed) with
//	                       the row error, in_review leg (none when unset)
//	canceled               cause comment, failed/prior-state move
//	superseded             cause comment; the issue is left alone when its
//	                       state is completed/canceled (a human closed it),
//	                       else restored like canceled (GH-5715)
//	anything else          "<status>: <reason>" failure comment, failed leg
//
// It never alters run's result or error, and a comment or transition failure is
// only logged. Skipped results and admission-gate declines
// (executor.ErrDispatchGated) post and move nothing.
//
// run receives admitted, which it must call exactly when the dispatch won its
// claim, i.e. after every admission gate (IsActive, repick backoff, terminal
// completion, claim-lost). The start move happens inside it, so a gated decline
// never touches the Linear state (GH-5703). admitted is safe to call more than
// once; only the first call moves.
func runLinearStateLegs(ctx context.Context, ev sdkcore.IssueEvent, notifier linearLifecycleNotifier, mover linearMover, run func(admitted func()) (*linearRun, error)) (*sdkcore.IssueResult, error) {
	log := logging.WithComponent("linear")

	// Failures are logged inside Move; errNoTransition is the expected "comment only" case.
	admitted := sync.OnceFunc(func() {
		_ = mover.Move(ctx, ev.IssueID, ev.SequenceID, legStart)
	})

	out, err := run(admitted)
	if out == nil || out.Result == nil {
		return nil, err
	}
	res := out.Result
	if res.Skipped || errors.Is(res.Error, executor.ErrDispatchGated) {
		return res, err
	}

	warn := func(what string, nerr error) {
		if nerr != nil {
			log.Warn("Failed to notify "+what,
				slog.String("issue_id", ev.IssueID),
				slog.Any("error", nerr),
			)
		}
	}
	ended := func(kind linearEndKind) {
		branch := out.BranchName
		if kind == linearEndCompleted && !out.hasBranch() {
			branch = "" // GH-5715: never name a branch that may not exist
		}
		warn("task ended ("+string(kind)+")", notifier.NotifyTaskEnded(ctx, ev.IssueID, kind, branch, out.BranchPushed, out.Detail))
	}

	switch out.Outcome {
	case string(executor.ExecStatusNeedsHuman):
		// holdPushedBranch pushed a salvaged branch for a human: keep the
		// started state (or move to in_review when configured).
		ended(linearEndHeld)
		_ = mover.Move(ctx, ev.IssueID, ev.SequenceID, legPR)
	case string(executor.ExecStatusNoOp):
		ended(linearEndNoOp)
		_ = mover.MoveNoPR(ctx, ev.IssueID, ev.SequenceID)
	case string(executor.ExecStatusCanceled):
		ended(linearEndCanceled)
		_ = mover.MoveNoPR(ctx, ev.IssueID, ev.SequenceID)
	case string(executor.ExecStatusSuperseded):
		ended(linearEndSuperseded)
		_ = mover.MoveSuperseded(ctx, ev.IssueID, ev.SequenceID)
	default:
		switch {
		case res.Success && res.PRURL != "":
			warn("task completed", notifier.NotifyTaskCompleted(ctx, ev.IssueID, res.PRURL, ""))
			_ = mover.Move(ctx, ev.IssueID, ev.SequenceID, legPR)
		case res.Success:
			// GH-5703: completed without a PR/MR and not a gated decline (issue
			// closed mid-run, ...). Without this the issue would sit in the
			// started-type state forever.
			ended(linearEndCompleted)
			_ = mover.MoveNoPR(ctx, ev.IssueID, ev.SequenceID)
		default:
			cause := res.Error
			if cause == nil && strings.TrimSpace(out.Detail) != "" {
				cause = errors.New(out.Detail)
			}
			status := out.Outcome
			if status == "" {
				status = string(executor.ExecStatusFailed)
			}
			warn("task failed", notifier.NotifyTaskFailed(ctx, ev.IssueID, linearFailureReason(status, cause)))
			_ = mover.Move(ctx, ev.IssueID, ev.SequenceID, legFailed)
		}
	}
	return res, err
}

// linearAbsPathRe matches an absolute filesystem path token (preceded by start,
// whitespace or an opening/separating delimiter) so it can be dropped from
// public comments. The token stops at ',' and ';' so the punctuation after a
// path survives, and at a ':' that is last or followed by whitespace, a digit
// or another ':' ("/tmp/wt: exit 1", "/tmp/x.go:12:"). A ':' inside a path stays
// part of it only in the "<segment>:<segment-without-/>/<rest>" shape, i.e. the
// text after the ':' runs on to a '/' within the same token
// ("/clients/acme:prod/secrets.env"). So "PATH=/usr/bin:/opt/x" splits at the
// ':' before a '/' into two paths, and "/tmp/wt:remote rejected" keeps the text
// after the ':'. It never starts with '/', so the "//" of a URL
// (https://host/x) is not mistaken for a path.
var linearAbsPathRe = regexp.MustCompile(`(^|[\s"'(=:,;\[])/[^\s"')\]:,;/][^\s"')\]:,;]*(?::[^\s"')\]:,;/0-9][^\s"')\]:,;/]*/[^\s"')\]:,;]*)*`)

// linearFailureReason renders a failure for the public failure comment
// (GH-5703, GH-5705): "<status>: <first line>" — the class is the classified
// execution status (failed, rate_limited, stalled, ...), because the error the
// daemon path sees is rebuilt from a DB string and carries no type. Without a
// status the class is the backend error type when the chain carries one, else
// "error". Only the first line is kept, absolute paths (worktrees, /tmp) are
// replaced by "<path>", and the result is capped at linearFailureReasonMax
// runes, so the wrapped error chain and stderr never reach the issue.
func linearFailureReason(status string, err error) string {
	if err == nil && status == "" {
		return "unknown error"
	}
	class, msg := "error", "no error detail"
	if status != "" {
		class = status
	}
	if err != nil {
		msg = err.Error()
		var be executor.BackendError
		if errors.As(err, &be) {
			if status == "" {
				if t := be.ErrorType(); t != "" {
					class = t
				}
			}
			if m := be.ErrorMessage(); strings.TrimSpace(m) != "" {
				msg = m
			}
		}
	}
	msg = linearDetailLine(msg)
	if msg == "" {
		return class
	}
	return linearCap(class + ": " + msg)
}

// linearDetailLine reduces an error text to what may be posted publicly: the
// first line only, without the dispatcher's "execution failed: " wrapper
// (the status class already says it), absolute paths replaced by "<path>".
func linearDetailLine(msg string) string {
	msg = strings.TrimSpace(msg)
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	msg = strings.TrimPrefix(msg, execFailedPrefix)
	return linearAbsPathRe.ReplaceAllString(msg, "${1}<path>")
}

// linearDetailText is linearDetailLine capped at linearFailureReasonMax runes.
func linearDetailText(msg string) string {
	return linearCap(linearDetailLine(msg))
}

func linearCap(s string) string {
	r := []rune(s)
	if len(r) > linearFailureReasonMax {
		return string(r[:linearFailureReasonMax]) + "…"
	}
	return s
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
