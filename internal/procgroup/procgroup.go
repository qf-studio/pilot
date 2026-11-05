// Package procgroup puts a subprocess in its own process group so a kill
// reaches every descendant it forked, and bounds cmd.Wait() on inherited
// stdout/stderr pipes. Shared by the executor's agent subprocesses (GH-4503)
// and the quality gate runner (GH-6081).
package procgroup

import "time"

// DefaultWaitDelay bounds how long cmd.Wait() will block on inherited
// stdout/stderr pipe fds after the tracked process has exited or been
// signalled. Without it, a surviving grandchild that still holds a pipe fd
// open (e.g. a backgrounded Bash-tool child — GH-4357's task_started/
// task_notification events prove Claude Code forks these) can hang
// cmd.Wait() forever: os/exec's io-copy goroutines block on Read() until
// every fd referencing the pipe's write end is closed, and Go never closes
// them on its own unless WaitDelay is set (GH-4503, pilot-console#24 —
// gen-0's claude process survived its session kill for 1h14m).
const DefaultWaitDelay = 5 * time.Second
