package main

import (
	"os"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
	"github.com/spf13/cobra"
)

// newGHGuardCmd returns the `pilot gh-guard` subcommand — the process the
// GH-4671 shim script (backend_claudecode.go's setupGHGuardShim) re-execs
// into for every `gh` call the Claude Code subprocess makes. Hidden: it's
// not a user-facing command, only ever invoked by the shim itself.
//
// DisableFlagParsing is required: the args here ARE a raw `gh` invocation
// (e.g. "issue", "close", "4649", "--repo", "other/repo") and must reach
// ghguard.Classify byte-for-byte, not be reinterpreted as pilot's own
// flags.
func newGHGuardCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "gh-guard",
		Short:              "Internal: gh-guard shim entry point (GH-4671)",
		Hidden:             true,
		DisableFlagParsing: true,
		SilenceErrors:      true,
		SilenceUsage:       true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := ghguard.RunConfig{
				Args:   args,
				RealGH: os.Getenv("PILOT_GH_REAL"),
				TaskCtx: ghguard.TaskContext{
					Issue:  os.Getenv("PILOT_TASK_ISSUE"),
					Repo:   os.Getenv("PILOT_TASK_REPO"),
					Branch: os.Getenv("PILOT_TASK_BRANCH"),
				},
				JournalPath: os.Getenv("PILOT_GH_GUARD_JOURNAL"),
				Stdout:      os.Stdout,
				Stderr:      os.Stderr,
			}
			os.Exit(ghguard.Run(cfg))
			return nil
		},
	}
}
