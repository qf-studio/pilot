#!/usr/bin/env bash
# Bootstrap cost harness runner (TASK-581).
#
# Measures what the Pilot Cloud "deep research" bootstrap costs per repo size.
# Each session is one of three stages, run on a scratch checkout:
#   0  context        pilot navigator bootstrap --project <checkout>
#   1  deep research  claude -p ... prompts/stage1-research.md  (Opus by default)
#   2  ticket drafts  claude -p ... prompts/stage2-draft.md     (Sonnet by default)
# and appends one JSON line per session to results/<date>.jsonl. Turn
# bench-bootstrap-report (Makefile) into the markdown table afterwards.
#
# THIS SCRIPT SPENDS REAL MONEY unless --dry-run is given. It is an operator
# tool for the founder box; no test runs it without --dry-run.
#
# Default matrix (30 sessions): 4 repos x 3 stages x 2 runs, plus one Sonnet
# stage-1 fallback run per repo, plus the XL repo's stage 1 scoped to its most
# active directories (2 runs). --scope DIRS replaces all that with scoped runs.
#
# Guards: --max-usd is passed to the CLI as --max-budget-usd for stages 1-2
# (stage 0 has its own 60-turn / 10-minute cap and no budget flag); the sum of
# reported USD across this invocation stops the matrix at --matrix-max-usd.
#
# Nothing here writes under the repo's .agent/ directory. The agent sessions
# write only inside their scratch checkout.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROMPTS_DIR="$SCRIPT_DIR/prompts"
REPORT_PY="$SCRIPT_DIR/bootstrap_report.py"
PRICES_JSON="$SCRIPT_DIR/prices.json"

# name|owner/repo|class|stack|default scope for the scoped variant (empty: none)
# The scoped default is static because a depth-1 clone has no history to rank
# directories by 12-month churn; pass --scope to override it.
REPO_TABLE=(
    "sqlx|jmoiron/sqlx|S|Go|"
    "react-router|remix-run/react-router|M|TS monorepo|"
    "cli|cli/cli|L|Go|"
    "grafana|grafana/grafana|XL|Go+TS|pkg/api,pkg/services,public/app/features"
    "django|django/django|L|Python|"
    "moby|moby/moby|XL|Go|"
)
DEFAULT_REPOS="sqlx,react-router,cli,grafana"

# Stage caps (TASK-581): turns and wall-clock seconds.
STAGE0_TURNS=60;  STAGE0_TIMEOUT=660
STAGE1_TURNS=150; STAGE1_TIMEOUT=1200
STAGE2_TURNS=40;  STAGE2_TIMEOUT=300

# Read-only tool allowlist; keep in sync with ChatReadOnlyTools in
# internal/executor/chat_turn.go.
READ_ONLY_TOOLS="Read,Grep,Glob,LS,Task,Bash(git log:*),Bash(git show:*),Bash(git diff:*),Bash(git blame:*),Bash(git ls-files:*),Bash(rg:*),Bash(ls:*)"
STAGE1_TOOLS="$READ_ONLY_TOOLS,Write(.agent/system/bootstrap-research.md)"
STAGE2_TOOLS="$READ_ONLY_TOOLS,Write(.agent/tasks/bootstrap/**)"

REPOS="$DEFAULT_REPOS"
RUNS=2
STAGES="0,1,2"
MODEL_RESEARCH="claude-opus-5-5"
MODEL_DRAFT="claude-sonnet-5-5"
MODEL_FALLBACK="claude-sonnet-5-5"
EFFORT="high"
SCOPE=""
MAX_USD=40
MATRIX_MAX_USD=250
DRY_RUN=0
FALLBACK=1
SCOPED=1
SCRATCH="${BOOTSTRAP_BENCH_SCRATCH:-${TMPDIR:-/tmp}/bootstrap-cost-scratch}"
OUT=""

