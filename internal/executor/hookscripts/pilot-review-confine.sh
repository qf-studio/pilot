#!/bin/bash
# Pilot Review Confinement: PreToolUse hook of the post-merge review checkout.
# Usage: pilot-review-confine.sh <worktree-root> <deny-log>
# Reads the PreToolUse JSON from stdin. Exit 0 allows the call; exit 2 denies it
# (stderr is shown to the model). Every denial appends "tool<TAB>path" to the
# deny log, which the daemon turns into one Warn line per denied call.
#
# Fails closed: a missing root, jq, or path resolver denies the call. Read,
# Grep and Glob targets must resolve (symlinks followed) under the root; Bash,
# Edit, Write, WebFetch, WebSearch and every other tool are denied outright.

set -uo pipefail

ROOT_ARG="${1:-}"
DENY_LOG="${2:-}"

deny() {
    local tool="$1" path="$2" reason="$3"
    if [ -n "$DENY_LOG" ]; then
        printf '%s\t%s\n' "$tool" "$(printf '%s' "$path" | tr '\n\r\t' '   ')" >>"$DENY_LOG" 2>/dev/null || true
    fi
    echo "Pilot review confinement: $tool of '$path' denied: $reason" >&2
    exit 2
}

# resolve prints the physical path of $1: symlinks followed, nonexistent tails
# kept lexically. GNU realpath first, python3 where realpath has no -m.
resolve() {
    local out
    if out=$(realpath -m -- "$1" 2>/dev/null) && [ -n "$out" ]; then
        printf '%s' "$out"
        return 0
    fi
    if out=$(python3 -c 'import os,sys;sys.stdout.write(os.path.realpath(sys.argv[1]))' "$1" 2>/dev/null) && [ -n "$out" ]; then
        printf '%s' "$out"
        return 0
    fi
    return 1
}

if [ -z "$ROOT_ARG" ]; then
    deny "unknown" "" "no worktree root configured"
fi
if ! command -v jq >/dev/null 2>&1; then
    deny "unknown" "" "jq is not available"
fi
ROOT=$(resolve "$ROOT_ARG") || deny "unknown" "$ROOT_ARG" "cannot resolve the worktree root"

INPUT=$(cat)
TOOL=$(printf '%s' "$INPUT" | jq -r '.tool_name // empty' 2>/dev/null)
if [ -z "$TOOL" ]; then
    deny "unknown" "" "no tool name in the hook input"
fi

# has_dotdot: a ".." path segment, also inside {a,..} alternatives. Denied
# outright (not normalised): the model has no use for it, and ".." next to a
# symlink resolves differently lexically and physically.
has_dotdot() {
    [[ "$1" =~ (^|[/{,])\.\.([/},]|$) ]]
}

# check_path <tool> <raw> <dir-for-relative>: deny unless raw resolves under
# the root. A relative raw is taken relative to <dir-for-relative>.
check_path() {
    local tool="$1" raw="$2" base="$3" full resolved
    case "$raw" in
        "~"*) deny "$tool" "$raw" "home-relative path" ;;
    esac
    if has_dotdot "$raw"; then
        deny "$tool" "$raw" "path has a .. segment"
    fi
    case "$raw" in
        /*) full="$raw" ;;
        *) full="$base/$raw" ;;
    esac
    resolved=$(resolve "$full") || deny "$tool" "$raw" "cannot resolve the path"
    if [ "$resolved" != "$ROOT" ]; then
        case "$resolved" in
            "$ROOT"/*) ;;
            *) deny "$tool" "$raw" "resolves to $resolved, outside the review worktree" ;;
        esac
    fi
}

# field <jq-path>: a string field of tool_input, empty when absent.
field() {
    printf '%s' "$INPUT" | jq -r ".tool_input.$1 // empty" 2>/dev/null
}

# check_pattern <tool> <pattern> <dir>: the literal directory prefix of a glob,
# up to its first wildcard, must resolve under the root; a relative pattern is
# taken relative to <dir>. "/x" alternatives and "~" are denied.
check_pattern() {
    local tool="$1" pat="$2" dir="$3" prefix target
    [ -z "$pat" ] && return 0
    case "$pat" in
        "~"*) deny "$tool" "$pat" "home-relative pattern" ;;
    esac
    if has_dotdot "$pat"; then
        deny "$tool" "$pat" "pattern has a .. segment"
    fi
    if [[ "$pat" =~ [{,]/ ]]; then
        deny "$tool" "$pat" "pattern has an absolute alternative"
    fi
    prefix="${pat%%[*?\[\{]*}"
    if [ "$prefix" = "$pat" ]; then
        target="$pat"
    elif [[ "$prefix" == */* ]]; then
        target="${prefix%/*}"
        case "$pat" in /*) [ -z "$target" ] && target="/" ;; esac
    else
        target="."
    fi
    check_path "$tool" "$target" "$dir"
}

case "$TOOL" in
    Read)
        FILE=$(field file_path)
        [ -z "$FILE" ] && deny "$TOOL" "" "no file_path"
        check_path "$TOOL" "$FILE" "$ROOT"
        ;;
    Grep)
        DIR=$(field path)
        [ -n "$DIR" ] && check_path "$TOOL" "$DIR" "$ROOT"
        GLOBF=$(field glob)
        if [ -n "$GLOBF" ] && { has_dotdot "$GLOBF" || [[ "$GLOBF" =~ [{,]/ ]]; }; then
            deny "$TOOL" "$GLOBF" "glob filter escapes the search path"
        fi
        ;;
    Glob)
        DIR=$(field path)
        if [ -n "$DIR" ]; then
            check_path "$TOOL" "$DIR" "$ROOT"
            case "$DIR" in /*) BASE_DIR="$DIR" ;; *) BASE_DIR="$ROOT/$DIR" ;; esac
        else
            BASE_DIR="$ROOT"
        fi
        check_pattern "$TOOL" "$(field pattern)" "$BASE_DIR"
        ;;
    *)
        deny "$TOOL" "" "tool not permitted in a review checkout"
        ;;
esac
exit 0
