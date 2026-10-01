// GH-5542: test-origin detection for gh-guard denials.
//
// The shim sits on PATH for the whole executor session, so when the model
// runs the quality gates (`make test`) the repo's own unit tests — which
// shell out to `gh` with fixture arguments like `issue comment EPIC-100` —
// hit the shim too. Those denials are correct (the shim still refuses them)
// but they are not the model misbehaving, and journaling them drowned the
// real signal (359 gh_guard_denied warnings on one box). The shim therefore
// inspects its parent process and stamps JournalEntry.TestOrigin; the
// runner-side audit drops stamped entries.
package ghguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// IsTestProcessArgv reports whether argv (a process command line) belongs
// to a Go test binary or a `go test` invocation: argv[0] named `*.test`
// (go's compiled test binaries), any `-test.*` flag (go test passes
// -test.timeout, -test.run, -test.v, ... to the binary), or `go test`
// itself. Pure so it is unit-testable without spawning processes.
func IsTestProcessArgv(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	base := filepath.Base(argv[0])
	if strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe") {
		return true
	}
	if base == "go" && len(argv) > 1 && argv[1] == "test" {
		return true
	}
	for _, a := range argv[1:] {
		if strings.HasPrefix(a, "-test.") {
			return true
		}
	}
	return false
}

// ParentIsTestProcess reports whether the current process's parent is a Go
// test binary. The shim is `exec`'d (not forked) by the sh script, so the
// parent of `pilot gh-guard` is whichever process invoked `gh`. Best-effort
// and fail-safe: if the parent's command line can't be read, it returns
// false so the denial is journaled as usual.
func ParentIsTestProcess() bool {
	return IsTestProcessArgv(processArgv(os.Getppid()))
}

// processArgv returns the command line of pid: /proc on Linux, `ps` as the
// fallback (macOS).
func processArgv(pid int) []string {
	if argv := procArgv(pid); argv != nil {
		return argv
	}
	return psArgv(pid)
}

// procArgv reads the NUL-separated command line from /proc/<pid>/cmdline.
// It returns nil when /proc is unavailable or the entry is empty.
func procArgv(pid int) []string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
}

// psArgv shells out to `ps`. Output is whitespace-split, which is sufficient
// for the prefix/suffix checks in IsTestProcessArgv.
func psArgv(pid int) []string {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // pid is our own parent pid
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}
