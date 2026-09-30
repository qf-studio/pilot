package executor

import (
	"strings"
	"sync/atomic"
	"time"
)

// GH-5530: a Claude Code session can report a successful stream-json `result`
// event while the `claude` process stays alive because a Bash tool call that
// outlived its tool timeout is still running and holds the stdout/stderr
// pipes. The stdout reader never sees EOF, so without this the executor waited
// for the full task timeout before the GH-2107 recovery path salvaged work the
// model had already finished. The backend arms a grace timer on the success
// result (ClaudeCodeConfig.ResultExitGrace) and kills the process group when
// it expires.

// resultExitGraceKills counts process-group kills issued because the
// subprocess outlived its grace after a success result
// (pilot_executor_result_exit_grace_kills_total).
var resultExitGraceKills atomic.Int64

// ResultExitGraceKillsTotal returns the process-lifetime number of
// result-exit-grace kills, exported for the Prometheus exporter.
func ResultExitGraceKillsTotal() int64 {
	return resultExitGraceKills.Load()
}

// resultExitGraceDrain bounds how long, after the group kill, the backend
// waits for the readers to hit EOF before force-closing the pipes. Covers a
// survivor that escaped the process group (e.g. setsid) and still holds them.
const resultExitGraceDrain = 5 * time.Second

const (
	maxSurvivorsLogged   = 5
	maxSurvivorCmdlineCh = 300
)

// formatSurvivorCmdline renders a raw /proc/<pid>/cmdline (NUL-separated argv)
// as one line, run through the same redaction applied to evidence output so
// secret-shaped argv values never reach the log.
func formatSurvivorCmdline(raw []byte) string {
	s := strings.TrimSpace(strings.ReplaceAll(strings.TrimRight(string(raw), "\x00"), "\x00", " "))
	s = redactOutputForEvidence(s)
	if len(s) > maxSurvivorCmdlineCh {
		s = s[:maxSurvivorCmdlineCh] + "..."
	}
	return s
}
