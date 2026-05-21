//go:build darwin

package executor

import (
	"os/exec"
	"strconv"
	"strings"
)

// readRSSMB reads the RSS for pid using `ps -o rss= -p <pid>` and returns MB.
// Returns 0 if the process has exited or ps fails.
func readRSSMB(pid int) int {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	kb, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return kb / 1024
}
