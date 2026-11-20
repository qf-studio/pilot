//go:build unix

package executor

import "golang.org/x/sys/unix"

// agentTokenNoFollow makes the create of the temporary token file refuse a
// link at its name.
const agentTokenNoFollow = unix.O_NOFOLLOW
