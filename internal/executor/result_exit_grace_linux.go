//go:build linux

package executor

import (
	"fmt"
	"os"
	"strconv"
)

// survivingChildCmdlines returns the redacted command lines of processes in
// process group pgid other than the leader itself (GH-5530), for the WARN log
// naming what kept the subprocess alive. Best-effort: unreadable entries are
// skipped and the result is capped at maxSurvivorsLogged.
func survivingChildCmdlines(pgid int) []string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil || pid == pgid {
			continue
		}
		statPGID, _, _, statErr := readProcStat(pid)
		if statErr != nil || statPGID != pgid {
			continue
		}
		raw, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if readErr != nil {
			continue
		}
		if line := formatSurvivorCmdline(raw); line != "" {
			out = append(out, line)
		}
		if len(out) >= maxSurvivorsLogged {
			break
		}
	}
	return out
}
