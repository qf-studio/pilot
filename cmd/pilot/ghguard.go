// GH-4671: `pilot gh-guard` is the exec target for the gh-guard shim
// (internal/executor/ghguard_spawn.go) — a `gh` shim script prepended onto
// executor subprocess PATHs re-execs this command as
// `pilot gh-guard -- <original gh argv>`. It is never meant to be invoked
// directly by a human; it is Hidden from `pilot --help`.
//
// This is the durable/preventive half of the GH-4649 containment pair (the
// detective half, GH-4670's post-run audit, is already merged). Policy
// classification itself lives in internal/executor/ghguard (pure, unit
// tested); this file is the thin process-boundary glue: read identity from
// env, classify, journal denials, and either refuse or exec the real `gh`.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
	"github.com/spf13/cobra"
)

func newGhGuardCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "gh-guard",
		Hidden:             true,
		DisableFlagParsing: true, // passthrough: argv must reach Classify byte-for-byte, never flag-parsed by cobra
		Short:              "Internal gh CLI interceptor target for the gh-guard shim (GH-4671); not for direct use",
		Long:               ghGuardUsageHint,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := runGhGuard(stripLeadingDoubleDash(args), os.Getenv, os.Stdin, os.Stdout, os.Stderr)
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
}

// stripLeadingDoubleDash removes the "--" separator the shim script always
// places between "gh-guard" and the original `gh` argv (see
// ghGuardShimScript in ghguard_spawn.go: `exec "<pilot>" gh-guard -- "$@"`).
// With DisableFlagParsing set, cobra hands the "--" straight through rather
// than consuming it.
func stripLeadingDoubleDash(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

// runGhGuard is the testable core of the gh-guard command: classify args
// against the identity built from getenv plus gh's own GH_REPO/GH_HOST env
// overrides (ghguard.EnvOverrideFromEnv — GH-4968/D5, since gh itself
// honors those vars and a guard that only reads argv can be bypassed by
// setting them instead of passing -R/--host), and either deny (journal +
// explain on stderr, no exec) or exec the real `gh` with stdin/stdout/
// stderr wired straight through. Returns the process exit code the caller
// should use.
//
// GH-4671 AC3 (fail-safe): a Deny verdict always exits non-zero without
// attempting to exec anything ("fail closed for mutations") regardless of
// whether PILOT_GH_REAL was set. An Allow verdict with no PILOT_GH_REAL
// falls back to a PATH search excluding the shim's own directory
// (ghguard.ResolveFallbackGh) rather than refusing outright ("fail open for
// reads") — only erroring if even that search comes up empty.
func runGhGuard(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "gh-guard: no gh subcommand given")
		return 2
	}

	id := ghguard.IdentityFromEnv(getenv)
	env := ghguard.EnvOverrideFromEnv(getenv)
	decision := ghguard.Classify(id, args, env)

	if decision.Verdict == ghguard.VerdictDeny {
		journalPath := getenv(ghguard.EnvJournalPath)
		entry := ghguard.JournalEntry{
			Time:      time.Now(),
			Verdict:   ghguard.VerdictDeny,
			Reason:    decision.Reason,
			Args:      args,
			TaskIssue: id.TaskIssue,
			TaskRepo:  id.TaskRepo,
			EnvRepo:   decision.EnvRepo,
			EnvHost:   decision.EnvHost,
		}
		// Best-effort: the journal is evidence for the GH-4670 audit to pick
		// up later, never a reason to change the deny decision itself.
		_ = ghguard.AppendJournal(journalPath, entry)

		_, _ = fmt.Fprintf(stderr, "gh-guard: denied `gh %s`\nreason: %s\n%s\n",
			ghguard.FormatArgsForLog(args), decision.Reason, decision.Allowed)
		return 1
	}

	realGh := id.RealGh
	if realGh == "" {
		fallback, err := ghguard.ResolveFallbackGh(getenv(ghguard.EnvShimDir))
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "gh-guard: allowed but no gh binary available to run: %v\n", err)
			return 1
		}
		realGh = fallback
	}

	execCmd := exec.Command(realGh, args...) //nolint:gosec // args are the operator's own gh invocation, classified above
	execCmd.Stdin = stdin
	execCmd.Stdout = stdout
	execCmd.Stderr = stderr
	if err := execCmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if isExitError(err, &exitErr) {
			return exitErr.ExitCode()
		}
		_, _ = fmt.Fprintf(stderr, "gh-guard: failed to run %s: %v\n", realGh, err)
		return 1
	}
	return 0
}

// isExitError is a small indirection around errors.As purely so runGhGuard
// reads linearly; kept private since it's only meaningful for *exec.ExitError.
func isExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

// ghGuardUsageHint is exported for cmd help text consistency; kept tiny and
// unexported-adjacent since gh-guard is Hidden and not part of the public CLI surface.
var ghGuardUsageHint = strings.TrimSpace(`
pilot gh-guard is an internal command invoked by the gh-guard shim
(GH-4671). It is not meant to be run directly.
`)
