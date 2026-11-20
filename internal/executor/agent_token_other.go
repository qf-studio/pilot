//go:build !unix

package executor

import "os"

// agentTokenNoFollow is 0 where the platform has no O_NOFOLLOW; O_EXCL still
// refuses an existing name. run_as_user is Linux only.
const agentTokenNoFollow = 0

// ownedByDaemonUser is false where the platform has no file owner to check:
// the sweep then removes nothing. run_as_user is Linux only.
func ownedByDaemonUser(os.FileInfo) bool { return false }

// tryLockAgentTokenFile takes no lock where the platform has no flock; the
// sweep removes nothing there. run_as_user is Linux only.
func tryLockAgentTokenFile(*os.File) error { return nil }
