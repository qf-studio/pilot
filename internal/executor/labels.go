package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// pilotLabelSpec describes one pilot-* label the daemon may apply to a
// GitHub issue somewhere in its lifecycle, plus the color `gh label create`
// uses the first time it creates the label.
type pilotLabelSpec struct {
	Name  string
	Color string
}

// PilotLabels enumerates every label name the daemon writes anywhere in its
// lifecycle: dispatcher.go's stalled-issue surfacing (pilot-blocked),
// title_rejection.go's escalation (pilot-failed, pilot-title-rejected), the
// GH-2432/GH-3715 retry-counter labels, and the base "pilot" trigger label
// itself.
//
// GH-4526: hosted tenant repos are onboarded with none of these labels
// pre-created (labels are a per-repo GitHub resource, not something `gh repo
// clone` brings along). The first label write on such a repo — e.g. stalled-
// issue surfacing's `gh issue edit --add-label pilot-blocked` — failed with
// `'pilot-blocked' not found`, and that failure is logged but swallowed by
// the caller (best-effort by design, since the store-side status is the
// durable source of truth). EnsureRepoLabels creates every label below
// up front so no future write can hit that error.
var PilotLabels = []pilotLabelSpec{
	{"pilot", "1d76db"},
	{"pilot-in-progress", "fbca04"},
	{"pilot-done", "0e8a16"},
	{"pilot-failed", "d73a4a"},
	{"pilot-retry-ready", "c5def5"},
	{"pilot-title-rejected", "c5def5"},
	{"pilot-superseded", "c5def5"},
	{"pilot-blocked", "d73a4a"},
	{"pilot-needs-clarification", "e99695"},
	{"pilot-spec-incomplete", "e99695"},
	{"pilot-skip-spec-check", "c5def5"},
	{"pilot-retry-1", "fbca04"},
	{"pilot-retry-2", "fbca04"},
	{"pilot-retry-exhausted", "d73a4a"},
	{"pilot-failed-retry-1", "fbca04"},
	{"pilot-failed-retry-2", "fbca04"},
	{"pilot-failed-retry-exhausted", "d73a4a"},
	{"pilot-needs-human", "5319e7"},
}

// EnsureRepoLabels creates every label in PilotLabels against dir's repo via
// `gh label create --force`, which is idempotent: it creates the label if
// absent and just updates its color in place if already present, so this is
// safe to call on every poller startup rather than only once per repo.
//
// Each label is attempted independently and a failure on one does not stop
// the rest — callers should log the returned errors (if any) as warnings,
// not treat them as fatal, mirroring the rest of this file's gh-CLI helpers.
func EnsureRepoLabels(ctx context.Context, dir string) []error {
	var errs []error
	for _, spec := range PilotLabels {
		if err := ghLabelCreate(ctx, dir, spec.Name, spec.Color); err != nil {
			errs = append(errs, fmt.Errorf("label %q: %w", spec.Name, err))
		}
	}
	return errs
}

// ghLabelCreate creates a single GitHub label via `gh label create --force`.
// --force makes this idempotent: an existing label has its color updated
// in place instead of the command failing with "already exists".
func ghLabelCreate(ctx context.Context, dir, name, color string) error {
	cmd := exec.CommandContext(ctx, "gh", "label", "create", name, "--color", color, "--force")
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh label create: %w (stderr: %s)", err, stderr.String())
	}
	return nil
}
