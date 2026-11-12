#!/bin/bash
# Mock host commands for setup-agent-user_test.sh. One script, linked into the
# test's stub bin under the name of each command it stands for (useradd,
# groupadd, usermod, visudo, iptables, ip6tables, systemctl, install, chown,
# chmod, id, getent, stat); the command is the basename of $0.
#
# Every call is appended to $MOCK_CALL_LOG. A call that would change a real
# host also goes to $MOCK_MUT_LOG, so a dry run can be shown to change nothing.
# The "host" is a state directory ($MOCK_STATE_DIR): users/, groups/,
# members/<group>, meta/ (owner:group per path), enabled/, <family>.rules.
#
# Knobs (env): MOCK_UID (answer of `id -u`, default 0), MOCK_VISUDO_FAIL=1
# (`visudo -cf` fails), MOCK_VISUDO_C_FAIL=1 (`visudo -c` fails), MOCK_REAL_PATH
# (PATH for the real tools the mocks delegate to).

set -euo pipefail

cmd="$(basename "$0")"
state="${MOCK_STATE_DIR:?}"
mkdir -p "$state"/{users,groups,members,meta,enabled}

real() { PATH="${MOCK_REAL_PATH:?}" "$@"; }

echo "${cmd} $*" >>"${MOCK_CALL_LOG:?}"
mutating() { echo "${cmd} $*" >>"${MOCK_MUT_LOG:?}"; }

# Path -> file name for the owner:group record.
meta_file() { echo "${state}/meta/$(echo "$1" | tr '/' '_')"; }

# Rules are kept as lines: "chain NAME" and "rule CHAIN ARGS".
fw() {
    local rules="${state}/${cmd}.rules" op chain
    local args=()
    for a in "$@"; do
        case "$a" in -w | -n) ;; *) args+=("$a") ;; esac
    done
    op="${args[0]}"
    chain="${args[1]:-}"
    touch "$rules"
    case "$op" in
        -L | -S) grep -qFx "chain ${chain}" "$rules" ;;
        -C) grep -qFx "rule ${args[*]:1}" "$rules" ;;
        -N)
            mutating "$@"
            grep -qFx "chain ${chain}" "$rules" && exit 1
            echo "chain ${chain}" >>"$rules"
            ;;
        -A | -I)
            mutating "$@"
            echo "rule ${args[*]:1}" >>"$rules"
            ;;
        -D)
            mutating "$@"
            grep -qFx "rule ${args[*]:1}" "$rules" || exit 1
            grep -vFx "rule ${args[*]:1}" "$rules" >"${rules}.new" || true
            mv "${rules}.new" "$rules"
            ;;
        -F)
            mutating "$@"
            grep -v "^rule ${chain} " "$rules" >"${rules}.new" || true
            mv "${rules}.new" "$rules"
            ;;
        -X)
            mutating "$@"
            grep -vFx "chain ${chain}" "$rules" >"${rules}.new" || true
            mv "${rules}.new" "$rules"
            ;;
        *) exit 2 ;;
    esac
}

case "$cmd" in
    id)
        case "${1:-}" in
            -u)
                if [ -z "${2:-}" ]; then
                    echo "${MOCK_UID:-0}"
                else
                    [ -e "${state}/users/$2" ] || exit 1
                    echo 1000
                fi
                ;;
            -nG)
                [ -e "${state}/users/$2" ] || exit 1
                out=""
                for g in "$state"/members/*; do
                    [ -e "$g" ] || continue
                    if grep -qx "$2" "$g"; then out="${out} $(basename "$g")"; fi
                done
                echo "${out# }"
                ;;
            *) exit 2 ;;
        esac
        ;;
    getent)
        [ "$1" = group ] && [ -e "${state}/groups/$2" ] || exit 2
        echo "$2:x:998:"
        ;;
    groupadd)
        mutating "$@"
        touch "${state}/groups/${!#}"
        ;;
    useradd)
        mutating "$@"
        touch "${state}/users/${!#}"
        ;;
    usermod)
        mutating "$@"
        # usermod -aG GROUP USER
        echo "$3" >>"${state}/members/$2"
        ;;
    visudo)
        case "${1:-}" in
            -cf) [ "${MOCK_VISUDO_FAIL:-0}" != 1 ] || { echo "visudo: parse error" >&2; exit 1; } ;;
            -c) [ "${MOCK_VISUDO_C_FAIL:-0}" != 1 ] || { echo "visudo: parse error" >&2; exit 1; } ;;
        esac
        ;;
    iptables | ip6tables) fw "$@" ;;
    systemctl)
        args=()
        for a in "$@"; do
            case "$a" in --quiet | --now) ;; *) args+=("$a") ;; esac
        done
        case "${args[0]}" in
            is-enabled) [ -e "${state}/enabled/${args[1]}" ] ;;
            enable)
                mutating "$@"
                touch "${state}/enabled/${args[1]}"
                ;;
            disable)
                mutating "$@"
                rm -f "${state}/enabled/${args[1]}"
                ;;
            *) mutating "$@" ;;
        esac
        ;;
    install)
        mutating "$@"
        mode="" owner="" group="" dir=0 paths=()
        while [ $# -gt 0 ]; do
            case "$1" in
                -d) dir=1 ;;
                -m) mode="$2"; shift ;;
                -o) owner="$2"; shift ;;
                -g) group="$2"; shift ;;
                *) paths+=("$1") ;;
            esac
            shift
        done
        if [ "$dir" = 1 ]; then
            targets=("${paths[@]}")
            for p in "${targets[@]}"; do real mkdir -p "$p"; done
        else
            real cp "${paths[0]}" "${paths[1]}"
            targets=("${paths[1]}")
        fi
        for p in "${targets[@]}"; do
            [ -z "$mode" ] || real chmod "$mode" "$p"
            echo "${owner:-root}:${group:-root}" >"$(meta_file "$p")"
        done
        ;;
    chown)
        mutating "$@"
        args=()
        for a in "$@"; do
            case "$a" in -R) ;; *) args+=("$a") ;; esac
        done
        spec="${args[0]}"
        p="${args[1]}"
        o="${spec%%:*}"
        g="${spec#*:}"
        mf="$(meta_file "$p")"
        if [ -e "$mf" ]; then
            cur_o="$(cut -d: -f1 "$mf")" cur_g="$(cut -d: -f2 "$mf")"
        else
            cur_o="$(real stat -c %U "$p")" cur_g="$(real stat -c %G "$p")"
        fi
        echo "${o:-$cur_o}:${g:-$cur_g}" >"$mf"
        ;;
    chmod)
        mutating "$@"
        real chmod "$@"
        ;;
    stat)
        # stat -c FORMAT PATH: owner and group come from the records of the
        # other mocks, the mode from the real file.
        fmt="$2"
        p="$3"
        [ -e "$p" ] || exit 1
        mf="$(meta_file "$p")"
        u="$(real stat -c %U "$p")" g="$(real stat -c %G "$p")"
        if [ -e "$mf" ]; then u="$(cut -d: -f1 "$mf")" g="$(cut -d: -f2 "$mf")"; fi
        a="$(real stat -c %a "$p")"
        fmt="${fmt//%U/$u}"
        fmt="${fmt//%G/$g}"
        fmt="${fmt//%a/$a}"
        echo "$fmt"
        ;;
    *)
        echo "mock-host-cmds: no mock for ${cmd}" >&2
        exit 127
        ;;
esac
