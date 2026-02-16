// Package main provides the onboard backend selection stage.
// GH-1340: Backend selection for pilot onboard command.
package main

import (
	"fmt"
	"os/exec"

	"github.com/alekspetrov/pilot/internal/executor"
)

// BackendOption represents an available execution backend
type BackendOption struct {
	Name        string
	Type        string // config value: "claude-code", "qwen-code", "opencode"
	Description string
	CLICommand  string // command to check with exec.LookPath
	Installed   bool
}

// detectBackends checks which backend CLIs are installed
func detectBackends() []BackendOption {
	backends := []BackendOption{
		{
			Name:        "Claude Code",
			Type:        "claude-code",
			Description: "Anthropic's CLI",
			CLICommand:  "claude",
		},
		{
			Name:        "Qwen Code",
			Type:        "qwen-code",
			Description: "Alibaba's open-source CLI",
			CLICommand:  "qwen",
		},
		{
			Name:        "OpenCode",
			Type:        "opencode",
			Description: "Server/client architecture",
			CLICommand:  "opencode",
		},
	}

	// Check which CLIs are installed
	for i := range backends {
		_, err := exec.LookPath(backends[i].CLICommand)
		backends[i].Installed = err == nil
	}

	return backends
}

// onboardBackendSetup runs the backend selection stage
func onboardBackendSetup(state *OnboardState) error {
	printStageHeader("EXECUTION BACKEND", state.CurrentStage, state.StagesTotal)
	fmt.Println()

	backends := detectBackends()

	// Count installed backends
	installedCount := 0
	var singleInstalled *BackendOption
	for i := range backends {
		if backends[i].Installed {
			installedCount++
			singleInstalled = &backends[i]
		}
	}

	// If only one backend is installed, auto-select it
	if installedCount == 1 {
		fmt.Printf("  Detected: %s %s\n",
			onboardSuccessStyle.Render("✓"),
			singleInstalled.Name)
		fmt.Printf("  %s\n", onboardDimStyle.Render(singleInstalled.Description))
		fmt.Println()

		// Initialize executor config if needed
		if state.Config.Executor == nil {
			state.Config.Executor = executor.DefaultBackendConfig()
		}
		state.Config.Executor.Type = singleInstalled.Type

		fmt.Printf("  %s Backend: %s\n",
			onboardSuccessStyle.Render("✓"),
			onboardValueStyle.Render(singleInstalled.Name))

		fmt.Println()
		printStageFooter()
		return nil
	}

	// Show menu
	fmt.Println("  Which AI coding backend should Pilot use?")
	fmt.Println()

	for i, b := range backends {
		status := ""
		if b.Installed {
			status = onboardSuccessStyle.Render(" ✓")
		} else {
			status = onboardDimStyle.Render(" (not installed)")
		}

		defaultMarker := ""
		if i == 0 {
			defaultMarker = onboardDimStyle.Render(" (default)")
		}

		fmt.Printf("    %s %s — %s%s%s\n",
			onboardValueStyle.Render(fmt.Sprintf("[%d]", i+1)),
			b.Name,
			onboardDimStyle.Render(b.Description),
			status,
			defaultMarker)
	}
	fmt.Println()

	// Prompt for selection
	fmt.Printf("  Your choice %s ", onboardCursorStyle.Render("[1]:"))
	line := readLine(state.Reader)

	// Parse selection (default to 1)
	idx := 1
	if line != "" {
		if _, err := fmt.Sscanf(line, "%d", &idx); err != nil || idx < 1 || idx > len(backends) {
			idx = 1
		}
	}

	selected := backends[idx-1]

	// Initialize executor config if needed
	if state.Config.Executor == nil {
		state.Config.Executor = executor.DefaultBackendConfig()
	}
	state.Config.Executor.Type = selected.Type

	// If OpenCode is selected, prompt for server URL
	if selected.Type == "opencode" {
		fmt.Println()
		fmt.Print("  OpenCode server URL [http://localhost:8080]: ")
		serverURL := readLine(state.Reader)
		if serverURL == "" {
			serverURL = "http://localhost:8080"
		}
		if state.Config.Executor.OpenCode == nil {
			state.Config.Executor.OpenCode = &executor.OpenCodeConfig{}
		}
		state.Config.Executor.OpenCode.ServerURL = serverURL
	}

	// Show confirmation
	fmt.Println()
	if !selected.Installed {
		fmt.Printf("  %s %s is not installed. Install it before running Pilot.\n",
			onboardDimStyle.Render("⚠"),
			selected.Name)
	}
	fmt.Printf("  %s Backend: %s\n",
		onboardSuccessStyle.Render("✓"),
		onboardValueStyle.Render(selected.Name))

	fmt.Println()
	printStageFooter()
	return nil
}