usage() {
    cat <<'EOF'
Usage: scripts/bench/bootstrap-cost.sh [flags]

  --repos a,b,c         repo names or owner/name slugs (default: sqlx,react-router,cli,grafana;
                        alternates in the table: django, moby)
  --runs N              runs per repo and stage (default 2)
  --stages 0,1,2        stages to run (default all)
  --model-research ID   stage 1 model (default claude-opus-5-5)
  --model-draft ID      stage 2 model (default claude-sonnet-5-5)
  --effort LEVEL        effort recorded and exported as CLAUDE_CODE_EFFORT_LEVEL (default high)
  --scope d1,d2         only run stage(s) on these directories (replaces fallback and default scoped runs)
  --no-fallback         skip the Sonnet stage-1 pricing run
  --no-scoped           skip the default scoped stage-1 runs of XL repos
  --max-usd N           per-session budget for stages 1-2 (default 40)
  --matrix-max-usd N    stop once summed reported USD reaches N (default 250)
  --scratch DIR         scratch directory for checkouts and captures
  --out FILE            results JSONL (default scripts/bench/results/<date>.jsonl)
  --dry-run             print every command, spend and write nothing
  -h, --help            this text
EOF
}

die() { echo "bootstrap-cost: $*" >&2; exit 2; }

while [ $# -gt 0 ]; do
    case "$1" in
        --repos) REPOS="${2:?--repos needs a value}"; shift 2 ;;
        --runs) RUNS="${2:?--runs needs a value}"; shift 2 ;;
        --stages) STAGES="${2:?--stages needs a value}"; shift 2 ;;
        --model-research) MODEL_RESEARCH="${2:?}"; shift 2 ;;
        --model-draft) MODEL_DRAFT="${2:?}"; shift 2 ;;
        --effort) EFFORT="${2:?}"; shift 2 ;;
        --scope) SCOPE="${2:?--scope needs a value}"; shift 2 ;;
        --no-fallback) FALLBACK=0; shift ;;
        --no-scoped) SCOPED=0; shift ;;
        --max-usd) MAX_USD="${2:?}"; shift 2 ;;
        --matrix-max-usd) MATRIX_MAX_USD="${2:?}"; shift 2 ;;
        --scratch) SCRATCH="${2:?}"; shift 2 ;;
        --out) OUT="${2:?}"; shift 2 ;;
        --dry-run) DRY_RUN=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) usage >&2; die "unknown flag: $1" ;;
    esac
done

[[ "$RUNS" =~ ^[1-9][0-9]*$ ]] || die "--runs must be a positive integer"
[[ "$MAX_USD" =~ ^[0-9]+(\.[0-9]+)?$ ]] || die "--max-usd must be a number"
[[ "$MATRIX_MAX_USD" =~ ^[0-9]+(\.[0-9]+)?$ ]] || die "--matrix-max-usd must be a number"
IFS=',' read -r -a STAGE_LIST <<<"$STAGES"
for s in "${STAGE_LIST[@]}"; do
    case "$s" in 0|1|2) ;; *) die "--stages takes 0, 1 and 2, got: $s" ;; esac
done
has_stage() { local s; for s in "${STAGE_LIST[@]}"; do [ "$s" = "$1" ] && return 0; done; return 1; }

[ -n "$OUT" ] || OUT="$SCRIPT_DIR/results/$(date +%F).jsonl"

# --- repo table lookup --------------------------------------------------------

