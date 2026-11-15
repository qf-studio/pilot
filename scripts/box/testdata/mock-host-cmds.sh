#!/bin/bash
# Mock host commands for setup-agent-user_test.sh. One script, linked into the
# test's stub bin under the name of each command it stands for (useradd,
# groupadd, usermod, visudo, iptables, ip6tables, systemctl, install, chown,
# chmod, id, getent, stat, sudo, runuser); the command is the basename of $0.
#
# Every call is appended to $MOCK_CALL_LOG. A call that would change a real
# host also goes to $MOCK_MUT_LOG, so a dry run can be shown to change nothing.
# The "host" is a state directory ($MOCK_STATE_DIR): users/ (one file per user:
# uid:shell:primary-group, empty for the defaults), groups/, members/<group>,
# meta/ (owner:group per path), enabled/, <family>.rules, gap.log (a firewall
# change that left the OUTPUT chain without a jump to a chain with a REJECT rule).
#
# Commands that act on files only do so inside $MOCK_SANDBOX; anywhere else they
# are logged and refused, so a script that went wrong cannot touch the real host.
# Only portable flags of the real tools are used (the test runs on macOS too).
#
# sudo decides as sudo does, from the sudoers files under $SUDOERS_DIR: the
# invoking user is root, or the user that runuser named (MOCK_INVOKER); a rule
# for that user (its name, ALL or %group) with the target in its run-as part
# runs the command when it has NOPASSWD or a Defaults line turns authenticate
# off, and else fails with "a password is required". `sudo -l -U` prints the
# listing built from the same files.
#
# Knobs (env): MOCK_UID (answer of `id -u`, default 0), MOCK_VISUDO_FAIL=1
# (`visudo -cf` fails), MOCK_VISUDO_C_FAIL=1 (`visudo -c` fails),
# MOCK_SUDO_BROKEN=1 (sudo fails whatever the rules), MOCK_SUDO_ROOT_OK=1 (sudo
# to root succeeds whatever the files hold: a rule from another source),
# MOCK_REAL_PATH (PATH for the real tools the mocks delegate to).

set -euo pipefail

cmd="${0##*/}"
state="${MOCK_STATE_DIR:?}"
sandbox="${MOCK_SANDBOX:?}"
[ -d "${state}/enabled" ] || mkdir -p "$state"/{users,groups,members,meta,enabled}

real() { PATH="${MOCK_REAL_PATH:?}" "$@"; }

echo "${cmd} $*" >>"${MOCK_CALL_LOG:?}"
mutating() { echo "${cmd} $*" >>"${MOCK_MUT_LOG:?}"; }

