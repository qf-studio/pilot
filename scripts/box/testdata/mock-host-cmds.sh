#!/bin/bash
# Mock host commands for setup-agent-user_test.sh. One script, linked into the
# test's stub bin under the name of each command it stands for (useradd,
# groupadd, usermod, visudo, iptables, ip6tables, systemctl, install, chown,
# chmod, id, getent, stat, sudo, runuser, pgrep, pkill, sleep); the command is
# the basename of $0.
#
# Every call is appended to $MOCK_CALL_LOG. A call that would change a real
# host also goes to $MOCK_MUT_LOG, so a dry run can be shown to change nothing.
# The "host" is a state directory ($MOCK_STATE_DIR): users/ (one file per user:
# uid:shell:primary-group, empty for the defaults), groups/, members/<group>,
# meta/ (owner:group per path), enabled/, <family>.rules, gap.log (a firewall
# change that left the OUTPUT chain without a jump to a chain with a REJECT rule),
# procs/<user> (one fake pid per line: the processes of the user that pgrep
# lists and pkill ends). sleep returns at once.
#
# Commands that act on files only do so inside $MOCK_SANDBOX; anywhere else they
# are logged and refused, so a script that went wrong cannot touch the real host.
# Only portable flags of the real tools are used (the test runs on macOS too).
#
# sudo decides as sudo does, from the sudoers files under $SUDOERS_DIR: the
# invoking user is root, or the user that runuser named (MOCK_INVOKER); a rule
# for that user (its name, ALL or %group) with the target in its run-as part
# runs the command when it has NOPASSWD or a Defaults line turns authenticate
# off, and else fails with "a password is required". `sudo -ll -U` prints the
# long listing built from the same files (MOCK_SUDO_LIST: a file printed instead).
#
# runuser -u USER -- ls|touch|rm|mkdir PATH... decides access as the kernel
# does for USER, from the owner:group records (meta/), the real modes and the
# groups of USER: every directory above PATH needs x, ls needs r, touch, rm and
# mkdir need w on the parent (touch on an existing file: w on the file). A
# denied call fails with "Permission denied"; an allowed one runs, and a path
# it makes gets USER and its primary group as owner. Other commands run as
# they are, with USER as the invoker that the sudo mock sees.
#
# Knobs (env): MOCK_UID (answer of `id -u`, default 0), MOCK_VISUDO_FAIL=1
# (`visudo -cf` fails), MOCK_VISUDO_C_FAIL=1 (`visudo -c` fails),
# MOCK_SUDO_BROKEN=1 (sudo fails whatever the rules), MOCK_SUDO_ROOT_OK=1 (sudo
# to root succeeds whatever the files hold: a rule from another source),
# MOCK_SUDO_LIST (a file that `sudo -ll -U` prints as the rule list),
# MOCK_SUDO_LIST_RC (the exit status of `sudo -ll -U` after MOCK_SUDO_LIST),
# MOCK_SUDO_LATE (names of files in $SUDOERS_DIR that sudo reads only late, see
# below),
# MOCK_REAL_PATH (PATH for the real tools the mocks delegate to),
# MOCK_PROC_TERM_IGNORED=1 (pkill -TERM ends no process), MOCK_PROC_UNKILLABLE=1
# (pkill -KILL ends none either), MOCK_PGREP_RC (pgrep exits with this code and
# lists nothing; 127 stands for a pgrep that is not found).
#
# MOCK_SUDO_BROKEN, MOCK_SUDO_ROOT_OK, MOCK_SUDO_LIST(_RC) and MOCK_SUDO_LATE act
# late: only once the agent rule ($SUDOERS_DIR/90-pilot-agent-runas) is in
# place. The check of the sudo rules that the script runs before its first
# change sees the sudoers files alone; the proof after the edit sees the knob.

set -euo pipefail

cmd="${0##*/}"
state="${MOCK_STATE_DIR:?}"
sandbox="${MOCK_SANDBOX:?}"
[ -d "${state}/procs" ] || mkdir -p "$state"/{users,groups,members,meta,enabled,procs}

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