# repo_field NAME INDEX -> field of the table row, or a derived default for an
# owner/name slug that is not in the table.
repo_row() {
    local want="$1" row name slug
    for row in "${REPO_TABLE[@]}"; do
        IFS='|' read -r name slug _ _ _ <<<"$row"
        if [ "$want" = "$name" ] || [ "$want" = "$slug" ]; then echo "$row"; return 0; fi
    done
    if [[ "$want" == */* ]]; then echo "${want##*/}|$want|?||"; return 0; fi
    return 1
}

# --- session plan ---------------------------------------------------------------

# Each entry: repo|variant|stage|run
SESSIONS=()
plan_session() { SESSIONS+=("$1|$2|$3|$4"); }

IFS=',' read -r -a REPO_LIST <<<"$REPOS"
for want in "${REPO_LIST[@]}"; do
    row="$(repo_row "$want")" || die "unknown repo: $want (use a name from the table or owner/name)"
    IFS='|' read -r name _ _ _ default_scope <<<"$row"
    # An owner/name slug outside the table: register it so later lookups by name work.
    repo_row "$name" >/dev/null 2>&1 || REPO_TABLE+=("$row")
    if [ -n "$SCOPE" ]; then
        for ((run = 1; run <= RUNS; run++)); do
            for s in "${STAGE_LIST[@]}"; do plan_session "$name" scoped "$s" "$run"; done
        done
        continue
    fi
    for ((run = 1; run <= RUNS; run++)); do
        for s in "${STAGE_LIST[@]}"; do plan_session "$name" full "$s" "$run"; done
    done
    if [ "$FALLBACK" = 1 ] && has_stage 1; then plan_session "$name" fallback 1 1; fi
    if [ "$SCOPED" = 1 ] && [ -n "$default_scope" ] && has_stage 1; then
        for ((run = 1; run <= RUNS; run++)); do plan_session "$name" scoped 1 "$run"; done
    fi
done
TOTAL=${#SESSIONS[@]}

# --- helpers --------------------------------------------------------------------

stage_turns()   { case "$1" in 0) echo $STAGE0_TURNS ;; 1) echo $STAGE1_TURNS ;; 2) echo $STAGE2_TURNS ;; esac; }
stage_timeout() { case "$1" in 0) echo $STAGE0_TIMEOUT ;; 1) echo $STAGE1_TIMEOUT ;; 2) echo $STAGE2_TIMEOUT ;; esac; }

stage_model() { # stage variant
    case "$1:$2" in
        0:*) echo "box default" ;;
        1:fallback) echo "$MODEL_FALLBACK" ;;
        1:*) echo "$MODEL_RESEARCH" ;;
        2:*) echo "$MODEL_DRAFT" ;;
    esac
}

scope_for() { # repo variant -> scope dirs for this session ("" when unscoped)
    local repo="$1" variant="$2" row default_scope
    [ "$variant" = scoped ] || { echo ""; return; }
    if [ -n "$SCOPE" ]; then echo "$SCOPE"; return; fi
    row="$(repo_row "$repo")"
    IFS='|' read -r _ _ _ _ default_scope <<<"$row"
    echo "$default_scope"
}

scope_line() { # scope -> sentence for the prompt
    if [ -z "$1" ]; then
        echo "no scope restriction; cover the whole repository."
    else
        echo "restrict reading to these directories: ${1//,/, }. Say so in the brief and list every top-level directory you skipped."
    fi
}

# render_prompt STAGE TURNS SCOPE -> prompt text on stdout
render_prompt() {
    local file text
    case "$1" in 1) file="$PROMPTS_DIR/stage1-research.md" ;; 2) file="$PROMPTS_DIR/stage2-draft.md" ;; esac
    text="$(cat "$file")"
    text="${text//\{\{TURN_CAP\}\}/$2}"
    text="${text//\{\{SCOPE_LINE\}\}/$(scope_line "$3")}"
    printf '%s\n' "$text"
}

# build_cmd STAGE MODEL WORKDIR -> fills the CMD array
build_cmd() {
    local stage="$1" model="$2" workdir="$3" turns timeout_s tools
    turns="$(stage_turns "$stage")"
    timeout_s="$(stage_timeout "$stage")"
    if [ "$stage" = 0 ]; then
        CMD=(timeout "$timeout_s" pilot navigator bootstrap --project "$workdir")
        return
    fi
    tools="$STAGE1_TOOLS"; [ "$stage" = 2 ] && tools="$STAGE2_TOOLS"
    CMD=(timeout "$timeout_s" claude -p --output-format stream-json --verbose
        --allowedTools "$tools" --max-turns "$turns" --model "$model"
        --max-budget-usd "$MAX_USD")
}

shellquote() { local out="" a; for a in "$@"; do out+="$(printf '%q' "$a") "; done; printf '%s' "${out% }"; }

run_start=$(date +%s)
TODAY="$(date +%F)"
MATRIX_USD="0"
RAN=0

# --- dry run ----------------------------------------------------------------------

