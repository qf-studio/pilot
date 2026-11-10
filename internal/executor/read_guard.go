package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/qf-studio/pilot/internal/logging"
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
// is a hook. Path inputs (Read file_path, Grep/Glob path, NotebookRead
// notebook_path) are resolved through symlinks (nearest existing ancestor via
// plain realpath, as in the write guard) so a link into the Pilot home does
// not slip through. Grep/Glob pattern text is never checked.
//
// GH-6267: a Bash command is tokenized (quotes honoured, separators split into
// simple commands, a leading ~, ~user, $HOME and ${HOME} expanded, cd tracked)
// and each path-looking token is resolved through the same resolve() as a Read
// path, instead of matching substrings, so "grep -rn ~/.pilot docs/" or a
// commit message that mentions the path is not a read. Words after -m,
// --message, -e, --regexp and the pattern of grep/rg are text. A recursive read
// (Grep/Glob path, grep -r, rg, find, ls -R, tree, du) of a parent of the Pilot
// home is denied too, except /tmp and the project dir. Only unparseable hook
// JSON fails closed; a token that cannot be resolved is allowed.
const readGuardScript = `#!/bin/sh
home=$1
cfg=$2
nl='
'
tab=$(printf '\t')
bt=$(printf '\140')
deny() { echo "read blocked: $1 (rule read-guard: the Pilot home, its config and process environments are off limits to agent sessions)" >&2; exit 2; }
input=$(cat)
if command -v jq >/dev/null 2>&1; then
  tool=$(printf '%s' "$input" | jq -r '.tool_name // empty' 2>/dev/null) || deny "cannot parse hook input"
  paths=$(printf '%s' "$input" | jq -r '[.tool_input.file_path, .tool_input.path, .tool_input.notebook_path] | map(select(type == "string" and . != "")) | .[]' 2>/dev/null) || deny "cannot parse hook input"
  cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // empty | strings' 2>/dev/null) || deny "cannot parse hook input"
elif command -v python3 >/dev/null 2>&1; then
  py() { printf '%s' "$input" | python3 -I -c "import json,sys; d=json.load(sys.stdin); i=d.get('tool_input') or {}; $1" 2>/dev/null; }
  tool=$(py 'print(d.get("tool_name") or "")') || deny "cannot parse hook input"
  paths=$(py 'print("\n".join(v for v in (i.get("file_path"), i.get("path"), i.get("notebook_path")) if isinstance(v, str) and v))') || deny "cannot parse hook input"
  cmd=$(py 'v=i.get("command"); print(v if isinstance(v, str) else "")') || deny "cannot parse hook input"
else
  deny "neither jq nor python3 is available to read the tool input"
fi
[ -n "$tool" ] || deny "tool call carries no tool name"
# resolve prints the physical path of $1: the nearest existing ancestor goes
# through realpath, the missing rest is appended. A "." or ".." in the missing
# part is applied lexically (the missing directory cannot be a symlink).
resolve() {
  p=$1
  tail=
  dd=0
  while [ "$p" != / ]; do
    p=${p%/}
    [ -n "$p" ] || break
    if [ -e "$p" ] || [ -L "$p" ]; then break; fi
    base=${p##*/}
    case $base in
      .) ;;
      ..) dd=$((dd+1)) ;;
      *) if [ "$dd" -gt 0 ]; then dd=$((dd-1)); else tail=$base${tail:+/$tail}; fi ;;
    esac
    p=${p%/*}
    [ -n "$p" ] || p=/
  done
  r=$(realpath -- "$p") || return 1
  while [ "$dd" -gt 0 ]; do
    r=${r%/*}
    [ -n "$r" ] || r=/
    dd=$((dd-1))
  done
  case $r in
    /) printf '%s' "/$tail" ;;
    *) printf '%s' "$r${tail:+/$tail}" ;;
  esac
}
real_home=$(resolve "$home") || real_home=$home
real_cfg=$(resolve "$cfg") || real_cfg=$cfg
real_pwd=$(resolve "$PWD") || real_pwd=$PWD
real_tmp=$(resolve /tmp) || real_tmp=/tmp
cwd=$PWD
# expand sets ex to $1 with a leading ~, ~user, $HOME or ${HOME} expanded and
# a relative path made absolute against cwd.
expand() {
  ex=$1
  case $ex in
    "~") ex=$HOME ;;
    "~/"*) ex=$HOME/${ex#"~/"} ;;
    "~"*)
      uname_=${ex#"~"}
      uname_=${uname_%%/*}
      case $uname_ in
        ""|*[!A-Za-z0-9_.-]*) ;;
        *)
          uhome=$(eval "printf '%s' ~$uname_" 2>/dev/null)
          case $uhome in
            /*) ex=$uhome${ex#"~$uname_"} ;;
          esac ;;
      esac ;;
    '$HOME'|'$HOME/'*) ex=$HOME${ex#'$HOME'} ;;
    '${HOME}'|'${HOME}/'*) ex=$HOME${ex#'${HOME}'} ;;
  esac
  case $ex in
    /*) ;;
    *) ex=$cwd/$ex ;;
  esac
}
# is_ancestor: $1 is / or a parent of the Pilot home, as configured or resolved
# (the home may itself be a symlink).
is_ancestor() {
  [ "$1" = / ] && return 0
  case $real_home in
    "$1"/*) return 0 ;;
  esac
  case $home in
    "$1"/*) return 0 ;;
  esac
  return 1
}
in_project() {
  [ "$real_pwd" = / ] && return 1
  case $1 in
    "$real_pwd"|"$real_pwd"/*) return 0 ;;
  esac
  return 1
}
# check_target denies an absolute path that is inside the Pilot home, equals the
# config file, is a process environment, or, when $2 is 1 (a recursive read), is
# a parent of the Pilot home. A path that cannot be resolved is allowed. Sets
# last to the resolved path.
check_target() {
  t=$1
  last=
  case $t in
    *[*?[]*)
      case $real_home in $t) deny "$t reaches the Pilot home" ;; esac
      case $real_cfg in $t) deny "$t reaches the Pilot config" ;; esac
      case $real_home/_ in $t) deny "$t reaches the Pilot home" ;; esac
      return 0 ;;
  esac
  r=$(resolve "$t") || return 0
  last=$r
  case $r in
    "$real_home"|"$real_home"/*|"$real_cfg") deny "$t is inside the Pilot home" ;;
    /proc/self/environ|/proc/thread-self/environ|/proc/[0-9]*/environ|/proc/[0-9]*/task/[0-9]*/environ) deny "$t is a process environment" ;;
  esac
  if [ "$2" = 1 ] && is_ancestor "$r" && [ "$r" != /tmp ] && [ "$r" != "$real_tmp" ] && ! in_project "$r"; then
    deny "$t is a parent of the Pilot home and is read recursively"
  fi
  return 0
}
# run_seg checks one simple command given as words. Words after -m, --message,
# -e, --regexp and the pattern of grep/rg are text, not paths.
run_seg() {
  while [ $# -gt 0 ]; do
    case $1 in
      *=*)
        n=${1%%=*}
        case $n in ""|[0-9]*|*[!A-Za-z0-9_]*) break ;; esac
        shift ;;
      sudo|env|time|nice|nohup|exec|command|builtin|xargs)
        shift
        while [ $# -gt 0 ]; do
          case $1 in -*) shift ;; *) break ;; esac
        done ;;
      *) break ;;
    esac
  done
  [ $# -gt 0 ] || return 0
  name=${1##*/}
  shift
  sr=0
  grepish=0
  has_pat=0
  case $name in rg|ag|ack|fd|find|tree|du) sr=1 ;; esac
  case $name in grep|egrep|fgrep|zgrep|rg|ag|ack) grepish=1 ;; esac
  prev=
  endo=0
  for a; do
    if [ $endo = 0 ]; then
      case $a in
        --) endo=1 ;;
        --recursive|--dereference-recursive|--directories=recurse) sr=1 ;;
        --regexp|--regexp=*|--file|--file=*) has_pat=1 ;;
        --*) ;;
        -?*)
          case $name in
            grep|egrep|fgrep|zgrep) case $a in *[rR]*) sr=1 ;; esac ;;
            ls) case $a in *R*) sr=1 ;; esac ;;
          esac
          case $a in -e*|-f) has_pat=1 ;; esac ;;
        recurse) [ "$prev" = -d ] && sr=1 ;;
      esac
    fi
    prev=$a
  done
  need_pat=0
  [ $grepish = 1 ] && [ $has_pat = 0 ] && need_pat=1
  prev=
  endo=0
  npaths=0
  for a; do
    p=$prev
    prev=$a
    if [ $endo = 0 ]; then
      case $p in
        -m|--message|-e|--regexp|-A|-B|-C|-g|--glob|--max-count|--include|--exclude|--exclude-dir|--after-context|--before-context|--context|-t|--type|-d|--directories) continue ;;
      esac
      case $a in
        --) endo=1; continue ;;
        --message=*|--regexp=*|--max-count=*|--glob=*|--include=*|--exclude=*|--exclude-dir=*) continue ;;
        -?*=*) a=${a#*=} ;;
        -?*) continue ;;
      esac
    fi
    if [ $need_pat = 1 ]; then
      need_pat=0
      continue
    fi
    islike=0
    case $a in
      */*|"~"*|.*|'$HOME'*|'${HOME}'*|*[*?[]*) islike=1 ;;
    esac
    if [ "$name" = cd ]; then
      [ "$a" = - ] && continue
      islike=1
    fi
    [ $islike = 1 ] || continue
    npaths=$((npaths+1))
    expand "$a"
    check_target "$ex" "$sr"
    if [ "$name" = cd ] && [ -n "$last" ]; then
      cwd=$last
    fi
  done
  if [ $sr = 1 ] && [ $npaths -eq 0 ]; then
    check_target "$cwd" 1
  fi
  return 0
}
tool_rec=0
case $tool in Grep|Glob) tool_rec=1 ;; esac
if [ -n "$paths" ]; then
  while IFS= read -r target; do
    expand "$target"
    check_target "$ex" "$tool_rec"
  done <<EOF_PATHS
$paths
EOF_PATHS
fi
if [ -n "$cmd" ]; then
  dq='"'
  sq="'"
  bs='\'
  set --
  tok=
  have=0
  rest=$cmd
  while [ -n "$rest" ]; do
    c=${rest%"${rest#?}"}
    rest=${rest#?}
    etok=0
    eseg=0
    case $c in
      "$sq")
        have=1
        seg=${rest%%"$sq"*}
        if [ "$seg" = "$rest" ]; then
          tok=$tok$rest
          rest=
        else
          tok=$tok$seg
          rest=${rest#"$seg$sq"}
        fi ;;
      "$dq")
        have=1
        while [ -n "$rest" ]; do
          a=${rest%%"$dq"*}
          b=${rest%%"$bs"*}
          if [ "${#b}" -lt "${#a}" ]; then
            tok=$tok$b
            rest=${rest#"$b"}
            rest=${rest#?}
            e=${rest%"${rest#?}"}
            rest=${rest#?}
            tok=$tok$e
          elif [ "$a" = "$rest" ]; then
            tok=$tok$rest
            rest=
          else
            tok=$tok$a
            rest=${rest#"$a$dq"}
            break
          fi
        done ;;
      "$bs")
        e=${rest%"${rest#?}"}
        rest=${rest#?}
        if [ "$e" != "$nl" ]; then
          tok=$tok$e
          have=1
        fi ;;
      " "|"$tab"|"<"|">") etok=1 ;;
      ";"|"|"|"&"|"("|")"|"$nl"|"$bt") etok=1; eseg=1 ;;
      "#")
        if [ $have = 0 ]; then
          etok=1
          eseg=1
          case $rest in
            *"$nl"*) rest=${rest#*"$nl"} ;;
            *) rest= ;;
          esac
        else
          tok=$tok$c
        fi ;;
      *)
        tok=$tok$c
        have=1 ;;
    esac
    if [ $etok = 1 ]; then
      if [ $have = 1 ]; then
        set -- "$@" "$tok"
      fi
      tok=
      have=0
    fi
    if [ $eseg = 1 ]; then
      run_seg "$@"
      set --
    fi
  done
  if [ $have = 1 ]; then
    set -- "$@" "$tok"
  fi
  run_seg "$@"
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

// readGuardOffForProcess is set by PreflightReadGuard when the host lacks a tool
// the guard script needs. ReadGuardEnabled then reports false for every config.
var readGuardOffForProcess atomic.Bool

// readGuardMissingTools names the host tools the guard script needs and
// lookPath cannot find: realpath, and jq or python3.
func readGuardMissingTools(lookPath func(string) (string, error)) []string {
	var missing []string
	if _, err := lookPath("realpath"); err != nil {
		missing = append(missing, "realpath")
	}
	_, jqErr := lookPath("jq")
	_, pyErr := lookPath("python3")
	if jqErr != nil && pyErr != nil {
		missing = append(missing, "jq or python3")
	}
	return missing
}

// PreflightReadGuard checks the host tools the read guard script runs on. When
// the guard is on and a tool is missing it logs one Error line naming the tool
// and turns the guard off for the process, because a broken guard blocks every
// Read, Grep, Glob and Bash call of every run (GH-6267). It reports whether it
// turned the guard off.
func PreflightReadGuard(cfg *ClaudeCodeConfig) bool {
	return preflightReadGuard(cfg, exec.LookPath)
}

func preflightReadGuard(cfg *ClaudeCodeConfig, lookPath func(string) (string, error)) bool {
	if !cfg.ReadGuardEnabled() {
		return false
	}
	missing := readGuardMissingTools(lookPath)
	if len(missing) == 0 {
		return false
	}
	logging.WithComponent("read-guard").Error("Read guard turned off: the host is missing a tool the guard script needs; agent sessions can read the Pilot home until it is installed",
		slog.String("missing", strings.Join(missing, ", ")))
	readGuardOffForProcess.Store(true)
	return true
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

// ReadGuardConfig returns the daemon-wide read guard config the helper spawns
// follow, nil when none was set (defaults apply).
func ReadGuardConfig() *ClaudeCodeConfig { return processClaudeCodeConfig.Load() }

// SetReadGuardConfig tells the helper Claude Code spawns which config the read
// guard follows (read_guard switch, Pilot home, loaded config path). GH-6258.
func SetReadGuardConfig(cfg *ClaudeCodeConfig) { processClaudeCodeConfig.Store(cfg) }

// withReadGuard returns args plus "--settings <read guard>" and a cleanup for
// the settings dir. With the guard disabled it returns args and a no-op
// cleanup. A setup failure is an error: a spawn must not run unguarded just
// because the guard could not be written. GH-6258: every Claude Code spawn
// gets the guard, not only the main backend and epic planning.
func withReadGuard(cfg *ClaudeCodeConfig, args []string) ([]string, func(), error) {
	if !cfg.ReadGuardEnabled() {
		return args, func() {}, nil
	}
	spec := cfg.readGuardSpec()
	if spec == nil {
		// Enabled, but the Pilot home cannot be resolved (no PilotHome and no
		// user home dir): that is not the same as the guard being switched
		// off. Fail closed.
		return nil, func() {}, errors.New("run guards: cannot resolve the Pilot home to protect (set HOME or claude_code.read_guard: false)")
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
