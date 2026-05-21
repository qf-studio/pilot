//go:build linux

package executor

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// readRSSMB reads the VmRSS field from /proc/<pid>/status and returns MB.
// Returns 0 if the file cannot be read (process may have exited).
func readRSSMB(pid int) int {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			// Format: "VmRSS:\t   12345 kB"
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.Atoi(fields[1])
				if err == nil {
					return kb / 1024
				}
			}
		}
	}
	return 0
}
