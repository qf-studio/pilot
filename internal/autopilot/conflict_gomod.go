package autopilot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// mechanicalResolutionCommitMessage is the commit used when autopilot
// mechanically resolves a go.mod/go.sum-only merge conflict instead of
// falling through to close-and-reexecute.
const mechanicalResolutionCommitMessage = "chore(rebase): mechanical go.mod/go.sum resolution"

// isGoModSumOnlyConflict reports whether conflictedFiles is a non-empty
// subset of {go.mod, go.sum} — the only shape of conflict this rung knows
// how to resolve mechanically. Anything else (a conflict that also touches
// source files) must fall through to close-and-reexecute.
func isGoModSumOnlyConflict(conflictedFiles []string) bool {
	if len(conflictedFiles) == 0 {
		return false
	}
	for _, f := range conflictedFiles {
		if f != "go.mod" && f != "go.sum" {
			return false
		}
	}
	return true
}

// resolveGoModSumConflict mechanically resolves a merge conflict confined to
// go.mod/go.sum inside worktreePath — left mid-merge by attemptLocalMerge —
// commits the result, and pushes it to branchName on origin.
//
// GH-4328: go.sum conflicts are near-universal once two branches both touch
// go.mod (the hash lines never textually merge), so close-and-reexecute was
// firing full re-execution on trivial dependency-addition conflicts that
// `go mod tidy` can regenerate canonically. This only fires when every
// conflicted file is go.mod and/or go.sum, and only trusts go.mod hunks that
// are pure `require` additions on both sides (taking their union) — a hunk
// containing anything else (version bumps, removals, non-require lines) is
// left unresolved. A post-tidy `go build ./...` gate catches the rarer case
// where the union still doesn't compile. Any failure along this path —
// unresolvable hunk, tidy failure, or build failure — returns an error and
// leaves the caller to fall through to closeAndReexecute.
func resolveGoModSumConflict(ctx context.Context, worktreePath, branchName string, conflictedFiles []string) error {
	if !isGoModSumOnlyConflict(conflictedFiles) {
		return fmt.Errorf("conflict is not confined to go.mod/go.sum: %v", conflictedFiles)
	}

	for _, f := range conflictedFiles {
		if f != "go.mod" {
			continue
		}
		if err := resolveGoModRequireUnion(filepath.Join(worktreePath, "go.mod")); err != nil {
			return fmt.Errorf("resolve go.mod conflict: %w", err)
		}
	}

	// go.sum's conflict markers make it invalid for `go mod tidy` to even
	// parse. Its content doesn't need to survive the merge — tidy rewrites
	// go.sum from scratch against the resolved go.mod.
	for _, f := range conflictedFiles {
		if f != "go.sum" {
			continue
		}
		if err := os.Remove(filepath.Join(worktreePath, "go.sum")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove conflicted go.sum: %w", err)
		}
	}

	if err := runGoModTidy(ctx, worktreePath); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}

	// Test-gate: a require-union that's individually valid on each side can
	// still fail to build once combined (e.g. two deps pulling incompatible
	// transitive versions that `go mod tidy` resolves without erroring). Catch
	// that here, before committing, rather than pushing a broken build for CI
	// to discover minutes later.
	if err := runGoBuild(ctx, worktreePath); err != nil {
		return fmt.Errorf("go build after mechanical resolution: %w", err)
	}

	if err := runGitCmd(ctx, worktreePath, "add", "go.mod", "go.sum"); err != nil {
		return fmt.Errorf("git add go.mod go.sum: %w", err)
	}

	if err := runGitCmd(ctx, worktreePath, "commit", "-m", mechanicalResolutionCommitMessage); err != nil {
		return fmt.Errorf("commit mechanical resolution: %w", err)
	}

	if err := runGitCmd(ctx, worktreePath, "push", "origin", "HEAD:refs/heads/"+branchName); err != nil {
		return fmt.Errorf("push mechanical resolution to %s: %w", branchName, err)
	}

	return nil
}

func runGoModTidy(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "go", "mod", "tidy")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runGoBuild(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

var (
	conflictMarkerOurs   = regexp.MustCompile(`^<{7}`)
	conflictMarkerBase   = regexp.MustCompile(`^\|{7}`)
	conflictMarkerSep    = regexp.MustCompile(`^={7}$`)
	conflictMarkerTheirs = regexp.MustCompile(`^>{7}`)

	// requireLineRe matches a single-line `require module vX.Y.Z` directive,
	// or a bare `module vX.Y.Z` line as it would appear inside a
	// `require (...)` block. These are the only line shapes this resolver
	// treats as a safe-to-union "addition".
	requireLineRe = regexp.MustCompile(`^\s*(require\s+)?\S+\s+v\S+(\s*//.*)?\s*$`)
)

// resolveGoModRequireUnion rewrites the go.mod file at path in place,
// resolving every conflict hunk whose both sides consist solely of
// require-directive lines by taking their union (deduplicated, order
// preserved: ours first, then any theirs lines not already present).
//
// If any hunk contains a non-require line on either side, resolveGoModRequireUnion
// returns an error without modifying the file — that hunk represents a real
// merge decision (e.g. a version bump on the same module) this mechanical
// rung isn't allowed to make silently.
func resolveGoModRequireUnion(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	sawConflict := false
	i := 0
	for i < len(lines) {
		line := lines[i]
		if !conflictMarkerOurs.MatchString(line) {
			out = append(out, line)
			i++
			continue
		}
		sawConflict = true
		i++ // skip <<<<<<< ours marker

		ours, next, err := readConflictSide(lines, i, conflictMarkerBase, conflictMarkerSep)
		if err != nil {
			return fmt.Errorf("go.mod conflict hunk: %w", err)
		}
		i = next

		if conflictMarkerBase.MatchString(lines[i]) {
			// diff3 conflict style (merge.conflictStyle=diff3) inserts a
			// common-ancestor section between ours and the separator.
			// Its content is informational only — discard it.
			_, baseEnd, err := readConflictSide(lines, i+1, conflictMarkerSep)
			if err != nil {
				return fmt.Errorf("go.mod conflict hunk: %w", err)
			}
			i = baseEnd
		}
		i++ // skip =======

		theirs, next, err := readConflictSide(lines, i, conflictMarkerTheirs)
		if err != nil {
			return fmt.Errorf("go.mod conflict hunk: %w", err)
		}
		i = next + 1 // skip >>>>>>> theirs marker

		for _, l := range append(append([]string{}, ours...), theirs...) {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if !requireLineRe.MatchString(l) {
				return fmt.Errorf("go.mod conflict hunk contains a non-require line, cannot union: %q", l)
			}
		}

		seen := make(map[string]bool)
		for _, side := range [][]string{ours, theirs} {
			for _, l := range side {
				t := strings.TrimSpace(l)
				if t == "" || seen[t] {
					continue
				}
				seen[t] = true
				out = append(out, l)
			}
		}
	}

	if !sawConflict {
		return fmt.Errorf("no conflict markers found in %s", path)
	}

	return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
}

// readConflictSide collects lines starting at index i until one matches any
// of ends, returning the collected lines and the index of the matching line.
func readConflictSide(lines []string, i int, ends ...*regexp.Regexp) ([]string, int, error) {
	var side []string
	for i < len(lines) {
		for _, end := range ends {
			if end.MatchString(lines[i]) {
				return side, i, nil
			}
		}
		side = append(side, lines[i])
		i++
	}
	return nil, 0, fmt.Errorf("unterminated conflict hunk (missing one of %d markers)", len(ends))
}
