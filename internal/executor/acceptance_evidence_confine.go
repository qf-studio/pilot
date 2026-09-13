package executor

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// GH-5437: PR #5436's mutation harness joined an issue-supplied relative
// path directly onto the worktree directory (filepath.Join(dir, file)) and
// wrote to the result. A `../` prefix — or an absolute path, or a symlink
// inside the worktree that points outside it — escapes the worktree and
// lets an issue author rewrite arbitrary files on the box. This file is the
// single choke point every mutation-file write goes through.

// errPathEscapesWorktree is returned by resolveWorktreeConfinedPath for
// every escape vector; callers report it verbatim as the Not-verified
// reason ("path escapes worktree") per the GH-5437 spec, without leaking
// the resolved (potentially sensitive, e.g. /etc/passwd) path back into the
// PR body.
var errPathEscapesWorktree = errors.New("path escapes worktree")

// resolveWorktreeConfinedPath resolves an issue-supplied relative file path
// against worktree and guarantees the result is actually inside worktree,
// following the GH-5437 spec's algorithm: filepath.Clean the input, reject
// it outright if it's already absolute, filepath.EvalSymlinks both the
// worktree root and the joined candidate (so a symlink inside the worktree
// that points outside it is caught, not just a textual ".." escape), and
// require filepath.Rel(worktree, candidate) to not start with "..".
//
// Returns errPathEscapesWorktree for every escape vector (relative "..",
// absolute path, or symlink pointing outside). Any other error (most
// commonly the candidate not existing) is returned as-is — that's a
// "could not read" condition, not an escape attempt, and callers report it
// with a different Not-verified reason.
func resolveWorktreeConfinedPath(worktree, file string) (string, error) {
	cleanFile := filepath.Clean(file)
	if filepath.IsAbs(cleanFile) {
		return "", errPathEscapesWorktree
	}

	candidate := filepath.Join(worktree, cleanFile)

	resolvedWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return "", fmt.Errorf("resolving worktree root: %w", err)
	}

	// EvalSymlinks requires the candidate to exist; a mutation naming a
	// file that doesn't exist fails here with a plain stat-shaped error,
	// which the caller distinguishes from an escape attempt.
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(resolvedWorktree, resolvedCandidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errPathEscapesWorktree
	}

	return resolvedCandidate, nil
}