# inside_sandbox PATH — refuse (exit 1) to act on a real path outside the sandbox.
inside_sandbox() {
    case "$1" in
        "$sandbox"/*) return 0 ;;
    esac
    echo "mock ${cmd}: refusing to touch ${1} outside the sandbox" >&2
    return 1
}

# Path -> file name for the owner:group record.
meta_file() { echo "${state}/meta/${1//\//_}"; }

# bsd_mode PATH — the mode as GNU stat %a prints it, from BSD stat: %Mp holds
# the setgid, setuid and sticky digit (%Lp alone drops it, 2770 would read as
# 770), and the leading zero of "0755" goes.
bsd_mode() {
    local m
    m="$(real stat -f '%Mp%Lp' "$1")" || return 1
    echo "${m#0}"
}

# real_stat FIELD PATH — one field (U, G or a) from the real file, GNU or BSD stat.
real_stat() {
    local f="$1" p="$2" out
    if out="$(real stat -c "%${f}" "$p" 2>/dev/null)"; then
        echo "$out"
        return 0
    fi
    case "$f" in
        U) real stat -f %Su "$p" ;;
        G) real stat -f %Sg "$p" ;;
        a) bsd_mode "$p" ;;
    esac
}

# user_field NAME INDEX DEFAULT — uid (1), shell (2) or primary group (3).
user_field() {
    local v="" f="${state}/users/$1"
    if [ -s "$f" ]; then
        v="$(cut -d: -f"$2" "$f")"
    fi
    echo "${v:-$3}"
}

# ------------------------------------------------------------------ firewall
# Rules are kept as lines: "chain NAME" and "rule CHAIN ARGS", in order.

# fw_effective — 0 when OUTPUT has a jump to a chain that holds a REJECT rule.
fw_effective() {
    awk '
        $1 == "chain" { chains[$2] = 1 }
        $1 == "rule" && / -j REJECT/ { rejects[$2] = 1 }
        $1 == "rule" && $2 == "OUTPUT" { targets[++n] = $NF }
        END { for (i = 1; i <= n; i++) if (chains[targets[i]] && rejects[targets[i]]) exit 0; exit 1 }' "$1"
}

# fw_op ARGS... — the operation on the rules file; return 1 for an iptables error.
fw_op() {
    local rules="${state}/${cmd}.rules" op chain pos line a args=()
    for a in "$@"; do
        case "$a" in -w | -n) ;; *) args+=("$a") ;; esac
    done
    op="${args[0]}"
    chain="${args[1]:-}"
    touch "$rules"
    case "$op" in
        -L) grep -qFx "chain ${chain}" "$rules" ;;
        -S)
            case "$chain" in
                OUTPUT | INPUT | FORWARD) echo "-P ${chain} ACCEPT" ;;
                *)
                    grep -qFx "chain ${chain}" "$rules" || return 1
                    echo "-N ${chain}"
                    ;;
            esac
            awk -v c="$chain" '$1 == "rule" && $2 == c { $1 = ""; $2 = ""; sub(/^  /, ""); print "-A " c " " $0 }' "$rules"
            ;;
        -C) grep -qFx "rule ${args[*]:1}" "$rules" ;;
        -N)
            grep -qFx "chain ${chain}" "$rules" && return 1
            echo "chain ${chain}" >>"$rules"
            ;;
        -A)
            echo "rule ${args[*]:1}" >>"$rules"
            ;;
        -I)
            pos=""
            line="rule ${chain} ${args[*]:2}"
            if [[ "${args[2]:-}" =~ ^[0-9]+$ ]]; then
                pos="${args[2]}"
                line="rule ${chain} ${args[*]:3}"
            fi
            awk -v c="$chain" -v pos="${pos:-1}" -v line="$line" '
                $1 == "rule" && $2 == c { n++; if (n == pos) { print line; done = 1 } }
                { print }
                END { if (!done) print line }' "$rules" >"${rules}.new"
            mv "${rules}.new" "$rules"
            ;;
        -D)
            if [[ "${args[2]:-}" =~ ^[0-9]+$ ]]; then
                pos="${args[2]}"
                awk -v c="$chain" -v pos="$pos" '
                    $1 == "rule" && $2 == c { n++; if (n == pos) { hit = 1; next } }
                    { print }
                    END { exit hit ? 0 : 1 }' "$rules" >"${rules}.new" || { rm -f "${rules}.new"; return 1; }
            else
                line="rule ${args[*]:1}"
                grep -qFx "$line" "$rules" || return 1
                # the first match only, as iptables does
                awk -v line="$line" '!done && $0 == line { done = 1; next } { print }' "$rules" >"${rules}.new"
            fi
            mv "${rules}.new" "$rules"
            ;;
        -F)
            grep -v "^rule ${chain} " "$rules" >"${rules}.new" || true
            mv "${rules}.new" "$rules"
            ;;
        -X)
            grep -qFx "chain ${chain}" "$rules" || return 1
            if grep -q "^rule ${chain} " "$rules" || grep -q "^rule .* -j ${chain}\$" "$rules"; then
                echo "iptables: Directory not empty / Too many links." >&2
                return 1
            fi
            grep -vFx "chain ${chain}" "$rules" >"${rules}.new" || true
            mv "${rules}.new" "$rules"
            ;;
        -E)
            grep -qFx "chain ${chain}" "$rules" || return 1
            grep -qFx "chain ${args[2]}" "$rules" && return 1
            awk -v old="$chain" -v new="${args[2]}" '
                $1 == "chain" && $2 == old { print "chain " new; next }
                $1 == "rule" {
                    if ($2 == old) $2 = new
                    if ($NF == old && $(NF - 1) == "-j") $NF = new
                }
                { print }' "$rules" >"${rules}.new"
            mv "${rules}.new" "$rules"
            ;;
        *) return 2 ;;
    esac
}

fw() {
    local rules="${state}/${cmd}.rules" op="" a was=0 rc=0
    for a in "$@"; do
        case "$a" in -w | -n) ;; *) op="$a"; break ;; esac
    done
    case "$op" in
        -N | -A | -I | -D | -F | -X | -E)
            mutating "$@"
            touch "$rules"
            if fw_effective "$rules"; then was=1; fi
            fw_op "$@" || rc=$?
            if [ "$was" = 1 ] && ! fw_effective "$rules"; then
                echo "${cmd} $*" >>"${state}/gap.log"
            fi
            return "$rc"
            ;;
        *) fw_op "$@" ;;
    esac
}

# --------------------------------------------------------------------- sudo

# sudoers_files — the files sudo reads: every file of $SUDOERS_DIR without a
# dot or a tilde in its name.
sudoers_files() {
    local f
    for f in "${SUDOERS_DIR:?}"/*; do
        [ -f "$f" ] || continue
        case "$(basename "$f")" in *.* | *~) continue ;; esac
        echo "$f"
    done
}

# sudo_verdict USER TARGET — "nopasswd" when a rule lets USER run a command as
# TARGET without a password, "passwd" when one needs the password, "" for none.
sudo_verdict() {
    local user="$1" target="$2" groups f files=()
    groups=" $("${0%/*}/id" -nG "$user" 2>/dev/null || true) "
    while IFS= read -r f; do files+=("$f"); done < <(sudoers_files)
    [ "${#files[@]}" -gt 0 ] || return 0
    awk -v u="$user" -v t="$target" -v groups="$groups" '
        function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
        function is_user(list,   n, i, w) {
            n = split(list, w, ",")
            for (i = 1; i <= n; i++) {
                w[i] = trim(w[i])
                if (w[i] == u || w[i] == "ALL") return 1
                if (substr(w[i], 1, 1) == "%" && index(groups, " " substr(w[i], 2) " ") > 0) return 1
            }
            return 0
        }
        /^[ \t]*#/ || /^[ \t]*$/ { next }
        $1 == "Defaults" || $1 == "Defaults:" u {
            if ($0 ~ /!authenticate/) noauth = 1
            next
        }
        $1 ~ /^Defaults/ { next }
        {
            line = trim($0)
            eq = index(line, "=")
            if (eq == 0) next
            left = trim(substr(line, 1, eq - 1)); right = trim(substr(line, eq + 1))
            split(left, w, /[ \t]+/)
            if (!is_user(w[1])) next
            runas = "root"
            if (substr(right, 1, 1) == "(") {
                rp = index(right, ")")
                runas = trim(substr(right, 2, rp - 2))
                sub(/:.*/, "", runas)
                right = substr(right, rp + 1)
            }
            n = split(runas, r, ",")
            hit = 0
            for (i = 1; i <= n; i++) if (trim(r[i]) == t || trim(r[i]) == "ALL") hit = 1
            if (!hit) next
            found = 1
            if (right ~ /NOPASSWD:/) nopass = 1
        }
        END { if (found) print ((nopass || noauth) ? "nopasswd" : "passwd") }' "${files[@]}"
}

