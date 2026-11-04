//go:build !unix

package quality

import "os/exec"

// configureGateProcessGroup keeps the default behavior on platforms without
// process groups: a timeout kills only the shell.
func configureGateProcessGroup(_ *exec.Cmd) {}
