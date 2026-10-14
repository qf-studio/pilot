package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteBoundaryDenyAll is a WriteBoundary value that blocks every write. It is
// an absolute path outside any project, so the guard's containment check can
// never pass; a relative name would be creatable by the very Write it guards.
const WriteBoundaryDenyAll = "/dev/null"

// writeBoundaryMatcher is the PreToolUse matcher of the guard hook: every
// Claude Code tool that writes a file.
const writeBoundaryMatcher = "Write|Edit|MultiEdit|NotebookEdit"

// writeBoundaryGuardName is the script's file name inside the per-run dir.
const writeBoundaryGuardName = "write-guard.sh"

// writeBoundaryGuardScript is the PreToolUse hook. Arguments: project dir,
// boundary (relative to the project dir, or absolute). It reads the hook JSON
// on stdin and exits 0 only when the tool's target path resolves inside the
// boundary; exit 2 blocks the tool call and shows stderr to the model.
//
// Decision: --allowedTools is inert under --dangerously-skip-permissions and
// Claude Code has no "deny Write except under X" rule, so the boundary is a
// hook (hooks do run under skip-permissions). The path goes through
// `realpath -m` so a symlink inside the boundary pointing out of it, and
// "..", cannot escape; the boundary dir must itself resolve inside the project.
// Everything unresolvable fails closed.
const writeBoundaryGuardScript = `#!/bin/sh
proj=$1
boundary=$2
deny() { echo "write blocked: $1 (writes are limited to ${boundary} in this session)" >&2; exit 2; }
input=$(cat)
if command -v jq >/dev/null 2>&1; then
  target=$(printf '%s' "$input" | jq -r '.tool_input.file_path // .tool_input.notebook_path // empty') || deny "cannot parse hook input"
elif command -v python3 >/dev/null 2>&1; then
  target=$(printf '%s' "$input" | python3 -I -c 'import json,sys; i=json.load(sys.stdin).get("tool_input") or {}; print(i.get("file_path") or i.get("notebook_path") or "")') || deny "cannot parse hook input"
else
  deny "neither jq nor python3 is available to read the tool input"
fi
[ -n "$target" ] || deny "tool call carries no file path"
real_proj=$(realpath -- "$proj") || deny "cannot resolve the project directory"
case $target in
  /*) ;;
  *) target=$real_proj/$target ;;
esac
case $boundary in
  /*) root=$boundary ;;
  *) root=$real_proj/$boundary ;;
esac
resolved=$(realpath -m -- "$target") || deny "cannot resolve $target"
real_root=$(realpath -m -- "$root") || deny "cannot resolve the boundary directory"
case $real_root in
  "$real_proj"/*) ;;
  *) deny "$target is outside the writable area" ;;
esac
case $resolved in
  "$real_root"/*) exit 0 ;;
esac
deny "$target is outside the writable area"
`

// setupWriteBoundary writes the guard script and a settings JSON that
// registers it as a PreToolUse hook into a fresh temp dir. The settings path
// goes to the CLI as --settings; cleanup removes both files.
func setupWriteBoundary(projectPath, boundary string) (settingsPath string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "pilot-writeguard-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("write boundary: create dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	scriptPath := filepath.Join(dir, writeBoundaryGuardName)
	if err := os.WriteFile(scriptPath, []byte(writeBoundaryGuardScript), 0o755); err != nil { //nolint:gosec // hook script must be executable
		cleanup()
		return "", func() {}, fmt.Errorf("write boundary: write guard script: %w", err)
	}

	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": writeBoundaryMatcher,
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": shellSingleQuote(scriptPath) + " " + shellSingleQuote(projectPath) + " " + shellSingleQuote(boundary),
						},
					},
				},
			},
		},
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write boundary: marshal settings: %w", err)
	}
	settingsPath = filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, raw, 0o600); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write boundary: write settings: %w", err)
	}
	return settingsPath, cleanup, nil
}

// shellSingleQuote quotes s for a POSIX shell command line.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
