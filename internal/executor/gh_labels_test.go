package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installFakeGHWithLabelEnforcement writes a fake `gh` CLI onto PATH that:
//   - logs every invocation (space-joined args) as a line in the returned log file
//   - `gh label create <name> ...` idempotently "creates" the label by
//     appending it to a labels file and always exits 0 (mirrors --force
//     semantics)
//   - `gh issue edit <id> [--add-label X|--remove-label X ...]` exits 1 with
//     a "not found" stderr message if ANY referenced label isn't in the
//     labels file yet — this reproduces the real `gh` CLI behavior GH-4526
//     hit on a freshly onboarded hosted repo with zero pre-existing pilot-*
//     labels.
//   - anything else (e.g. `gh issue comment`) just logs and exits 0.
//
// Returns the path to the call log and the labels file (empty/absent
// initially — simulating a repo with no pilot-* labels yet).
func installFakeGHWithLabelEnforcement(t *testing.T) (logFile, labelsFile string) {
	t.Helper()
	fakeBin := t.TempDir()
	logFile = filepath.Join(t.TempDir(), "gh-calls.log")
	labelsFile = filepath.Join(t.TempDir(), "labels.txt")

	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> "%s"

if [ "$1" = "label" ] && [ "$2" = "create" ]; then
  echo "$3" >> "%s"
  exit 0
fi

if [ "$1" = "issue" ] && [ "$2" = "edit" ]; then
  shift 2
  while [ $# -gt 0 ]; do
    case "$1" in
      --add-label|--remove-label)
        if ! grep -qxF "$2" "%s" 2>/dev/null; then
          echo "gh: '$2' not found" 1>&2
          exit 1
        fi
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done
  exit 0
fi

exit 0
`, logFile, labelsFile, labelsFile)

	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+origPATH)
	return logFile, labelsFile
}

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestGhEditLabels_CreatesMissingLabelsFirst is the GH-4526 regression test
// for the core bug: a hosted tenant repo is onboarded via a fresh `gh repo
// clone` with zero pre-existing pilot-* labels. `gh issue edit
// --add-label pilot-blocked --remove-label pilot-failed` against such a repo
// fails outright with "'pilot-blocked' not found" — before this fix, that
// silently dropped every label-based side channel (stalled-issue surfacing,
// title-rejection escalation) on a brand-new repo's very first dispatch.
// ghEditLabels must now create any missing label first so the edit succeeds
// unconditionally, regardless of the repo's label history.
func TestGhEditLabels_CreatesMissingLabelsFirst(t *testing.T) {
	logFile, labelsFile := installFakeGHWithLabelEnforcement(t)

	// Precondition: repo has zero pilot-* labels (mirrors a fresh hosted clone).
	if content := readFileOrEmpty(t, labelsFile); content != "" {
		t.Fatalf("expected no pre-existing labels, got: %q", content)
	}

	err := ghEditLabels(context.Background(), "", "9001",
		[]string{"pilot-blocked"},
		[]string{"pilot-failed", "pilot-in-progress"},
	)
	if err != nil {
		t.Fatalf("expected ghEditLabels to succeed by creating missing labels first, got: %v", err)
	}

	calls := readFileOrEmpty(t, logFile)
	for _, want := range []string{
		"label create pilot-blocked",
		"label create pilot-failed",
		"label create pilot-in-progress",
		"issue edit 9001",
		"--add-label pilot-blocked",
		"--remove-label pilot-failed",
		"--remove-label pilot-in-progress",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("expected gh call log to contain %q, got:\n%s", want, calls)
		}
	}

	// The label-create calls must land in the log before the issue-edit call —
	// otherwise the edit would still race against label creation.
	createIdx := strings.Index(calls, "label create pilot-blocked")
	editIdx := strings.Index(calls, "issue edit 9001")
	if createIdx == -1 || editIdx == -1 || createIdx > editIdx {
		t.Errorf("expected label create calls before the issue edit call, got:\n%s", calls)
	}
}

// TestGhEditLabels_UsesForceFlag verifies label creation is idempotent (safe
// to call on every edit, not just the first time a repo sees a given
// label) via `--force`, so a repo that already has the label doesn't error
// out or need a separate existence check.
func TestGhEditLabels_UsesForceFlag(t *testing.T) {
	logFile, _ := installFakeGHWithLabelEnforcement(t)

	if err := ghEditLabels(context.Background(), "", "42", []string{"pilot"}, nil); err != nil {
		t.Fatalf("ghEditLabels failed: %v", err)
	}

	calls := readFileOrEmpty(t, logFile)
	if !strings.Contains(calls, "label create pilot --color") || !strings.Contains(calls, "--force") {
		t.Errorf("expected label create to pass --force, got:\n%s", calls)
	}
}

// TestEnsureGitHubLabels_DeduplicatesNames guards against redundant `gh
// label create` calls when the same label appears in both addLabels and
// removeLabels (or is repeated) — one create call per unique name.
func TestEnsureGitHubLabels_DeduplicatesNames(t *testing.T) {
	logFile, _ := installFakeGHWithLabelEnforcement(t)

	ensureGitHubLabels(context.Background(), "", []string{"pilot-failed", "pilot-failed", ""})

	calls := readFileOrEmpty(t, logFile)
	if got := strings.Count(calls, "label create pilot-failed"); got != 1 {
		t.Errorf("expected exactly 1 create call for pilot-failed, got %d in:\n%s", got, calls)
	}
}
