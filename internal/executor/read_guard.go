package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// readGuardMatcher is the PreToolUse matcher of the read guard: every tool that
// can read a file or run a shell command.
const readGuardMatcher = "Read|Grep|Glob|NotebookRead|Bash"

// readGuardName is the script's file name inside the per-run dir.
const readGuardName = "read-guard.sh"

// readGuardSpec names what the read guard protects: the Pilot home directory
// and the loaded config file. Both come from the executor config.
type readGuardSpec struct {
	Home       string
	ConfigPath string
}

// readGuardScript is the PreToolUse hook. Arguments: Pilot home, config path.
// It reads the hook JSON on stdin; exit 2 blocks the tool call and shows
// stderr to the model.
//
// Decision: this is a guard against accidental reads (GH-6252: an agent
// session read the whole daemon config.yaml with a plain tool call), not a
// security boundary — a determined agent can still read the file through an
// interpreter. The real fix is a separate uid. Under
// --dangerously-skip-permissions only a PreToolUse hook holds, so the guard
// is a hook. Path inputs are resolved through symlinks (nearest existing
// ancestor via plain `realpath`, as in the write guard) so a link into the
// Pilot home does not slip through; Bash commands are matched textually.
// Unparseable input fails closed.
const readGuardScript = `#!/bin/sh
home=$1
cfg=$2
deny() { echo "read blocked: $1 (rule read-guard: the Pilot home, its config and process environments are off limits to agent sessions)" >&2; exit 2; }
input=$(cat)
if command -v jq >/dev/null 2>&1; then
  tool=$(printf '%s' "$input" | jq -r '.tool_name // empty') || deny "cannot parse hook input"
  paths=$(printf '%s' "$input" | jq -r '[.tool_input.file_path, .tool_input.path, .tool_input.notebook_path] | map(select(type == "string" and . != "")) | .[]') || deny "cannot parse hook input"
  pattern=$(printf '%s' "$input" | jq -r '.tool_input.pattern // empty | strings') || deny "cannot parse hook input"
  cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // empty | strings') || deny "cannot parse hook input"
elif command -v python3 >/dev/null 2>&1; then
  py() { printf '%s' "$input" | python3 -I -c "import json,sys; d=json.load(sys.stdin); i=d.get('tool_input') or {}; $1"; }
  tool=$(py 'print(d.get("tool_name") or "")') || deny "cannot parse hook input"
  paths=$(py 'print("\n".join(v for v in (i.get("file_path"), i.get("path"), i.get("notebook_path")) if isinstance(v, str) and v))') || deny "cannot parse hook input"
  pattern=$(py 'v=i.get("pattern"); print(v if isinstance(v, str) else "")') || deny "cannot parse hook input"
  cmd=$(py 'v=i.get("command"); print(v if isinstance(v, str) else "")') || deny "cannot parse hook input"
else
  deny "neither jq nor python3 is available to read the tool input"
fi
[ -n "$tool" ] || deny "tool call carries no tool name"
resolve() {
  p=$1
  tail=
  while [ "$p" != / ]; do
    p=${p%/}
    [ -n "$p" ] || break
    if [ -e "$p" ] || [ -L "$p" ]; then break; fi
    base=${p##*/}
    case $base in
      .|..) return 1 ;;
    esac
    tail=$base${tail:+/$tail}
    p=${p%/*}
    [ -n "$p" ] || p=/
  done
  r=$(realpath -- "$p") || return 1
  case $r in
    /) printf '%s' "/$tail" ;;
    *) printf '%s' "$r${tail:+/$tail}" ;;
  esac
}
real_home=$(resolve "$home") || real_home=$home
real_cfg=$(resolve "$cfg") || real_cfg=$cfg
mentions() {
  case $1 in
    *"$home"*|*"$real_home"*|*"$cfg"*|*"$real_cfg"*) return 0 ;;
  esac
  return 1
}
if [ -n "$paths" ]; then
  while IFS= read -r target; do
    case $target in
      "~") target=$HOME ;;
      "~/"*) target=$HOME/${target#"~/"} ;;
      /*) ;;
      *) target=$PWD/$target ;;
    esac
    resolved=$(resolve "$target") || deny "cannot resolve $target"
    case $resolved in
      "$real_home"|"$real_home"/*|"$real_cfg") deny "$target is inside the Pilot home" ;;
    esac
  done <<EOF_PATHS
$paths
EOF_PATHS
fi
if [ -n "$pattern" ] && mentions "$pattern"; then
  deny "the pattern points into the Pilot home"
fi
if [ -n "$cmd" ]; then
  if mentions "$cmd"; then
    deny "the command touches the Pilot home or its config"
  fi
  case $cmd in
    *.pilot/config*|*"~/.pilot"*|*'$HOME/.pilot'*|*'${HOME}/.pilot'*)
      deny "the command touches the Pilot home or its config" ;;
    */proc/*environ*)
      deny "the command reads a process environment" ;;
  esac
fi
exit 0
`

