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
// commit message that mentions the path is not a read. Text words are per
// command: the word after -m or --message of git, and the word after -e,
// --regexp, -g, -t, --include, -A, -B, -C and the first non-option word of
// grep and rg; for every other command a word after a flag is a path (ls -t,
// cat -A). Nested commands are commands: the inside of a command substitution
// (also in double quotes, also a backtick form), the script of bash/sh/zsh -c
// and of eval are queued and scanned by the same rules; the value of an env
// assignment is checked; here-document bodies are text. A glob is matched one
// path segment per star. A recursive read (Grep/Glob path, grep -r, rg, find,
// ls -R, tree, du) of a parent of the Pilot home is denied too, except /tmp and
// the project dir. Only unparseable hook JSON fails closed; a token that cannot
// be resolved is allowed.
const readGuardScript = `#!/bin/sh
home=$1
cfg=$2
nl='
'
tab=$(printf '\t')
bt=$(printf '\140')
us=$(printf '\037')
rs=$(printf '\036')
dp='$('
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
  r=$(realpath -- "$p" 2>/dev/null) || return 1
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
# glob_reach: the leading path segments of the glob $1 match every segment of
# the path $2, so $1 names $2 or something inside it. A star matches within one
# segment only, never across a "/".
glob_reach() {
  gp=${1#/}
  gt=${2#/}
  while [ -n "$gt" ]; do
    gps=${gp%%/*}
    gts=${gt%%/*}
    case $gts in $gps) ;; *) return 1 ;; esac
    case $gp in */*) gp=${gp#*/} ;; *) gp= ;; esac
    case $gt in */*) gt=${gt#*/} ;; *) gt= ;; esac
  done
  return 0
}
# check_target denies an absolute path that is inside the Pilot home, equals the
# config file, is a process environment, or, when $2 is 1 (a recursive read), is
# a parent of the Pilot home. A glob is matched one path segment per star. A
# path that cannot be resolved is allowed. Sets last to the resolved path.
check_target() {
  t=$1
  last=
  case $t in
    *[*?[]*)
      if glob_reach "$t" "$real_home" || glob_reach "$t" "$home" || glob_reach "$t" "$real_cfg"; then
        deny "$t reaches the Pilot home"
      fi
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
# check_word checks $1 as a path candidate when it looks like a path.
check_word() {
  case $1 in
    */*|"~"*|.*|'$HOME'*|'${HOME}'*|*[*?[]*)
      expand "$1"
      check_target "$ex" "$2" ;;
  esac
}
# text_arg succeeds when $1, the word before the current one, makes the current
# word text instead of a path. The text words are per command: the message of
# git, and the pattern and filter options of grep and rg.
text_arg() {
  case $name in
    git) case $1 in -m|--message|-[!-]*m) return 0 ;; esac ;;
    grep|egrep|fgrep|zgrep|rg)
      case $1 in -e|--regexp|-A|-B|-C|-g|--glob|-t|--type|--include|--exclude|--exclude-dir|--after-context|--before-context|--context) return 0 ;; esac ;;
  esac
  return 1
}
# text_opt succeeds when $1 is a long option whose "=value" is text.
text_opt() {
  case $name in
    git) case $1 in --message) return 0 ;; esac ;;
    grep|egrep|fgrep|zgrep|rg)
      case $1 in --regexp|--glob|--type|--include|--exclude|--exclude-dir|--after-context|--before-context|--context) return 0 ;; esac ;;
  esac
  return 1
}
# push queues the text $1 as a command of its own, run from the current cwd.
queue=
push() { queue=${queue:+$queue$us}$cwd$rs$1; }
# take_paren: $1 is the text after a dollar-paren. Sets tp_inner to the text up
# to the matching close paren and tp_after to the text past it. An unbalanced
# paren takes the rest.
take_paren() {
  tp_rest=$1
  tp_inner=
  tp_after=
  tp_depth=1
  while [ -n "$tp_rest" ]; do
    tp_pre=${tp_rest%%[()]*}
    if [ "$tp_pre" = "$tp_rest" ]; then
      tp_inner=$tp_inner$tp_rest
      return 0
    fi
    tp_ch=${tp_rest#"$tp_pre"}
    tp_ch=${tp_ch%"${tp_ch#?}"}
    tp_rest=${tp_rest#"$tp_pre$tp_ch"}
    if [ "$tp_ch" = "(" ]; then
      tp_depth=$((tp_depth+1))
    else
      tp_depth=$((tp_depth-1))
      if [ "$tp_depth" = 0 ]; then
        tp_inner=$tp_inner$tp_pre
        tp_after=$tp_rest
        return 0
      fi
    fi
    tp_inner=$tp_inner$tp_pre$tp_ch
  done
  return 0
}
# take_bt: $1 is the text after a backtick. Same outputs as take_paren.
take_bt() {
  tp_inner=${1%%"$bt"*}
  if [ "$tp_inner" = "$1" ]; then tp_after=; else tp_after=${1#*"$bt"}; fi
}
# push_substs queues every command substitution found in the text $1.
push_substs() {
  ps=$1
  while [ -n "$ps" ]; do
    pa=${ps%%"$dp"*}
    pb=${ps%%"$bt"*}
    if [ "$pa" = "$ps" ] && [ "$pb" = "$ps" ]; then break; fi
    if [ "${#pa}" -le "${#pb}" ]; then
      take_paren "${ps#"$pa$dp"}"
    else
      take_bt "${ps#"$pb$bt"}"
    fi
    push "$tp_inner"
    ps=$tp_after
  done
}
# strip_quotes sets sq_out to $1 without quote and backslash characters.
strip_quotes() {
  sq_out=
  sq_in=$1
  while [ -n "$sq_in" ]; do
    sq_c=${sq_in%"${sq_in#?}"}
    sq_in=${sq_in#?}
    case $sq_c in
      "'"|'"'|'\') ;;
      *) sq_out=$sq_out$sq_c ;;
    esac
  done
}
# skip_heredocs drops the bodies of the here-documents opened on the line that
# just ended. A body is text. The body of an unquoted here-document still runs
# its command substitutions, so those are queued.
skip_heredocs() {
  while [ -n "$hpend" ]; do
    hd=${hpend%%"$us"*}
    case $hpend in *"$us"*) hpend=${hpend#*"$us"} ;; *) hpend= ;; esac
    hqf=${hd%%:*}
    hw=${hd#*:}
    while [ -n "$rest" ]; do
      hl=${rest%%"$nl"*}
      if [ "$hl" = "$rest" ]; then rest=; else rest=${rest#*"$nl"}; fi
      hl2=$hl
      while :; do
        case $hl2 in "$tab"*) hl2=${hl2#?} ;; *) break ;; esac
      done
      [ "$hl2" = "$hw" ] && break
      [ "$hqf" = u ] && push_substs "$hl"
    done
  done
}
# run_seg checks one simple command given as words. Text words are per command
# (text_arg); for every other command a word after a flag is a path candidate.
# The script of sh -c, bash -c, zsh -c and eval is queued as a command of its
# own, and the value of an env assignment is checked.
run_seg() {
  while [ $# -gt 0 ]; do
    case $1 in
      *=*)
        n=${1%%=*}
        case $n in ""|[0-9]*|*[!A-Za-z0-9_]*) break ;; esac
        check_word "${1#*=}" 0
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
  shell=0
  want_c=0
  case $name in rg|ag|ack|fd|find|tree|du) sr=1 ;; esac
  case $name in grep|egrep|fgrep|zgrep|rg|ag|ack) grepish=1 ;; esac
  case $name in bash|sh|zsh|dash|ksh|ash) shell=1 ;; esac
  [ "$name" = eval ] && push "$*"
  prev=
  endo=0
  for a; do
    if [ $endo = 0 ]; then
      if [ $shell = 1 ]; then
        if [ $want_c = 1 ]; then
          case $a in -*) ;; *) push "$a"; want_c=0 ;; esac
        else
          case $a in --*) ;; -?*) case $a in *c*) want_c=1 ;; esac ;; esac
        fi
      fi
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
      if text_arg "$p"; then continue; fi
      case $a in
        --) endo=1; continue ;;
        -?*=*)
          if text_opt "${a%%=*}"; then continue; fi
          a=${a#*=} ;;
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
# scan tokenizes the command text $1 (quotes honoured, separators split it into
# simple commands, here-document bodies skipped) and runs run_seg on each simple
# command. Command substitutions are queued, not run here.
scan() {
  dq='"'
  sq="'"
  bs='\'
  rest=$1
  set --
  tok=
  have=0
  hpend=
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
          qa=${rest%%"$dq"*}
          qb=${rest%%"$bs"*}
          qs=${rest%%"$dp"*}
          qk=${rest%%"$bt"*}
          qm=$qa
          qkind=q
          if [ "${#qb}" -lt "${#qm}" ]; then qm=$qb; qkind=b; fi
          if [ "${#qs}" -lt "${#qm}" ]; then qm=$qs; qkind=s; fi
          if [ "${#qk}" -lt "${#qm}" ]; then qm=$qk; qkind=k; fi
          if [ "$qm" = "$rest" ]; then
            tok=$tok$rest
            rest=
            break
          fi
          tok=$tok$qm
          rest=${rest#"$qm"}
          case $qkind in
            q) rest=${rest#?}; break ;;
            b)
              rest=${rest#?}
              e=${rest%"${rest#?}"}
              rest=${rest#?}
              tok=$tok$e ;;
            s)
              take_paren "${rest#??}"
              push "$tp_inner"
              rest=$tp_after ;;
            k)
              take_bt "${rest#?}"
              push "$tp_inner"
              rest=$tp_after ;;
          esac
        done ;;
      "$bs")
        e=${rest%"${rest#?}"}
        rest=${rest#?}
        if [ "$e" != "$nl" ]; then
          tok=$tok$e
          have=1
        fi ;;
      "<")
        etok=1
        case $rest in
          "<<"*) rest=${rest#??} ;;
          "<"*)
            rest=${rest#?}
            case $rest in "-"*) rest=${rest#?} ;; esac
            while :; do
              case $rest in " "*|"$tab"*) rest=${rest#?} ;; *) break ;; esac
            done
            hw=
            while [ -n "$rest" ]; do
              hc=${rest%"${rest#?}"}
              case $hc in " "|"$tab"|"$nl"|";"|"|"|"&"|"("|")"|"<"|">") break ;; esac
              hw=$hw$hc
              rest=${rest#?}
            done
            hqf=u
            case $hw in *"'"*|*'"'*|*'\'*) hqf=q ;; esac
            strip_quotes "$hw"
            hpend=${hpend:+$hpend$us}$hqf:$sq_out ;;
        esac ;;
      " "|"$tab"|">") etok=1 ;;
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
      if [ "$c" = "$nl" ] && [ -n "$hpend" ]; then
        skip_heredocs
      fi
    fi
  done
  if [ $have = 1 ]; then
    set -- "$@" "$tok"
  fi
  run_seg "$@"
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
# The command and every command nested in it (command substitution, a shell -c
# script, eval) run through scan the same way. A nested command keeps the cwd of
# the command that holds it. The cap bounds a pathological nesting.
if [ -n "$cmd" ]; then
  push "$cmd"
  items=0
  while [ -n "$queue" ] && [ "$items" -lt 64 ]; do
    item=${queue%%"$us"*}
    case $queue in *"$us"*) queue=${queue#*"$us"} ;; *) queue= ;; esac
    cwd=${item%%"$rs"*}
    items=$((items+1))
    scan "${item#*"$rs"}"
  done
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
