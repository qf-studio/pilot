package main

import (
	"context"
	"fmt"
	"html"

	planeSDK "github.com/qf-studio/studio-sdk/sdk/integrations/plane"

	"github.com/qf-studio/pilot/internal/autopilot"
)

// planeDoneAPI is the slice of *planeSDK.Client the merge leg needs.
type planeDoneAPI interface {
	ListStates(ctx context.Context, workspaceSlug, projectID string) ([]planeSDK.State, error)
	UpdateIssueState(ctx context.Context, workspaceSlug, projectID, workItemID, stateID string) error
	AddCommentWithTracking(ctx context.Context, workspaceSlug, projectID, workItemID, commentHTML, externalSource, externalID string) error
}

// planeDoneNotifier is autopilot's PlaneDoneNotifier (GH-6751): on a merged
// PR with a Plane link it moves the work item to the first state of the
// completed group and posts one comment with the PR link. The SDK poller
// leaves the state alone for a run with a PR when CompletedStateAtMerge is
// set (see planeSDKConfig), so this leg is the only one that completes it.
type planeDoneNotifier struct {
	client        planeDoneAPI
	workspaceSlug string
}

func newPlaneDoneNotifier(client planeDoneAPI, workspaceSlug string) *planeDoneNotifier {
	return &planeDoneNotifier{client: client, workspaceSlug: workspaceSlug}
}

// NotifyMerged implements autopilot.PlaneDoneNotifier.
func (n *planeDoneNotifier) NotifyMerged(ctx context.Context, projectID, workItemID, prURL string) error {
	// The comment follows the state change: a failed change posts no comment.
	if err := n.CompleteWorkItem(ctx, projectID, workItemID); err != nil {
		return err
	}

	// The external ID is per work item, so a second attempt posts no second comment.
	commentHTML := fmt.Sprintf(`<p>🔀 Pull request merged: <a href="%s">%s</a></p>`,
		html.EscapeString(prURL), html.EscapeString(prURL))
	if err := n.client.AddCommentWithTracking(ctx, n.workspaceSlug, projectID, workItemID,
		commentHTML, "pilot", "pilot-merged-"+workItemID); err != nil {
		return fmt.Errorf("post merge comment on Plane work item %s: %w", workItemID, err)
	}
	return nil
}

// CompleteWorkItem moves the work item to the first state of the completed
// group and posts no comment. NotifyMerged uses it at the merge; the routed
// OnPRCreated callback uses it alone for a PR no controller takes (GH-6761).
func (n *planeDoneNotifier) CompleteWorkItem(ctx context.Context, projectID, workItemID string) error {
	states, err := n.client.ListStates(ctx, n.workspaceSlug, projectID)
	if err != nil {
		return fmt.Errorf("list states of Plane project %s: %w", projectID, err)
	}
	stateID := ""
	for _, s := range states {
		if s.Group == planeSDK.StateGroupCompleted {
			stateID = s.ID
			break
		}
	}
	if stateID == "" {
		return fmt.Errorf("plane project %s has no state of the %s group", projectID, planeSDK.StateGroupCompleted)
	}

	if err := n.client.UpdateIssueState(ctx, n.workspaceSlug, projectID, workItemID, stateID); err != nil {
		return fmt.Errorf("move Plane work item %s to the completed state: %w", workItemID, err)
	}
	return nil
}

// registerPlaneDoneNotifier wires the Plane merge leg on the default
// controller and on each controller of a repository (the controllers
// planeOnPRCreated can route a PR to). nil controllers are skipped.
func registerPlaneDoneNotifier(n autopilot.PlaneDoneNotifier, def *autopilot.Controller, byRepo map[string]*autopilot.Controller) {
	if def != nil {
		def.SetPlaneDoneNotifier(n)
	}
	for _, c := range byRepo {
		if c != nil {
			c.SetPlaneDoneNotifier(n)
		}
	}
}