// setupRunGuards writes the guard scripts and one settings JSON registering
// them as PreToolUse hooks into a fresh temp dir. The read guard is included
// when read is non-nil, the write guard when writeBoundary is non-empty. The
// settings path goes to the CLI as --settings; cleanup removes the dir.
func setupRunGuards(projectPath, writeBoundary string, read *readGuardSpec) (settingsPath string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "pilot-runguard-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("run guards: create dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	fail := func(what string, err error) (string, func(), error) {
		cleanup()
		return "", func() {}, fmt.Errorf("run guards: %s: %w", what, err)
	}

	var entries []any
	addHook := func(matcher, scriptName, script string, args ...string) error {
		scriptPath := filepath.Join(dir, scriptName)
		if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil { //nolint:gosec // hook script must be executable
			return err
		}
		command := shellSingleQuote(scriptPath)
		for _, a := range args {
			command += " " + shellSingleQuote(a)
		}
		entries = append(entries, map[string]any{
			"matcher": matcher,
			"hooks":   []any{map[string]any{"type": "command", "command": command}},
		})
		return nil
	}
	if read != nil {
		if err := addHook(readGuardMatcher, readGuardName, readGuardScript, read.Home, read.ConfigPath); err != nil {
			return fail("write read guard script", err)
		}
	}
	if writeBoundary != "" {
		if err := addHook(writeBoundaryMatcher, writeBoundaryGuardName, writeBoundaryGuardScript, projectPath, writeBoundary); err != nil {
			return fail("write guard script", err)
		}
	}

	raw, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": entries}})
	if err != nil {
		return fail("marshal settings", err)
	}
	settingsPath = filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, raw, 0o600); err != nil {
		return fail("write settings", err)
	}
	return settingsPath, cleanup, nil
}

// readGuardSpec resolves what the read guard protects, or nil when the guard
// is disabled (claude_code.read_guard: false). Home defaults to ~/.pilot and
// the config path to <home>/config.yaml.
func (c *ClaudeCodeConfig) readGuardSpec() *readGuardSpec {
	if !c.ReadGuardEnabled() {
		return nil
	}
	spec := &readGuardSpec{}
	if c != nil {
		spec.Home, spec.ConfigPath = c.PilotHome, c.ConfigPath
	}
	if spec.Home == "" {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			spec.Home = filepath.Join(h, ".pilot")
		}
	}
	if spec.ConfigPath == "" && spec.Home != "" {
		spec.ConfigPath = filepath.Join(spec.Home, "config.yaml")
	}
	if spec.Home == "" {
		return nil
	}
	if abs, err := filepath.Abs(spec.ConfigPath); err == nil {
		spec.ConfigPath = abs
	}
	spec.Home = strings.TrimRight(spec.Home, "/")
	return spec
}

// processClaudeCodeConfig is the Claude Code config the helper spawns without a
// Runner of their own (research subagents, classifiers, subtask parser, intent
// judge, post-merge reviewer) take their read guard settings from. The daemon
// sets it at startup; unset means the defaults: guard on, ~/.pilot.
var processClaudeCodeConfig atomic.Pointer[ClaudeCodeConfig]

// SetReadGuardConfig tells the helper Claude Code spawns which config the read
// guard follows (read_guard switch, Pilot home, loaded config path). GH-6258.
func SetReadGuardConfig(cfg *ClaudeCodeConfig) { processClaudeCodeConfig.Store(cfg) }

// withReadGuard returns args plus "--settings <read guard>" and a cleanup for
// the settings dir. With the guard disabled it returns args and a no-op
// cleanup. A setup failure is an error: a spawn must not run unguarded just
// because the guard could not be written. GH-6258: every Claude Code spawn
// gets the guard, not only the main backend and epic planning.
func withReadGuard(cfg *ClaudeCodeConfig, args []string) ([]string, func(), error) {
	spec := cfg.readGuardSpec()
	if spec == nil {
		return args, func() {}, nil
	}
	settingsPath, cleanup, err := setupRunGuards("", "", spec)
	if err != nil {
		return nil, func() {}, err
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, args...)
	out = append(out, "--settings", settingsPath)
	return out, cleanup, nil
}

// withProcessReadGuard is withReadGuard with the daemon-wide config.
func withProcessReadGuard(args []string) ([]string, func(), error) {
	return withReadGuard(processClaudeCodeConfig.Load(), args)
}
