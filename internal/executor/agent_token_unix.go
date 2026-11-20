//go:build unix

package executor

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// agentTokenNoFollow makes the create of the temporary token file refuse a
// link at its name.
const agentTokenNoFollow = unix.O_NOFOLLOW

// ownedByDaemonUser reports whether fi (of an Lstat) belongs to the effective
// user of the daemon.
func ownedByDaemonUser(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
