package secretpatterns

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrPushRefused is matched (errors.Is) by every *PushRangeError. A retry loop
// must treat it as final: the same commits hold the same secret on every
// attempt.
var ErrPushRefused = errors.New("push refused")

// PushRangeError is the CheckPushRange verdict for a range that holds a secret.
// It names the commit and the file, never the value.
type PushRangeError struct {
	Commit string // short SHA of the commit that adds the secret
	File   string // path of the file, or CommitMessageFile
	Kind   string // Match.Kind of the hit: KindConfiguredValue or a pattern prefix
}

// CommitMessageFile is PushRangeError.File when the secret is in a commit message.
const CommitMessageFile = "the commit message"

func (e *PushRangeError) Error() string {
	return fmt.Sprintf("push refused: commit %s adds a secret value in %s", e.Commit, e.File)
}

// Is makes errors.Is(err, ErrPushRefused) true for a *PushRangeError.
func (e *PushRangeError) Is(target error) bool { return target == ErrPushRefused }

// patternAllowlist lists the files that show secret shapes on purpose. It
// mirrors ALLOWLIST in scripts/check-secret-patterns.sh, so a Pilot PR that
// edits them is not refused for its own fixtures. It never excuses a
// registered value.
var patternAllowlist = map[string]bool{
	"CLAUDE.md":                                            true,
	"CONTRIBUTING.md":                                      true,
	"internal/testutil/tokens.go":                          true,
	"scripts/check-secret-patterns.sh":                     true,
	"internal/executor/acceptance_evidence_run_test.go":    true,
	".agent/tasks/archive/TASK-41-test-secret-patterns.md": true,
	"internal/executor/secretpatterns/patterns.txt":        true,
	"internal/executor/secretpatterns/patterns_test.go":    true,
}

// emptyTree is git's well-known empty tree, the diff base for a range with no
// base commit.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// PushBase returns the commit a push of head is measured from: the merge base
// of head and the remote default branch (origin/HEAD, else origin/main, else
// origin/master). It returns "" when no such ref exists, which makes
// CheckPushRange scan everything reachable from head.
func PushBase(ctx context.Context, dir, head string) string {
	var candidates []string
	if out, err := gitOutput(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimSpace(string(out)); ref != "" {
			candidates = append(candidates, ref)
		}
	}
	candidates = append(candidates, "origin/main", "origin/master")
	for _, ref := range candidates {
		if out, err := gitOutput(ctx, dir, "merge-base", head, ref); err == nil {
			if sha := strings.TrimSpace(string(out)); sha != "" {
				return sha
			}
		}
	}
	return ""
}

// CheckPush is CheckPushRange for the commits a push of head would publish,
// measured from PushBase.
func CheckPush(ctx context.Context, dir, head string) error {
	return CheckPushRange(ctx, dir, PushBase(ctx, dir, head), head)
}

// CheckPushRange refuses the commits in base..head when their diff or messages
// hold a registered secret value or a known secret shape. base "" means every
// commit reachable from head. The returned *PushRangeError names the commit and
// the file, never the value. Only added lines count: a secret already in base
// is not this push's doing.
//
// Decision: besides the net diff and the messages, every commit is scanned on
// its own. A token committed and removed again by a later commit is gone from
// the net diff but still reaches the remote, which is exactly how the
// 2026-04-07 token was published.
//
// A failure to run git is returned as a plain error (fail closed): the caller
// must not push on it.
func CheckPushRange(ctx context.Context, dir, base, head string) error {
	diffBase, logRange := base, base+".."+head
	if base == "" {
		diffBase, logRange = emptyTree, head
	}
	diff, err := gitOutput(ctx, dir, "diff", "--no-color", diffBase+".."+head)
	if err != nil {
		return fmt.Errorf("secret gate: git diff: %w", err)
	}
	msgs, err := gitOutput(ctx, dir, "log", "--format=%B", logRange)
	if err != nil {
		return fmt.Errorf("secret gate: git log: %w", err)
	}
	netFile, netKind, netHit := scanPatch(diff)
	msgKind, msgHit := firstHit(msgs, false)

	// Per-commit pass: oldest first, so the error names the first offender.
	// Format per commit: \x01 sha \x02 message \x03 patch.
	perCommit, err := gitOutput(ctx, dir, "log", "-p", "--reverse", "--no-color", "--format=%x01%H%x02%B%x03", logRange)
	if err != nil {
		return fmt.Errorf("secret gate: git log -p: %w", err)
	}
	for _, chunk := range bytes.Split(perCommit, []byte{1}) {
		sha, rest, ok := bytes.Cut(chunk, []byte{2})
		if !ok {
			continue
		}
		msg, patch, _ := bytes.Cut(rest, []byte{3})
		if kind, hit := firstHit(msg, false); hit {
			return &PushRangeError{Commit: shortSHA(sha), File: CommitMessageFile, Kind: kind}
		}
		if file, kind, hit := scanPatch(patch); hit {
			return &PushRangeError{Commit: shortSHA(sha), File: file, Kind: kind}
		}
	}

	// Hits the per-commit pass cannot see (a merge's conflict resolution).
	if netHit || msgHit {
		sha, _ := gitOutput(ctx, dir, "rev-parse", "--short=12", head)
		e := &PushRangeError{Commit: strings.TrimSpace(string(sha)), File: netFile, Kind: netKind}
		if !netHit {
			e.File, e.Kind = CommitMessageFile, msgKind
		}
		return e
	}
	return nil
}

func shortSHA(sha []byte) string {
	s := strings.TrimSpace(string(sha))
	if len(s) > 12 {
		s = s[:12]
	}
	return s
}

// firstHit reports the Kind of the first secret in text. skipPatterns ignores
// shape matches, so only a registered value counts.
func firstHit(text []byte, skipPatterns bool) (string, bool) {
	for _, m := range FindMatches(text) {
		if skipPatterns && m.Kind != KindConfiguredValue {
			continue
		}
		return m.Kind, true
	}
	return "", false
}

// scanPatch scans the added lines of a unified diff, one file at a time (so a
// value spanning several added lines still matches), and returns the first file
// with a hit.
func scanPatch(patch []byte) (file, kind string, hit bool) {
	var (
		cur   string
		added bytes.Buffer
	)
	flush := func() bool {
		if added.Len() == 0 {
			return false
		}
		k, ok := firstHit(added.Bytes(), patternAllowlist[cur])
		added.Reset()
		if ok {
			file, kind = cur, k
		}
		return ok
	}
	for _, line := range bytes.Split(patch, []byte{'\n'}) {
		switch {
		case bytes.HasPrefix(line, []byte("diff --git ")):
			if flush() {
				return file, kind, true
			}
			cur = ""
		case bytes.HasPrefix(line, []byte("+++ ")):
			cur = strings.TrimSuffix(strings.TrimPrefix(string(line[4:]), "b/"), "\t")
		case len(line) > 0 && line[0] == '+':
			added.Write(line[1:])
			added.WriteByte('\n')
		}
	}
	if flush() {
		return file, kind, true
	}
	return "", "", false
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=false"}, args...)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