if [ "$DRY_RUN" = 1 ]; then
    echo "[dry-run] $TOTAL sessions planned; nothing is cloned, run, spent or written."
    echo "[dry-run] scratch=$SCRATCH out=$OUT per-session-max=\$$MAX_USD matrix-max=\$$MATRIX_MAX_USD effort=$EFFORT"
    for want in "${REPO_LIST[@]}"; do
        row="$(repo_row "$want")"; IFS='|' read -r name slug _ _ _ <<<"$row"
        echo "PREP $name: git clone --depth 1 https://github.com/$slug.git $SCRATCH/$name/base && scc --format json $SCRATCH/$name/base"
    done
    n=0
    for entry in "${SESSIONS[@]}"; do
        IFS='|' read -r repo variant stage run <<<"$entry"
        n=$((n + 1))
        row="$(repo_row "$repo")"; IFS='|' read -r _ _ cls _ _ <<<"$row"
        model="$(stage_model "$stage" "$variant")"
        scope="$(scope_for "$repo" "$variant")"
        workdir="$SCRATCH/$repo/$variant-r$run"
        build_cmd "$stage" "$model" "$workdir"
        suffix=""
        [ "$stage" != 0 ] && suffix=" < $SCRATCH/prompts/$repo-$variant-r$run-stage$stage.md"
        echo "SESSION $n/$TOTAL repo=$repo class=$cls variant=$variant stage=$stage run=$run model=$model${scope:+ scope=$scope}"
        if [ "$stage" = 0 ]; then
            echo "  \$ ${CMD[*]}"
        else
            echo "  \$ (cd $workdir && CLAUDE_CODE_EFFORT_LEVEL=$EFFORT $(shellquote "${CMD[@]}")$suffix)"
        fi
    done
    echo "[dry-run] $TOTAL commands."
    exit 0
fi

# --- live run -----------------------------------------------------------------------

command -v python3 >/dev/null || die "python3 is required"
command -v git >/dev/null || die "git is required"
command -v timeout >/dev/null || die "GNU timeout is required (brew install coreutils; alias gtimeout)"
if has_stage 0; then command -v pilot >/dev/null || die "pilot is required for stage 0"; fi
if has_stage 1 || has_stage 2; then
    command -v claude >/dev/null || die "claude is required for stages 1-2"
    claude --help 2>&1 | grep -q -- '--max-budget-usd' ||
        echo "bootstrap-cost: warning: this claude has no --max-budget-usd; the per-session USD guard is NOT enforced" >&2
fi
LOC_TOOL=""
if command -v scc >/dev/null; then LOC_TOOL=scc; elif command -v cloc >/dev/null; then LOC_TOOL=cloc
else echo "bootstrap-cost: warning: neither scc nor cloc found; LOC columns will be 0" >&2; fi

mkdir -p "$SCRATCH/prompts" "$SCRATCH/streams" "$(dirname "$OUT")"
export CLAUDE_CODE_EFFORT_LEVEL="$EFFORT"

declare -A LOC_OF FILES_OF
prepare_repo() { # name -> clones base checkout, records LOC
    local name="$1" row slug base json loc files
    row="$(repo_row "$name")"; IFS='|' read -r _ slug _ _ _ <<<"$row"
    base="$SCRATCH/$name/base"
    if [ ! -d "$base/.git" ]; then
        mkdir -p "$SCRATCH/$name"
        git clone --depth 1 "https://github.com/$slug.git" "$base"
    fi
    LOC_OF[$name]=0; FILES_OF[$name]=0
    case "$LOC_TOOL" in
        scc) json="$(scc --format json --no-cocomo "$base" 2>/dev/null || true)" ;;
        cloc) json="$(cloc --json --quiet "$base" 2>/dev/null || true)" ;;
        *) json="" ;;
    esac
    if [ -n "$json" ]; then
        read -r loc files < <(python3 -I "$REPORT_PY" loc <<<"$json")
        LOC_OF[$name]="$loc"; FILES_OF[$name]="$files"
    fi
}

prepare_workdir() { # repo variant run -> path on stdout
    local repo="$1" variant="$2" run="$3" work seed
    work="$SCRATCH/$repo/$variant-r$run"
    if [ ! -d "$work" ]; then
        cp -a "$SCRATCH/$repo/base" "$work"
        # Fallback and scoped runs start from the stage-0 context of the
        # full variant's first run, so the stage-1 comparison is like for like.
        seed="$SCRATCH/$repo/full-r1/.agent"
        if [ "$variant" != full ] && [ -d "$seed" ]; then
            rm -rf "$work/.agent"
            cp -a "$seed" "$work/.agent"
            rm -rf "$work/.agent/system/bootstrap-research.md" "$work/.agent/tasks/bootstrap"
            touch "$work/.bench-context-seeded"
        fi
    fi
    echo "$work"
}

