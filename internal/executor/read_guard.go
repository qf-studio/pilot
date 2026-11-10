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
// GH-6267: a Bash command is tokenized and each path-looking token is resolved
// through the same resolve() as a Read path, instead of matching substrings, so
// "grep -rn ~/.pilot docs/" or a commit message that mentions the path is not a
// read. The tokenizer is one awk pass (GH-6287: a shell loop per character took
// 15 s on a 10 KB command; the awk pass is linear), run from the cmd_words
// function: it honours quotes and escapes, splits separators into simple
// commands, expands a leading ~ and $HOME / ${HOME}, tracks cd (no argument is
// the user home; a subshell restores the directory), skips here-document bodies
// and takes redirect targets as paths. Wrappers and keywords before the command
// word (if, then, do, !, time, timeout N, sudo [-u x], env, nice, nohup, xargs)
// do not hide it. Text words are per command: the word after -m or --message of
// git commit, the patterns and filter options of grep, rg and git grep, the
// --grep, -S and -G of git log; the value of a grep or rg option is never the
// pattern; for every other command a word after a flag is a path (ls -t, cat
// -A). Nested commands are commands: the inside of a command substitution (also
// in double quotes, also a backtick form), the script of bash/sh/zsh -c and of
// eval are queued and scanned by the same rules; the value of an env assignment
// or of name=@path is checked; here-document bodies are text. A glob is matched
// one path segment per star. A recursive read (Grep/Glob path, grep -r, rg,
// find, ls -R, tree, du) of a parent of the Pilot home is denied too, except
// /tmp and the project dir; the parent is compared as configured, resolved, and
// as the resolved parent plus the base name (a symlinked parent of the home).
//
// Decision: text another program reads cannot be followed, so those shapes fall
// back to the old substring rule (GH-6287): an interpreter with inline code
// (python3 -c, node -e, perl -e, ruby -e, awk programs), a heredoc or a pipe
// into a shell or an interpreter, a command that runs another command from text
// (watch, ssh, su, docker, find -exec, ...), a brace expansion, a command that
// needs more than 64 distinct paths or 256 nested items. When the shape is
// present AND the raw command text names the Pilot home, the config path or a
// process environment (tilde, HOME and absolute spellings), the call is denied.
// A word that still holds a variable or a command substitution after the HOME
// expansion and names the home directory (P=$HOME; cat $P/.pilot/x) is denied.
// /proc/<anything>/environ is denied for a glob or variable in the middle.
// Without awk the whole command falls back to the substring rule; without
// realpath the guard denies every call with one line naming the tool. Only
// unparseable hook JSON fails closed; a token that cannot be resolved is
// allowed.
const readGuardScript = `#!/bin/sh
home=$1
cfg=$2
nl='
'
us=$(printf '\037')
deny() { printf 'read blocked: %s (rule read-guard: the Pilot home, its config and process environments are off limits to agent sessions)\n' "$1" >&2; exit 2; }
input=$(cat)
command -v realpath >/dev/null 2>&1 || deny "realpath is not installed, so the guard cannot resolve paths"
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
# resolve sets res to the physical path of $1: the nearest existing ancestor
# goes through realpath, the missing rest is appended. A "." or ".." in the
# missing part is applied lexically (the missing directory cannot be a
# symlink). It fails when realpath does. It sets a variable instead of printing
# so a call costs no subshell.
resolve() {
  rp=$1
  rt=
  rdd=0
  while [ "$rp" != / ]; do
    rp=${rp%/}
    [ -n "$rp" ] || break
    if [ -e "$rp" ] || [ -L "$rp" ]; then break; fi
    rb=${rp##*/}
    case $rb in
      .) ;;
      ..) rdd=$((rdd+1)) ;;
      *) if [ "$rdd" -gt 0 ]; then rdd=$((rdd-1)); else rt=$rb${rt:+/$rt}; fi ;;
    esac
    rp=${rp%/*}
    [ -n "$rp" ] || rp=/
  done
  rr=$(realpath -- "$rp" 2>/dev/null) || return 1
  while [ "$rdd" -gt 0 ]; do
    rr=${rr%/*}
    [ -n "$rr" ] || rr=/
    rdd=$((rdd-1))
  done
  case $rr in
    /) res=/$rt ;;
    *) res=$rr${rt:+/$rt} ;;
  esac
}
real_home=$home
resolve "$home" && real_home=$res
real_cfg=$cfg
resolve "$cfg" && real_cfg=$res
real_pwd=$PWD
resolve "$PWD" && real_pwd=$res
real_tmp=/tmp
resolve /tmp && real_tmp=$res
# alt_home: the resolved parent of the Pilot home plus its base name. It differs
# from real_home when the home itself is a symlink, and it is what a parent
# directory resolves against when the parent path runs through a symlink (macOS
# /var, /tmp).
home_parent=${home%/*}
[ -n "$home_parent" ] || home_parent=/
real_parent=$home_parent
resolve "$home_parent" && real_parent=$res
case $real_parent in
  /) alt_home=/${home##*/} ;;
  *) alt_home=$real_parent/${home##*/} ;;
esac
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
# is_ancestor: $1 is / or a parent of the Pilot home, as configured, resolved or
# resolved through its parent (the home may itself be a symlink).
is_ancestor() {
  [ "$1" = / ] && return 0
  case $real_home in
    "$1"/*) return 0 ;;
  esac
  case $home in
    "$1"/*) return 0 ;;
  esac
  case $alt_home in
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
# path that cannot be resolved is allowed.
check_target() {
  t=$1
  case $t in
    *[*?[]*)
      if glob_reach "$t" "$real_home" || glob_reach "$t" "$home" || glob_reach "$t" "$real_cfg"; then
        deny "$t reaches the Pilot home"
      fi
      for pe in /proc/self/environ /proc/thread-self/environ /proc/1/environ /proc/12/environ /proc/123/environ /proc/1234/environ; do
        if glob_reach "$t" "$pe"; then deny "$t reaches a process environment"; fi
      done
      return 0 ;;
  esac
  resolve "$t" || return 0
  r=$res
  case $r in
    "$real_home"|"$real_home"/*|"$real_cfg") deny "$t is inside the Pilot home" ;;
    /proc/self/environ|/proc/thread-self/environ|/proc/[0-9]*/environ|/proc/[0-9]*/task/[0-9]*/environ) deny "$t is a process environment" ;;
  esac
  if [ "$2" = 1 ] && is_ancestor "$r" && [ "$r" != /tmp ] && [ "$r" != "$real_tmp" ] && ! in_project "$r"; then
    deny "$t is a parent of the Pilot home and is read recursively"
  fi
  return 0
}
# mentions succeeds when the text $1 names the Pilot home, the config file or a
# process environment in any spelling: the old substring rule. It is the fallback
# for text the tokenizer cannot follow.
mentions() {
  case $1 in
    *"$home"*|*"$real_home"*|*"$cfg"*|*"$real_cfg"*) return 0 ;;
    *.pilot/config*|*"~/.pilot"*|*'$HOME/.pilot'*|*'${HOME}/.pilot'*) return 0 ;;
    */proc/*environ*) return 0 ;;
  esac
  return 1
}
# cmd_words is the tokenizer: one awk pass over the command text, linear in its
# length. It splits the text into simple commands (quotes, escapes, command
# substitutions, here-documents, subshells and cd tracked) and prints one line
# per fact: "T<US>rec<US>path" for a path the command names (rec is 1 for a
# recursive read) and "D<US>reason" for a denial. The shell resolves the paths.
cmd_words() {
  cat <<'AWK_PROGRAM'
BEGIN {
  HOME_DIR = ENVIRON["PG_USERHOME"]
  PHOME = ENVIRON["PG_HOME"]
  RHOME = ENVIRON["PG_REALHOME"]
  PCFG = ENVIRON["PG_CFG"]
  RCFG = ENVIRON["PG_REALCFG"]
  CWD0 = ENVIRON["PG_PWD"]
  if (CWD0 == "") CWD0 = "/"
  HB = PHOME
  sub(/^.*\//, "", HB)
  BT = "\140"
  NL = "\n"
  US = "\037"
  CHUNK = 1024
  MAXQ = 256
  MAXC = 64
  BUDGET = 1000000
  mkset(KW, "if then else elif fi do done while until ! { }")
  mkset(WRAP, "sudo doas env time timeout nice nohup xargs exec command builtin")
  mkset(SHELLS, "bash sh zsh dash ksh ash")
  mkset(GREPSET, "grep egrep fgrep zgrep rg ag ack")
  mkset(SRSET, "rg ag ack fd find tree du")
  mkset(INTERP, "node nodejs deno bun perl ruby php lua luajit Rscript osascript awk gawk mawk nawk")
  mkset(NEEDARGS0, "cd pushd eval git find ls bash sh zsh dash ksh ash rg ag ack fd tree du grep egrep fgrep zgrep node nodejs deno bun perl ruby php lua luajit Rscript osascript awk gawk mawk nawk")
  mkset(EXECLIKE, "watch ssh su script parallel at batch screen tmux docker podman kubectl nsenter chroot flock setsid stdbuf unshare runuser systemd-run sg newgrp sshpass mosh rsh ionice chrt taskset strace ltrace expect busybox")
}
function mkset(arr, str,   m, a, i) {
  m = split(str, a, " ")
  for (i = 1; i <= m; i++) arr[a[i]] = 1
}
{
  chunk1 = chunk1 $0 NL
  if (length(chunk1) > 8192) {
    all = all chunk1
    chunk1 = ""
  }
}
END {
  all = all chunk1
  qn = 0
  qi = 0
  push_item(CWD0, all)
  while (qi < qn) {
    qi++
    if (qi > MAXQ || spent > BUDGET) {
      undec = 1
      break
    }
    spent += length(QT[qi])
    scan(QT[qi], QC[qi])
  }
  if ((undec || capped) && mentions(all)) deny_now("the command has text the guard cannot follow and mentions the Pilot home, its config or a process environment")
  exit 0
}
function deny_now(msg) {
  print "D" US msg
  exit 0
}
function mentions(t,   p) {
  if (PHOME != "" && index(t, PHOME) > 0) return 1
  if (RHOME != "" && index(t, RHOME) > 0) return 1
  if (PCFG != "" && index(t, PCFG) > 0) return 1
  if (RCFG != "" && index(t, RCFG) > 0) return 1
  if (index(t, ".pilot/config") > 0 || index(t, "~/.pilot") > 0 || index(t, "$HOME/.pilot") > 0 || index(t, "${HOME}/.pilot") > 0) return 1
  p = index(t, "/proc/")
  if (p > 0 && index(substr(t, p), "environ") > 0) return 1
  return 0
}
function push_item(c, t) {
  qn++
  QC[qn] = c
  QT[qn] = t
}
function nextidx(c, from,   p, k, i) {
  p = from
  while (p <= n) {
    k = substr(s, p, CHUNK)
    i = index(k, c)
    if (i > 0) return p + i - 1
    p += CHUNK
  }
  return 0
}
function norm(p,   a, m, i, top, st, out) {
  m = split(p, a, "/")
  top = 0
  for (i = 1; i <= m; i++) {
    if (a[i] == "" || a[i] == ".") continue
    if (a[i] == "..") {
      if (top > 0) top--
      continue
    }
    top++
    st[top] = a[i]
  }
  out = ""
  for (i = 1; i <= top; i++) out = out "/" st[i]
  if (out == "") out = "/"
  return out
}
function lex_inside(v,   w) {
  w = norm(v)
  if (PHOME != "" && (w == PHOME || index(w, PHOME "/") == 1)) return 1
  if (RHOME != "" && (w == RHOME || index(w, RHOME "/") == 1)) return 1
  if (PCFG != "" && w == PCFG) return 1
  if (RCFG != "" && w == RCFG) return 1
  return 0
}
function emit(path, rec,   key) {
  key = rec US path
  if (key in SEEN) return
  if (substr(path, 1, 1) == "/" && path !~ /[*?[]/ && lex_inside(path)) deny_now(path " is inside the Pilot home")
  if (nc >= MAXC) {
    capped = 1
    return
  }
  SEEN[key] = 1
  nc++
  print "T" US rec US path
}
function flush_tok() {
  if (!have) return
  if (rd == 2) {
    hn++
    HD[hn] = tok
    HQ[hn] = tq
    rd = 0
  } else if (rd == 3) {
    rd = 0
  } else if (rd == 1) {
    rd = 0
    chk_redir(tok, tdyn)
  } else {
    nw++
    W[nw] = tok
    WD[nw] = tdyn
  }
  tok = ""
  have = 0
  tdyn = 0
  tq = 0
}
function end_seg(p) {
  if (nw == 0) {
    if (p) pipe_in = 1
    return
  }
  run_seg()
  nw = 0
  rd = 0
  seg_hd = 0
  pipe_in = p
}
function take_paren(   d, k, i, ch) {
  d = 1
  tp_in = ""
  while (pos <= n) {
    k = substr(s, pos, CHUNK)
    i = match(k, /[()]/)
    if (i == 0) {
      tp_in = tp_in k
      pos += length(k)
      continue
    }
    ch = substr(k, i, 1)
    if (ch == "(") {
      d++
    } else {
      d--
      if (d == 0) {
        tp_in = tp_in substr(k, 1, i - 1)
        pos += i
        return
      }
    }
    tp_in = tp_in substr(k, 1, i)
    pos += i
  }
}
function take_bt(   i) {
  i = nextidx(BT, pos)
  if (i == 0) {
    tp_in = substr(s, pos)
    pos = n + 1
  } else {
    tp_in = substr(s, pos, i - pos)
    pos = i + 1
  }
}
function push_substs(line,   ss, sp, sn, t, a, b) {
  ss = s
  sp = pos
  sn = n
  s = line
  n = length(s)
  pos = 1
  while (pos <= n) {
    t = substr(s, pos)
    a = index(t, "$(")
    b = index(t, BT)
    if (a == 0 && b == 0) break
    if (b == 0 || (a != 0 && a < b)) {
      pos += a + 1
      take_paren()
    } else {
      pos += b
      take_bt()
    }
    push_item(cwd, tp_in)
  }
  s = ss
  pos = sp
  n = sn
}
function skip_heredocs(   j, e, line, l2) {
  for (j = 1; j <= hn; j++) {
    while (pos <= n) {
      e = nextidx(NL, pos)
      if (e == 0) {
        line = substr(s, pos)
        pos = n + 1
      } else {
        line = substr(s, pos, e - pos)
        pos = e + 1
      }
      l2 = line
      sub(/^\t+/, "", l2)
      if (l2 == HD[j]) break
      if (HQ[j] == 0) push_substs(line)
    }
  }
  hn = 0
}
function dollar(   nx, i, nm, k) {
  have = 1
  nx = substr(s, pos + 1, 1)
  if (nx == "'" || nx == "\"") {
    pos++
    return
  }
  if (nx == "(") {
    pos += 2
    take_paren()
    push_item(cwd, tp_in)
    tok = tok "$(" tp_in ")"
    tdyn = 1
    return
  }
  if (nx == "{") {
    i = nextidx("}", pos + 2)
    if (i == 0) {
      nm = substr(s, pos + 2)
      pos = n + 1
    } else {
      nm = substr(s, pos + 2, i - pos - 2)
      pos = i + 1
    }
    if (nm == "HOME" && HOME_DIR != "") {
      tok = tok HOME_DIR
    } else {
      tok = tok "${" nm "}"
      tdyn = 1
    }
    return
  }
  k = substr(s, pos + 1, 256)
  if (match(k, /^[A-Za-z_][A-Za-z0-9_]*/)) {
    nm = substr(k, 1, RLENGTH)
    pos += 1 + RLENGTH
    if (nm == "HOME" && HOME_DIR != "") {
      tok = tok HOME_DIR
    } else {
      tok = tok "$" nm
      tdyn = 1
    }
    return
  }
  if (nx != "" && index("@*#?!$-0123456789", nx) > 0) {
    tok = tok "$" nx
    pos += 2
    tdyn = 1
    return
  }
  tok = tok "$"
  pos++
}
function dquote(   k, i, ch) {
  while (pos <= n) {
    k = substr(s, pos, CHUNK)
    i = match(k, /["\\$\140]/)
    if (i == 0) {
      tok = tok k
      pos += length(k)
      continue
    }
    tok = tok substr(k, 1, i - 1)
    pos += i - 1
    ch = substr(s, pos, 1)
    if (ch == "\"") {
      pos++
      return
    }
    if (ch == "\\") {
      pos++
      tok = tok substr(s, pos, 1)
      pos++
      continue
    }
    if (ch == BT) {
      pos++
      take_bt()
      push_item(cwd, tp_in)
      tok = tok BT tp_in BT
      tdyn = 1
      continue
    }
    dollar()
  }
}
function redir(c,   nx, nx2) {
  if (have && tq == 0 && tok ~ /^[0-9]+$/) {
    tok = ""
    have = 0
    tdyn = 0
  }
  flush_tok()
  nx = substr(s, pos + 1, 1)
  nx2 = substr(s, pos + 2, 1)
  if (c == "<") {
    if (nx == "<") {
      if (nx2 == "<") {
        pos += 3
        rd = 3
        return
      }
      pos += 2
      if (nx2 == "-") pos++
      rd = 2
      seg_hd = 1
      return
    }
    if (nx == "(") {
      pos++
      return
    }
    pos++
    if (nx == "&") pos++
    rd = 1
    return
  }
  pos++
  if (nx == ">" || nx == "|" || nx == "&") pos++
  if (substr(s, pos, 1) == "(") return
  rd = 1
}
function scan(text, cwd0,   c, k, i, e) {
  s = text
  n = length(s)
  pos = 1
  cwd = cwd0
  nw = 0
  tok = ""
  have = 0
  tdyn = 0
  tq = 0
  rd = 0
  hn = 0
  pipe_in = 0
  seg_hd = 0
  sd = 0
  while (pos <= n) {
    c = substr(s, pos, 1)
    if (c == "#" && !have) {
      i = nextidx(NL, pos)
      if (i == 0) pos = n + 1
      else pos = i
      continue
    }
    if (index(" \t\n;&|()<>$\"'\\\140", c) == 0) {
      k = substr(s, pos, CHUNK)
      match(k, /^[^ \t\n;&|()<>$"'\\\140]+/)
      tok = tok substr(k, 1, RLENGTH)
      pos += RLENGTH
      have = 1
      continue
    }
    if (c == "'") {
      have = 1
      tq = 1
      pos++
      i = nextidx("'", pos)
      if (i == 0) {
        tok = tok substr(s, pos)
        pos = n + 1
      } else {
        tok = tok substr(s, pos, i - pos)
        pos = i + 1
      }
    } else if (c == "\"") {
      have = 1
      tq = 1
      pos++
      dquote()
    } else if (c == "\\") {
      pos++
      e = substr(s, pos, 1)
      pos++
      tq = 1
      if (e != NL && e != "") {
        tok = tok e
        have = 1
      }
    } else if (c == "$") {
      dollar()
    } else if (c == BT) {
      pos++
      take_bt()
      push_item(cwd, tp_in)
      tok = tok BT tp_in BT
      tdyn = 1
      have = 1
    } else if (c == " " || c == "\t") {
      pos++
      flush_tok()
    } else if (c == NL) {
      pos++
      flush_tok()
      end_seg(0)
      if (hn > 0) skip_heredocs()
    } else if (c == ";") {
      pos++
      flush_tok()
      end_seg(0)
    } else if (c == "&") {
      if (substr(s, pos + 1, 1) == ">") {
        pos++
        redir(">")
      } else {
        if (substr(s, pos + 1, 1) == "&") pos++
        pos++
        flush_tok()
        end_seg(0)
      }
    } else if (c == "|") {
      flush_tok()
      e = substr(s, pos + 1, 1)
      if (e == "|") {
        pos += 2
        end_seg(0)
      } else {
        if (e == "&") pos++
        pos++
        end_seg(1)
      }
    } else if (c == "(") {
      pos++
      flush_tok()
      end_seg(0)
      sd++
      SDW[sd] = cwd
    } else if (c == ")") {
      pos++
      flush_tok()
      end_seg(0)
      if (sd > 0) {
        cwd = SDW[sd]
        sd--
      }
    } else {
      redir(c)
    }
  }
  flush_tok()
  end_seg(0)
}
function chk_redir(w, dyn) {
  if (w ~ /^[0-9-]*$/) return
  if (dyn && HB != "" && index(w, HB) > 0) deny_now("a redirect target with a variable or a command substitution names the Pilot home")
  cand_word(w, dyn, 0, 1)
}
function chk_word(j, rec, force) {
  if (WD[j] && HB != "" && index(W[j], HB) > 0) deny_now("a word with a variable or a command substitution names the Pilot home")
  return cand_word(W[j], WD[j], rec, force)
}
function cand_word(w, dyn, rec, force,   v, c1, like) {
  wlike = 0
  v = w
  if (match(w, /^(--?[A-Za-z0-9_][A-Za-z0-9_-]*|[A-Za-z_][A-Za-z0-9_]*)=/)) {
    v = substr(w, RLENGTH + 1)
  }
  sub(/^@/, "", v)
  if (v == "" || index(v, NL) > 0) return ""
  if (HOME_DIR != "") {
    if (v == "$HOME" || index(v, "$HOME/") == 1) v = HOME_DIR substr(v, 6)
    else if (v == "${HOME}" || index(v, "${HOME}/") == 1) v = HOME_DIR substr(v, 8)
  }
  if (v ~ /\{[^}]*(,|\.\.)[^}]*\}/) {
    if (HB != "" && index(v, HB) > 0) deny_now("a brace expansion names the Pilot home")
    undec = 1
  }
  c1 = substr(v, 1, 1)
  if (c1 == "~") {
    if (v == "~" || substr(v, 2, 1) == "/") {
      if (HOME_DIR == "") return ""
      v = HOME_DIR substr(v, 2)
    }
    like = 1
  } else {
    like = force || index(v, "/") > 0 || c1 == "." || v ~ /[*?[]/ || v == "environ"
  }
  if (!like) return ""
  wlike = 1
  if (dyn && (index(v, "$") > 0 || index(v, BT) > 0)) {
    if (v ~ /^\/proc\/[^\/]+\/(task\/[^\/]+\/)?environ/) deny_now("a process environment is off limits")
    return ""
  }
  if (c1 != "~" && c1 != "/") {
    if (cwd == "/") v = "/" v
    else v = cwd "/" v
  }
  if (v ~ /^\/proc\/[^\/]+\/(task\/[^\/]+\/)?environ/) deny_now("a process environment is off limits")
  emit(v, rec)
  return v
}
function is_interp(nm) {
  return (nm ~ /^python[0-9.]*$/ || (nm in INTERP))
}
function inline_flag(nm, a,   cl) {
  if (nm ~ /^(awk|gawk|mawk|nawk)$/) return 1
  if (substr(a, 1, 1) != "-" || length(a) < 2) return (nm == "deno" && a == "eval")
  if (substr(a, 1, 2) == "--") return (a == "--eval" || a == "--print" || a == "--command" || index(a, "--eval=") == 1)
  cl = substr(a, 2)
  if (nm ~ /^python[0-9.]*$/) return (index(cl, "c") > 0)
  if (nm == "perl") return (index(cl, "e") > 0 || index(cl, "E") > 0)
  if (nm == "ruby" || nm == "lua" || nm == "luajit" || nm == "Rscript" || nm == "osascript") return (index(cl, "e") > 0)
  if (nm == "node" || nm == "nodejs" || nm == "bun" || nm == "deno") return (index(cl, "e") > 0 || index(cl, "p") > 0)
  if (nm == "php") return (index(cl, "r") > 0)
  return 0
}
function skip_wrap(i,   nm, vo, a, j) {
  nm = W[i]
  sub(/^.*\//, "", nm)
  j = i + 1
  vo = ""
  if (nm == "sudo" || nm == "doas") vo = " -u -g -C -D -h -p -r -t -U -T --user --group --chdir --host --prompt --role --type "
  else if (nm == "env") vo = " -u -C -S --unset --chdir "
  else if (nm == "nice") vo = " -n --adjustment "
  else if (nm == "timeout") vo = " -k -s --kill-after --signal "
  else if (nm == "xargs") vo = " -I -n -P -L -d -E -s -a -J --max-args --max-procs --max-lines --delimiter --arg-file "
  while (j <= nw) {
    a = W[j]
    if (a == "--") {
      j++
      break
    }
    if (substr(a, 1, 1) != "-" || length(a) < 2) break
    if (index(vo, " " a " ") > 0) {
      j++
      if (j <= nw) chk_word(j, 0, 0)
    }
    j++
  }
  if (nm == "timeout" && j <= nw) j++
  return j
}
function vchars(nm, gs) {
  if (nm == "grep" || nm == "egrep" || nm == "fgrep" || nm == "zgrep") return "mdDABCef"
  if (nm == "rg" || nm == "ag" || nm == "ack") return "mABCefgtTjMrEd"
  if (nm == "git") {
    if (gs == "grep") return "mdABCef"
    if (gs == "commit") return "mFcC"
    if (gs == "tag") return "mFu"
    if (gs == "merge" || gs == "notes") return "mF"
    if (gs == "stash") return "m"
    if (gs == "log" || gs == "shortlog" || gs == "rev-list" || gs == "show") return "SGn"
  }
  return ""
}
function lvtype(nm, gs, opt,   t) {
  t = " " opt " "
  if (nm == "grep" || nm == "egrep" || nm == "fgrep" || nm == "zgrep" || nm == "rg" || nm == "ag" || nm == "ack" || (nm == "git" && gs == "grep")) {
    if (index(" --file --exclude-from --ignore-file --pre ", t) > 0) return 2
    if (index(" --regexp --after-context --before-context --context --max-count --directories --devices --include --exclude --exclude-dir --glob --type --type-not --label --max-columns --threads --max-depth --maxdepth --replace --encoding --iglob --sort --sortr --type-add --max-filesize ", t) > 0) return 1
    return 0
  }
  if (nm == "git") {
    if (gs == "commit" || gs == "tag" || gs == "merge" || gs == "notes" || gs == "stash") {
      if (opt == "--file") return 2
      if (index(" --message --author --date --reuse-message --reedit-message --trailer ", t) > 0) return 1
    }
    if (gs == "log" || gs == "shortlog" || gs == "rev-list" || gs == "show") {
      if (index(" --grep --author --committer --since --until --after --before --max-count --skip --format --pretty --date ", t) > 0) return 1
    }
  }
  return 0
}
function clustervalue(nm, gs, a,   vc, i, ch) {
  cl_ch = ""
  cl_attached = 0
  vc = vchars(nm, gs)
  if (vc == "") return
  for (i = 2; i <= length(a); i++) {
    ch = substr(a, i, 1)
    if (index(vc, ch) > 0) {
      cl_ch = ch
      cl_attached = (i < length(a))
      return
    }
  }
}
function run_seg(   i, j, w, a, nm, sr, grepish, gitgrep, shl, interp, iscd, gsubc, gi, haspat, wantc, inl, endo, p, kind, needpat, npaths, cdone, cdash, r, scj, opt) {
  i = 1
  while (i <= nw) {
    w = W[i]
    if (w in KW) {
      i++
      continue
    }
    if (w ~ /^[A-Za-z_][A-Za-z0-9_]*=/) {
      chk_word(i, 0, 0)
      i++
      continue
    }
    if (w in WRAP) {
      i = skip_wrap(i)
      continue
    }
    break
  }
  if (i > nw) return
  chk_word(i, 0, 0)
  nm = W[i]
  sub(/^.*\//, "", nm)
  if (i == nw && !(nm in NEEDARGS0) && !(nm in EXECLIKE) && nm !~ /^python/) return
  sr = (nm in SRSET)
  grepish = (nm in GREPSET)
  shl = (nm in SHELLS)
  interp = is_interp(nm)
  iscd = (nm == "cd" || nm == "pushd")
  gsubc = ""
  gi = 0
  if (nm == "git") {
    for (j = i + 1; j <= nw; j++) {
      a = W[j]
      if (a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace") {
        j++
        continue
      }
      if (substr(a, 1, 1) == "-") continue
      gsubc = a
      gi = j
      break
    }
  }
  gitgrep = (nm == "git" && gsubc == "grep")
  if (nm in EXECLIKE) undec = 1
  haspat = 0
  wantc = 0
  inl = 0
  endo = 0
  scj = 0
  if (nm == "eval") {
    a = ""
    for (j = i + 1; j <= nw; j++) a = a (j > i + 1 ? " " : "") W[j]
    push_item(cwd, a)
  }
  for (j = i + 1; j <= nw; j++) {
    a = W[j]
    if (endo) continue
    if (a == "--") {
      endo = 1
      continue
    }
    if (shl) {
      if (wantc) {
        if (substr(a, 1, 1) != "-") {
          push_item(cwd, a)
          scj = j
          wantc = 0
        }
        continue
      }
      if (a ~ /^-[^-]/ && index(a, "c") > 0) wantc = 1
    }
    if (interp && inline_flag(nm, a)) inl = 1
    if (nm == "find" && (a == "-exec" || a == "-execdir" || a == "-ok" || a == "-okdir")) undec = 1
    if (a == "--recursive" || a == "--dereference-recursive" || a == "--directories=recurse") {
      sr = 1
    } else if (a == "--regexp" || index(a, "--regexp=") == 1 || a == "--file" || index(a, "--file=") == 1) {
      haspat = 1
    } else if (nm == "rg" && (a == "--files" || a == "--type-list")) {
      haspat = 1
    } else if (a ~ /^-[^-]/) {
      if ((nm == "grep" || nm == "egrep" || nm == "fgrep" || nm == "zgrep") && a ~ /[rR]/) sr = 1
      if (nm == "ls" && a ~ /R/) sr = 1
      if (grepish || gitgrep) {
        clustervalue(nm, gsubc, a)
        if (cl_ch == "e" || cl_ch == "f") haspat = 1
      }
    } else if (a == "recurse" && W[j - 1] == "-d") {
      sr = 1
    }
  }
  if ((shl || interp) && (pipe_in || seg_hd)) undec = 1
  if (interp && inl) undec = 1
  needpat = ((grepish || gitgrep) && !haspat)
  endo = 0
  npaths = 0
  cdone = 0
  cdash = 0
  j = i + 1
  while (j <= nw) {
    a = W[j]
    if (nm == "git" && j <= gi) {
      if (a == "-C" || a == "--git-dir" || a == "--work-tree") {
        j++
        if (j <= nw) chk_word(j, 0, 0)
        j++
        continue
      }
      if (a == "-c" || a == "--namespace") {
        j += 2
        continue
      }
      if (j == gi) {
        j++
        continue
      }
    }
    if (shl && j == scj) {
      j++
      continue
    }
    if (!endo) {
      if (a == "--") {
        endo = 1
        j++
        continue
      }
      if (length(a) > 1 && substr(a, 1, 1) == "-") {
        p = index(a, "=")
        if (p > 0) {
          kind = 0
          if (substr(a, 1, 2) == "--") kind = lvtype(nm, gsubc, substr(a, 1, p - 1))
          if (kind != 1) chk_word(j, 0, 0)
          j++
          continue
        }
        if (substr(a, 2, 1) == "-") {
          kind = lvtype(nm, gsubc, a)
          j++
          if (kind > 0) {
            if (kind == 2 && j <= nw) chk_word(j, 0, 0)
            j++
          }
          continue
        }
        clustervalue(nm, gsubc, a)
        j++
        if (cl_ch != "" && !cl_attached) {
          if (cl_ch == "F" || (cl_ch == "f" && (grepish || gitgrep))) {
            if (j <= nw) chk_word(j, 0, 0)
          }
          j++
        }
        continue
      }
    }
    if (needpat) {
      needpat = 0
      j++
      continue
    }
    if (iscd) {
      if (a == "-") {
        cdash = 1
        j++
        continue
      }
      if (cdone == 0) {
        cdone = 1
        r = chk_word(j, 0, 1)
        if (r != "" && substr(r, 1, 1) == "/" && r !~ /[*?[]/) cwd = norm(r)
        else if (r == "" && WD[j] && substr(W[j], 1, 1) == "/") cwd = norm(W[j])
        j++
        continue
      }
    }
    chk_word(j, sr, 0)
    if (wlike) npaths++
    j++
  }
  if (iscd && cdone == 0 && cdash == 0 && HOME_DIR != "") cwd = HOME_DIR
  if (sr && npaths == 0) emit(cwd, 1)
}
AWK_PROGRAM
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
# The command runs through the tokenizer. Without awk, or when awk fails, the
# old substring rule applies: a command that names the Pilot home is denied.
if [ -n "$cmd" ]; then
  out=
  if command -v awk >/dev/null 2>&1; then
    prog=$(cmd_words)
    out=$(printf '%s' "$cmd" | PG_USERHOME=$HOME PG_HOME=$home PG_REALHOME=$real_home PG_CFG=$cfg PG_REALCFG=$real_cfg PG_PWD=$PWD LC_ALL=C awk "$prog" 2>/dev/null) || out=E
  else
    out=E
  fi
  if [ "$out" = E ]; then
    mentions "$cmd" && deny "the command touches the Pilot home or its config"
  else
    while IFS=$us read -r kind rec target; do
      case $kind in
        D) deny "$rec" ;;
        T)
          expand "$target"
          check_target "$ex" "$rec" ;;
      esac
    done <<EOF_OUT
$out
EOF_OUT
  fi
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
