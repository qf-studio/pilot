//go:build !unix

package executor

import (
	"errors"
	"os"
)

// errNoFollowUnixOnly is the error of the no-follow helpers off Unix;
// run_as_user is Linux only.
var errNoFollowUnixOnly = errors.New("run_as_user: no-follow open is Unix only")

// openWorkspaceRootFD is not available off Unix; run_as_user is Linux only.
func openWorkspaceRootFD(string) (*os.File, error) { return nil, errNoFollowUnixOnly }

// openWorkspaceNoFollow is not available off Unix; run_as_user is Linux only.
func openWorkspaceNoFollow(*os.File, string, bool) (*os.File, error) {
	return nil, errNoFollowUnixOnly
}

// noFollowRefused is false off Unix.
func noFollowRefused(error) bool { return false }

// openSourceFileNoFollow is not available off Unix; run_as_user is Linux only.
func openSourceFileNoFollow(string) (*os.File, error) { return nil, errNoFollowUnixOnly }

// linkCount is not available off Unix; run_as_user is Linux only.
func linkCount(*os.File) (uint64, error) { return 0, errNoFollowUnixOnly }
