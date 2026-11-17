//go:build !unix

package executor

import (
	"errors"
	"os"
)

// openWorkspaceRootFD is not available off Unix; run_as_user is Linux only.
func openWorkspaceRootFD(worktreePath string) (*os.File, error) {
	return nil, errors.New("run_as_user: no-follow open is Unix only")
}

// openWorkspaceNoFollow is not available off Unix; run_as_user is Linux only.
func openWorkspaceNoFollow(ws *os.File, rel string, dir bool) (*os.File, error) {
	return nil, errors.New("run_as_user: no-follow open is Unix only")
}

// noFollowRefused is false off Unix.
func noFollowRefused(error) bool { return false }

// openFileNoFollow is os.Open off Unix; run_as_user is Linux only.
func openFileNoFollow(path string) (*os.File, error) {
	return os.Open(path) // #nosec G304 -- the daemon's own .agent directory.
}

// fileLinkCount is 1 off Unix.
func fileLinkCount(os.FileInfo) uint64 { return 1 }