# --------------------------------------------------------------------- hosts

case "$cmd" in
    id)
        case "${1:-}" in
            -u)
                if [ -z "${2:-}" ]; then
                    echo "${MOCK_UID:-0}"
                else
                    [ -e "${state}/users/$2" ] || exit 1
                    user_field "$2" 1 1000
                fi
                ;;
            -gn)
                [ -e "${state}/users/$2" ] || exit 1
                user_field "$2" 3 "$2"
                ;;
            -nG)
                [ -e "${state}/users/$2" ] || exit 1
                out=" $(user_field "$2" 3 "$2")"
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
        case "$1" in
            group)
                [ -e "${state}/groups/$2" ] || exit 2
                echo "$2:x:998:"
                ;;
            passwd)
                [ -e "${state}/users/$2" ] || exit 2
                echo "$2:x:$(user_field "$2" 1 1000):998::/home/$2:$(user_field "$2" 2 /usr/sbin/nologin)"
                ;;
            *) exit 2 ;;
        esac
        ;;
    groupadd)
        mutating "$@"
        touch "${state}/groups/${!#}"
        ;;
    useradd)
        mutating "$@"
        shell="/usr/sbin/nologin" grp="${!#}"
        prev=""
        for a in "$@"; do
            case "$prev" in
                --shell) shell="$a" ;;
                --gid) grp="$a" ;;
            esac
            prev="$a"
        done
        echo "1000:${shell}:${grp}" >"${state}/users/${!#}"
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
    runuser)
        # runuser -u USER -- COMMAND...: COMMAND runs with USER as the invoker
        # that the sudo mock sees.
        [ "${1:-}" = -u ] || exit 2
        invoker="$2"
        shift 2
        [ "${1:-}" != -- ] || shift
        [ -e "${state}/users/${invoker}" ] || { echo "runuser: user ${invoker} does not exist" >&2; exit 1; }
        MOCK_INVOKER="$invoker" exec "$@"
        ;;
    sudo)
        [ "${MOCK_SUDO_BROKEN:-0}" != 1 ] || { echo "sudo: unable to initialize policy plugin" >&2; exit 1; }
        listing=0 target=root user=""
        while [ $# -gt 0 ]; do
            case "$1" in
                -n) ;;
                -l) listing=1 ;;
                -U) user="$2"; shift ;;
                -u) target="$2"; shift ;;
                --) shift; break ;;
                -*) echo "mock sudo: unknown flag $1" >&2; exit 2 ;;
                *) break ;;
            esac
            shift
        done
        if [ "$listing" = 0 ]; then
            invoker="${MOCK_INVOKER:-root}"
            if [ "$invoker" = root ] || { [ "$target" = root ] && [ "${MOCK_SUDO_ROOT_OK:-0}" = 1 ]; }; then
                exec "$@"
            fi
            case "$(sudo_verdict "$invoker" "$target")" in
                nopasswd) exec "$@" ;;
                passwd) echo "sudo: a password is required" >&2 ;;
                *) echo "Sorry, user ${invoker} is not allowed to execute '$*' as ${target} on host." >&2 ;;
            esac
            exit 1
        fi
        # sudo -n -l -U USER: the listing is built from the sudoers files, so it
        # follows every edit the script makes.
        rules=""
        for f in "${SUDOERS_DIR:?}"/*; do
            [ -f "$f" ] || continue
            case "$(basename "$f")" in *.* | *~) continue ;; esac
            rules="${rules}$(awk -v u="$user" '
                /^[ \t]*#/ || !/=/ { next }
                {
                    line = $0; sub(/^[ \t]+/, "", line)
                    eq = index(line, "=")
                    left = substr(line, 1, eq - 1); right = substr(line, eq + 1)
                    sub(/^[ \t]+/, "", right); sub(/[ \t]+$/, "", right)
                    split(left, w, /[ \t]+/)
                    n = split(w[1], users, ",")
                    for (i = 1; i <= n; i++) if (users[i] == u || users[i] == "ALL") hit = 1
                    if (!hit) next
                    hit = 0
                    if (substr(right, 1, 1) != "(") right = "(root) " right
                    gsub(/NOPASSWD:[ \t]*/, "NOPASSWD: ", right)
                    gsub(/SETENV:[ \t]*/, "SETENV: ", right)
                    # sudo prints the tags in its own order
                    sub(/NOPASSWD: SETENV:/, "SETENV: NOPASSWD:", right)
                    print "    " right
                }' "$f")"$'\n'
        done
        [ "${MOCK_SUDO_ROOT_OK:-0}" != 1 ] || rules="${rules}    (ALL) NOPASSWD: ALL"
        if [ -z "$(printf '%s' "$rules" | tr -d '[:space:]')" ]; then
            echo "User ${user} is not allowed to run sudo on host."
            exit 1
        fi
        echo "Matching Defaults entries for ${user} on host:"
        echo "    !visiblepw, always_set_home"
        echo
        echo "User ${user} may run the following commands on host:"
        printf '%s\n' "$rules" | grep -v '^$'
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
            for p in "${targets[@]}"; do
                inside_sandbox "$p"
                real mkdir -p "$p"
            done
        else
            inside_sandbox "${paths[1]}"
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
            cur_o="$(real_stat U "$p")" cur_g="$(real_stat G "$p")"
        fi
        echo "${o:-$cur_o}:${g:-$cur_g}" >"$mf"
        ;;
    chmod)
        mutating "$@"
        inside_sandbox "${!#}"
        real chmod "$@"
        ;;
    stat)
        # stat [-L] -c FORMAT PATH: owner and group come from the records of the
        # other mocks, the mode from the real file. Every ancestor of the sandbox
        # reads as root:root 0755, whatever the temp dir of the host is. With -L
        # a link is followed, its target's records are used.
        deref=0 fmt=""
        while [ $# -gt 1 ]; do
            case "$1" in
                -L) deref=1 ;;
                -c) fmt="$2"; shift ;;
            esac
            shift
        done
        p="$1"
        [ -e "$p" ] || [ -L "$p" ] || exit 1
        if [ "$deref" = 1 ]; then
            [ -e "$p" ] || exit 1
            p="$(real realpath "$p")"
        fi
        mf="$(meta_file "$p")"
        if ! rs="$(real stat -c '%U:%G:%a' "$p" 2>/dev/null)"; then
            rs="$(real stat -f '%Su:%Sg' "$p"):$(bsd_mode "$p")"
        fi
        IFS=: read -r u g a <<<"$rs"
        if [ -e "$mf" ]; then IFS=: read -r u g <"$mf"; fi
        if [ "$p" = / ] || case "${sandbox}/" in "$p"/*) true ;; *) false ;; esac; then
            u=root g=root a=755
        fi
        fmt="${fmt//%U/$u}"
        fmt="${fmt//%G/$g}"
        fmt="${fmt//%g/1000}"
        fmt="${fmt//%a/$a}"
        echo "$fmt"
        ;;
    *)
        echo "mock-host-cmds: no mock for ${cmd}" >&2
        exit 127
        ;;
esac