# meta_of PATH — "owner group mode" of PATH as the stat mock reports it.
# Every ancestor of the sandbox, and the sandbox itself, reads as root:root 0755.
meta_of() {
    local p="$1" rs u g a mf
    if [ "$p" = / ] || case "${sandbox}/" in "$p"/*) true ;; *) false ;; esac; then
        echo "root root 755"
        return 0
    fi
    if ! rs="$(real stat -c '%U:%G:%a' "$p" 2>/dev/null)"; then
        rs="$(real stat -f '%Su:%Sg' "$p"):$(bsd_mode "$p")"
    fi
    IFS=: read -r u g a <<<"$rs"
    mf="$(meta_file "$p")"
    if [ -e "$mf" ]; then IFS=: read -r u g <"$mf"; fi
    echo "${u} ${g} ${a}"
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

# late — the agent rule is in place: the late knobs act (see the top).
late() { [ -f "${SUDOERS_DIR:?}/90-pilot-agent-runas" ]; }

# sudoers_files — the files sudo reads: every file of $SUDOERS_DIR without a
# dot or a tilde in its name; a file named in MOCK_SUDO_LATE only late.
sudoers_files() {
    local f
    for f in "${SUDOERS_DIR:?}"/*; do
        [ -f "$f" ] || continue
        case "$(basename "$f")" in *.* | *~) continue ;; esac
        case " ${MOCK_SUDO_LATE:-} " in *" $(basename "$f") "*) late || continue ;; esac
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

# ------------------------------------------------------------ file access

# may USER PATH BIT — USER has BIT (4 r, 2 w, 1 x) on PATH (a link is followed).
may() {
    local user="$1" p o g a m cls
    p="$(real realpath "$2")"
    read -r o g a <<<"$(meta_of "$p")"
    m=$((8#$a))
    if [ "$o" = "$user" ]; then
        cls=$(((m >> 6) & 7))
    elif case " $("${0%/*}/id" -nG "$user") " in *" ${g} "*) true ;; *) false ;; esac; then
        cls=$(((m >> 3) & 7))
    else
        cls=$((m & 7))
    fi
    [ $((cls & $3)) -ne 0 ]
}

# reach USER PATH — USER may pass every directory above PATH (x).
reach() {
    local user="$1" d
    d="$(real realpath "$(dirname "$2")")" || return 1
    while case "$d" in "$sandbox"/*) true ;; *) false ;; esac; do
        may "$user" "$d" 1 || return 1
        d="$(dirname "$d")"
    done
}

# as_user USER COMMAND PATH... — COMMAND (ls, touch, rm, mkdir) with the
# access of USER; options are passed through (mkdir -m takes a value).
as_user() {
    local user="$1" c="$2" a parent made opts=() paths=()
    shift 2
    while [ $# -gt 0 ]; do
        case "$1" in
            -m) opts+=("$1" "$2"); shift ;;
            -*) opts+=("$1") ;;
            *) paths+=("$1") ;;
        esac
        shift
    done
    # One path at a time, as the real tool goes: a directory that mkdir -p
    # made is the parent of the next one.
    for a in "${paths[@]}"; do
        inside_sandbox "$a"
        parent="$(dirname "$a")"
        made=0
        case "$c" in
            ls) reach "$user" "$a" && may "$user" "$a" 4 && may "$user" "$a" 1 ;;
            touch)
                if [ -e "$a" ]; then
                    reach "$user" "$a" && may "$user" "$a" 2
                else
                    made=1
                    reach "$user" "$a" && may "$user" "$parent" 2 && may "$user" "$parent" 1
                fi
                ;;
            rm) reach "$user" "$a" && may "$user" "$parent" 2 && may "$user" "$parent" 1 ;;
            mkdir)
                if [ ! -d "$a" ]; then
                    made=1
                    reach "$user" "$a" && may "$user" "$parent" 2 && may "$user" "$parent" 1
                fi
                ;;
        esac || { echo "${c}: cannot access '${a}': Permission denied" >&2; return 1; }
        # A made directory is a change of the host; the file of a proof that is
        # made and removed again is not.
        if [ "$c" = mkdir ] && [ "$made" = 1 ]; then
            mutating "as ${user}:" "$c" "${opts[@]+"${opts[@]}"}" "$a"
        fi
        real "$c" "${opts[@]+"${opts[@]}"}" "$a"
        if [ "$made" = 1 ]; then
            echo "${user}:$(user_field "$user" 3 "$user")" >"$(meta_file "$a")"
        fi
    done
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
        case "${1:-}" in
            ls | touch | rm | mkdir) as_user "$invoker" "$@" ;;
            *) MOCK_INVOKER="$invoker" exec "$@" ;;
        esac
        ;;
    sudo)
        [ "${MOCK_SUDO_BROKEN:-0}" != 1 ] || ! late || { echo "sudo: unable to initialize policy plugin" >&2; exit 1; }
        listing=0 target=root user=""
        while [ $# -gt 0 ]; do
            case "$1" in
                -n) ;;
                -l | -ll) listing=1 ;;
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
            if [ "$invoker" = root ] || { [ "$target" = root ] && [ "${MOCK_SUDO_ROOT_OK:-0}" = 1 ] && late; }; then
                exec "$@"
            fi
            case "$(sudo_verdict "$invoker" "$target")" in
                nopasswd) exec "$@" ;;
                passwd) echo "sudo: a password is required" >&2 ;;
                *) echo "Sorry, user ${invoker} is not allowed to execute '$*' as ${target} on host." >&2 ;;
            esac
            exit 1
        fi
        # sudo -n -ll -U USER: the long listing of sudo 1.9, built from the
        # sudoers files (the user, ALL or one of its groups), so it follows
        # every edit the script makes; MOCK_SUDO_LIST names a file printed
        # instead (late), with the exit status MOCK_SUDO_LIST_RC.
        if [ -n "${MOCK_SUDO_LIST:-}" ] && late; then
            cat "$MOCK_SUDO_LIST"
            exit "${MOCK_SUDO_LIST_RC:-0}"
        fi
        groups=" $("${0%/*}/id" -nG "$user" 2>/dev/null || true) "
        rules=""
        while IFS= read -r f; do
            rules="${rules}$(awk -v u="$user" -v groups="$groups" '
                function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
                /^[ \t]*#/ || /^[ \t]*Defaults/ || !/=/ { next }
                {
                    line = trim($0)
                    eq = index(line, "=")
                    left = substr(line, 1, eq - 1); right = trim(substr(line, eq + 1))
                    split(left, w, /[ \t]+/)
                    n = split(w[1], users, ",")
                    hit = 0
                    for (i = 1; i <= n; i++) {
                        if (users[i] == u || users[i] == "ALL") hit = 1
                        if (substr(users[i], 1, 1) == "%" && index(groups, " " substr(users[i], 2) " ") > 0) hit = 1
                    }
                    if (!hit) next
                    ru = "root"; rg = ""
                    if (substr(right, 1, 1) == "(") {
                        rp = index(right, ")")
                        runas = substr(right, 2, rp - 2)
                        right = trim(substr(right, rp + 1))
                        if (index(runas, ":")) { rg = trim(substr(runas, index(runas, ":") + 1)); runas = substr(runas, 1, index(runas, ":") - 1) }
                        ru = trim(runas)
                        if (ru == "") ru = u
                    }
                    opts = ""
                    if (right ~ /SETENV:/) opts = "setenv"
                    if (right ~ /NOPASSWD:/) opts = opts (opts == "" ? "" : ", ") "!authenticate"
                    gsub(/(NOPASSWD|SETENV):[ \t]*/, "", right)
                    printf "\nSudoers entry: %s\n    RunAsUsers: %s\n", FILENAME, ru
                    if (rg != "") printf "    RunAsGroups: %s\n", rg
                    if (opts != "") printf "    Options: %s\n", opts
                    printf "    Commands:\n"
                    n = split(right, c, ",")
                    for (i = 1; i <= n; i++) printf "\t%s\n", trim(c[i])
                }' "$f")"$'\n'
        done < <(sudoers_files)
        [ "${MOCK_SUDO_ROOT_OK:-0}" != 1 ] || ! late || rules="${rules}"$'\n''Sudoers entry: another source'$'\n''    RunAsUsers: ALL'$'\n''    Options: !authenticate'$'\n''    Commands:'$'\n\t''ALL'
        if [ -z "$(printf '%s' "$rules" | tr -d '[:space:]')" ]; then
            echo "User ${user} is not allowed to run sudo on host."
            exit 0
        fi
        echo "Matching Defaults entries for ${user} on host:"
        echo "    !visiblepw, always_set_home"
        echo
        echo "User ${user} may run the following commands on host:"
        printf '%s\n' "$rules" | grep -v '^$' | awk '/^Sudoers entry:/ { print "" } { print }'
        ;;
    pgrep)
        # pgrep [-l] -u USER
        case "${MOCK_PGREP_RC:-}" in
            "") ;;
            127) echo "pgrep: command not found" >&2; exit 127 ;;
            *) echo "pgrep: mock failure" >&2; exit "$MOCK_PGREP_RC" ;;
        esac
        user="" list=0
        while [ $# -gt 0 ]; do
            case "$1" in
                -l) list=1 ;;
                -u) user="$2"; shift ;;
            esac
            shift
        done
        [ -s "${state}/procs/${user}" ] || exit 1
        if [ "$list" = 1 ]; then sed 's/$/ mock-proc/' "${state}/procs/${user}"; else cat "${state}/procs/${user}"; fi
        ;;
    pkill)
        # pkill -SIGNAL -u USER
        mutating "$@"
        [ "${2:-}" = -u ] || exit 2
        [ -s "${state}/procs/$3" ] || exit 1
        case "$1" in
            -TERM) [ "${MOCK_PROC_TERM_IGNORED:-0}" = 1 ] || [ "${MOCK_PROC_UNKILLABLE:-0}" = 1 ] || : >"${state}/procs/$3" ;;
            -KILL) [ "${MOCK_PROC_UNKILLABLE:-0}" = 1 ] || : >"${state}/procs/$3" ;;
            *) exit 2 ;;
        esac
        ;;
    sleep) ;;
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
        read -r u g a <<<"$(meta_of "$p")"
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
