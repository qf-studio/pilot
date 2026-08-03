//go:build linux

package executor

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// probeProcessLiveness scans /proc for every process whose process group id
// (pgid) matches pgid — which, thanks to configureProcessGroup's Setpgid, is
// the same value as the tracked subprocess's own PID — and returns how many
// such processes exist besides the leader itself, plus their combined
// utime+stime CPU ticks (jiffies).
//
// GH-4668: a claude-code child running a long local tool (e.g. `make test`)
// forks that tool inside the same process group. The leader's own stdout
// goes silent for the tool's duration, but the group as a whole keeps
// accumulating CPU time and gains live descendant PIDs — both distinguish
// "silent because busy" from "silent because hung".
func probeProcessLiveness(pgid int) (processLivenessSnapshot, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return processLivenessSnapshot{}, fmt.Errorf("read /proc: %w", err)
	}

	var snap processLivenessSnapshot
	found := false
	for _, e := range entries {
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil {
			continue // not a pid directory (self, cpuinfo, etc.)
		}

		statPGID, utime, stime, statErr := readProcStat(pid)
		if statErr != nil {
			continue // process exited between ReadDir and stat read — not fatal
		}
		if statPGID != pgid {
			continue
		}
		found = true
		snap.cpuTicks += utime + stime
		if pid != pgid {
			snap.descendants++
		}
	}

	if !found {
		// The leader itself should always be found while it's alive. If the
		// scan found nothing at all for this pgid, that's evidence the scan
		// itself is broken (e.g. /proc unreadable in this context) rather
		// than evidence the group is empty — surface it so the caller fails
		// toward the safe kill-on-silence path instead of reporting a false
		// "no descendants, no CPU".
		return processLivenessSnapshot{}, fmt.Errorf("no /proc entries found for pgid %d", pgid)
	}

	return snap, nil
}

// readProcStat parses /proc/<pid>/stat and returns its pgrp (process group
// id) and utime/stime CPU ticks. The comm field (2nd field) is delimited by
// parentheses and may itself contain spaces or parentheses, so the parse
// skips to the last ')' before splitting the remaining whitespace-separated
// fields (see `man 5 proc`): state(1) ppid(2) pgrp(3) session(4) tty_nr(5)
// tpgid(6) flags(7) minflt(8) cminflt(9) majflt(10) cmajflt(11) utime(12)
// stime(13) ...
func readProcStat(pid int) (pgrp int, utime, stime uint64, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, 0, err
	}
	line := string(data)
	idx := strings.LastIndex(line, ")")
	if idx < 0 || idx+2 > len(line) {
		return 0, 0, 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(line[idx+2:])
	// After skipping "pid (comm) ", fields[0]=state fields[1]=ppid
	// fields[2]=pgrp ... fields[11]=utime fields[12]=stime.
	const minFields = 13
	if len(fields) < minFields {
		return 0, 0, 0, fmt.Errorf("short /proc/%d/stat: %d fields", pid, len(fields))
	}
	pgrp, err = strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, 0, err
	}
	utime, err = strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	stime, err = strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	return pgrp, utime, stime, nil
}
