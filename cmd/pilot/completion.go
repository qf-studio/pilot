package main

import (
	"os"

	"github.com/spf13/cobra"
)

func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion scripts",
		Long: `Generate shell completion scripts for Pilot.

To load completions:

Bash:
  $ source <(pilot completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ pilot completion bash > /etc/bash_completion.d/pilot
  # macOS:
  $ pilot completion bash > $(brew --prefix)/etc/bash_completion.d/pilot

Zsh:
  # If shell completion is not already enabled in your environment,
  # you will need to enable it. Execute once:
  $ echo "autoload -U compinit; compinit" >> ~/.zshrc

  # To load completions for each session, execute once:
  $ pilot completion zsh > "${fpath[1]}/_pilot"

  # You will need to start a new shell for this setup to take effect.

Fish:
  $ pilot completion fish | source

  # To load completions for each session, execute once:
  $ pilot completion fish > ~/.config/fish/completions/pilot.fish

PowerShell:
  PS> pilot completion powershell | Out-String | Invoke-Expression

  # To load completions for every new session, run:
  PS> pilot completion powershell > pilot.ps1
  # and source this file from your PowerShell profile.
`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletion(os.Stdout)
			case "zsh":
				return cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				return cmd.Root().GenFishCompletion(os.Stdout, true)
			case "powershell":
				return cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
			}
			return nil
		},
	}

	return cmd
}
