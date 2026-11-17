//go:build !unix

package executor

import (
	"errors"
	"os"
)

// openWorkspaceNoFollow is not available off Unix; run_as_user is Linux only.
func openWorkspaceNoFollow(root, rel string, dir bool) (*os.File, error) {
	return nil, errors.New("run_as_user: no-follow open is Unix only")
}

// noFollowRefused is false off Unix.
func noFollowRefused(error) bool { return false }