run_session() { # repo variant stage run
    local repo="$1" variant="$2" stage="$3" run="$4"
    local row cls stack model scope workdir turns timeout_s tag stream start rc wall seeded kind line usd
    row="$(repo_row "$repo")"; IFS='|' read -r _ _ cls stack _ <<<"$row"
    model="$(stage_model "$stage" "$variant")"
    scope="$(scope_for "$repo" "$variant")"
    turns="$(stage_turns "$stage")"; timeout_s="$(stage_timeout "$stage")"
    workdir="$(prepare_workdir "$repo" "$variant" "$run")"
    tag="$repo-$variant-r$run-stage$stage"
    stream="$SCRATCH/streams/$tag.out"
    seeded=0; [ -f "$workdir/.bench-context-seeded" ] && seeded=1

    if [ "$stage" = 2 ] && [ ! -f "$workdir/.agent/system/bootstrap-research.md" ]; then
        echo "bootstrap-cost: skipping $tag: no research brief in $workdir (run stage 1 first)" >&2
        return 0
    fi

    build_cmd "$stage" "$model" "$workdir"
    echo "[$((RAN + 1))/$TOTAL] $tag ($model)" >&2
    start=$(date +%s)
    set +e
    if [ "$stage" = 0 ]; then
        "${CMD[@]}" >"$stream" 2>"$stream.err"
        kind=pilot
    else
        render_prompt "$stage" "$turns" "$scope" >"$SCRATCH/prompts/$tag.md"
        (cd "$workdir" && "${CMD[@]}" <"$SCRATCH/prompts/$tag.md" >"$stream" 2>"$stream.err")
        kind=claude
    fi
    rc=$?
    set -e
    wall=$(( $(date +%s) - start ))

    line="$(python3 -I "$REPORT_PY" summarize --kind "$kind" --stream "$stream" --workdir "$workdir" \
        --clone "$SCRATCH/$repo/base" --repo "$repo" --class "$cls" --stack "$stack" \
        --loc "${LOC_OF[$repo]:-0}" --files "${FILES_OF[$repo]:-0}" \
        --stage "$stage" --variant "$variant" --run "$run" --model "$model" --effort "$EFFORT" \
        --turn-cap "$turns" --timeout-s "$timeout_s" --wall-s "$wall" --exit-code "$rc" \
        --scope "$scope" --context-seeded "$seeded" --date "$TODAY")"
    printf '%s\n' "$line" >>"$OUT"
    usd="$(python3 -I "$REPORT_PY" usd --prices "$PRICES_JSON" <<<"$line")"
    MATRIX_USD="$(awk -v a="$MATRIX_USD" -v b="$usd" 'BEGIN { printf "%.6f", a + b }')"
    RAN=$((RAN + 1))
}

for want in "${REPO_LIST[@]}"; do
    row="$(repo_row "$want")"; IFS='|' read -r name _ _ _ _ <<<"$row"
    prepare_repo "$name"
done

for entry in "${SESSIONS[@]}"; do
    if awk -v t="$MATRIX_USD" -v l="$MATRIX_MAX_USD" 'BEGIN { exit !(t >= l) }'; then
        printf '{"type":"matrix_stop","usd_total":%s,"limit_usd":%s,"sessions_run":%d,"sessions_planned":%d,"date":"%s"}\n' \
            "$MATRIX_USD" "$MATRIX_MAX_USD" "$RAN" "$TOTAL" "$TODAY" >>"$OUT"
        echo "bootstrap-cost: matrix guard hit at \$$MATRIX_USD (limit \$$MATRIX_MAX_USD) after $RAN of $TOTAL sessions" >&2
        break
    fi
    IFS='|' read -r repo variant stage run <<<"$entry"
    run_session "$repo" "$variant" "$stage" "$run"
done

echo "bootstrap-cost: $RAN sessions, \$$MATRIX_USD reported, $(( $(date +%s) - run_start )) s; results in $OUT" >&2
echo "bootstrap-cost: report with: make bench-bootstrap-report" >&2
