package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
)

// GH-4526: hosted tenants are onboarded via a fresh `gh repo clone` with
// zero pre-existing labels — box repos have accreted the full pilot-*
// label set over years of manual use, so this gap never surfaced there.
// `gh issue edit --add-label X` / `--remove-label X` hard-fails
// ("'X' not found") if X doesn't exist on the repo yet, which silently
// dropped every labeling side-channel (stalled-issue surfacing, title
// rejection escalation, ...) on a brand-new hosted repo's first dispatch.
// ensureGitHubLabels makes label creation idempotent so those call sites
// work unconditionally, regardless of whether the repo has ever seen a
// pilot-* label before.

// pilotLabelMeta describes the color/description used when creating a
// pilot-managed label on demand.
type pilotLabelMeta struct {
	color       string
	description string
}

// defaultPilotLabelColor is used for any label name not listed in
// pilotLabelDefs below (defensive fallback — new call sites shouldn't need
// to update this map to stay working, just to get a nicer color).
const defaultPilotLabelColor = "ededed"

// pilotLabelDefs documents color/description for the labels Pilot is known
// to apply via the gh CLI. Names mirror the string values of the
// github.Label* constants (internal/adapters/github/types.go) — duplicated
// here as literals rather than imported because internal/executor already
// sits behind internal/comms in that package's import path, and
// internal/adapters/github imports internal/comms, which would create an
// import cycle. Not required to be exhaustive.
var pilotLabelDefs = map[string]pilotLabelMeta{
	"pilot":                     {"5319e7", "Pilot should dispatch and execute this issue"},
	"pilot-in-progress":         {"fbca04", "Pilot is currently executing this issue"},
	"pilot-done":                {"0e8a16", "Pilot completed this issue"},
	"pilot-failed":              {"d93f0b", "Pilot's last attempt at this issue failed"},
	"pilot-retry-ready":         {"1d76db", "Re-armed for Pilot to retry after a manual fix"},
	"pilot-title-rejected":      {"d93f0b", "PR title rejected -- needs a conventional-commit title"},
	"pilot-superseded":          {"cfd3d7", "Closed automatically -- already shipped by a parent epic"},
	"pilot-blocked":             {"b60205", "Stalled past the repick hard cap -- needs manual unblock"},
	"pilot-needs-clarification": {"fbca04", "Executor declined this issue as unactionable"},
	"pilot-spec-incomplete":     {"fbca04", "Issue body too thin to dispatch"},
}

// ensureGitHubLabels makes sure every label in names exists on the repo in
// dir, creating (or updating) each via `gh label create --force`. Best
// effort: failures are logged, never returned, so a labels API hiccup never
// blocks the caller's actual `gh issue edit`/`gh issue comment` attempt —
// that call's own error remains the caller's source of truth.
func ensureGitHubLabels(ctx context.Context, dir string, names []string) {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true

		meta, ok := pilotLabelDefs[name]
		if !ok {
			meta = pilotLabelMeta{color: defaultPilotLabelColor}
		}
		if err := ghCreateLabel(ctx, dir, name, meta.color, meta.description); err != nil {
			slog.Warn("ensureGitHubLabels: failed to create/update label",
				"label", name, "error", err)
		}
	}
}

// ghCreateLabel creates the named label via `gh label create --force`,
// which creates it if missing and just updates color/description if it
// already exists — so this is safe to call unconditionally before every
// label edit rather than needing to check existence first.
func ghCreateLabel(ctx context.Context, dir, name, color, description string) error {
	args := []string{"label", "create", name, "--color", color, "--force"}
	if description != "" {
		args = append(args, "--description", description)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh label create %s: %w (stderr: %s)", name, err, stderr.String())
	}
	return nil
}
