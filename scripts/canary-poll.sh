#!/usr/bin/env bash
# Poll a sandbox repo for a canary issue's pipeline progression and assert
# it reaches the expected terminal condition (TASK-403 A2/A3).
#
# Contract: callers select a terminal condition via --assert.
#   merged             -- single issue -> single PR -> merged (TASK-403 A2)
#   n-children-merged  -- epic-lifecycle: parent decomposes into child
#                         issues, each child ships its own merged PR, then
#                         the parent auto-closes clean (TASK-403 A3)
#
# Usage:
#   canary-poll.sh --repo OWNER/NAME --issue N \
#     --timeout-minutes M --poll-interval-seconds S \
#     --assert merged|n-children-merged [--branch-prefix PREFIX]
#
# Requires: gh, jq. Writes stage/pr_number/result to $GITHUB_OUTPUT if set;
# exits non-zero when the terminal condition is not reached in time.

set -uo pipefail

REPO=""
ISSUE_NUMBER=""
TIMEOUT_MINUTES=""
POLL_INTERVAL_SECONDS=""
ASSERT_MODE=""
BRANCH_PREFIX="pilot/GH-"

while [ $# -gt 0 ]; do
  case "$1" in
    --repo)
      REPO="$2"
      shift 2
      ;;
    --issue)
      ISSUE_NUMBER="$2"
      shift 2
      ;;
    --timeout-minutes)
      TIMEOUT_MINUTES="$2"
      shift 2
      ;;
    --poll-interval-seconds)
      POLL_INTERVAL_SECONDS="$2"
      shift 2
      ;;
    --assert)
      ASSERT_MODE="$2"
      shift 2
      ;;
    --branch-prefix)
      BRANCH_PREFIX="$2"
      shift 2
      ;;
    *)
      echo "canary-poll.sh: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [ -z "$REPO" ] || [ -z "$ISSUE_NUMBER" ] || [ -z "$TIMEOUT_MINUTES" ] || [ -z "$POLL_INTERVAL_SECONDS" ] || [ -z "$ASSERT_MODE" ]; then
  echo "canary-poll.sh: --repo, --issue, --timeout-minutes, --poll-interval-seconds, and --assert are all required" >&2
  exit 2
fi

case "$ASSERT_MODE" in
  merged) ;;
  n-children-merged) ;;
  *)
    echo "canary-poll.sh: unsupported --assert mode '$ASSERT_MODE' (supported: merged, n-children-merged)" >&2
    exit 2
    ;;
esac

DEADLINE=$(( $(date -u +%s) + TIMEOUT_MINUTES * 60 ))

write_outputs() {
  # write_outputs writes NAME=VALUE pairs (one per remaining arg, already
  # "name=value" formatted) to $GITHUB_OUTPUT when running under Actions.
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    for pair in "$@"; do
      echo "$pair" >> "$GITHUB_OUTPUT"
    done
  fi
}

join_by() {
  # join_by , a b c -> "a,b,c"
  local sep="$1"
  shift
  local IFS="$sep"
  echo "$*"
}

if [ "$ASSERT_MODE" = "merged" ]; then
  STAGE="issue-filed"
  PR_NUMBER=""
  RESULT="failure"

  while [ "$(date -u +%s)" -lt "$DEADLINE" ]; do
    if [ -z "$PR_NUMBER" ]; then
      # Pilot branches follow `pilot/GH-<issue-number>` (project convention);
      # fall back to a body reference in case the branch-naming convention
      # drifts.
      PR_NUMBER=$(gh pr list \
        --repo "$REPO" \
        --state all \
        --json number,headRefName,body \
        | jq -r --arg n "$ISSUE_NUMBER" --arg prefix "$BRANCH_PREFIX" \
          '[.[] | select(.headRefName == ($prefix + $n) or ((.body // "") | test("#" + $n)))][0].number // empty')

      if [ -n "$PR_NUMBER" ]; then
        STAGE="PR-opened"
        echo "Found PR #$PR_NUMBER referencing canary issue #$ISSUE_NUMBER."
      fi
    else
      PR_JSON=$(gh pr view "$PR_NUMBER" --repo "$REPO" --json state,mergedAt)
      PR_STATE=$(echo "$PR_JSON" | jq -r '.state')
      MERGED_AT=$(echo "$PR_JSON" | jq -r '.mergedAt')

      if [ "$PR_STATE" = "MERGED" ] && [ "$MERGED_AT" != "null" ]; then
        STAGE="merged"
        RESULT="success"
        break
      elif [ "$PR_STATE" = "CLOSED" ]; then
        STAGE="merge-stalled"
        echo "PR #$PR_NUMBER was closed without merging."
        break
      fi
    fi

    sleep "$POLL_INTERVAL_SECONDS"
  done

  if [ "$RESULT" != "success" ] && [ "$STAGE" != "merge-stalled" ] && [ -n "$PR_NUMBER" ]; then
    STAGE="merge-stalled"
  fi

  echo "Final stage: $STAGE (result: $RESULT)"

  write_outputs "stage=$STAGE" "pr_number=$PR_NUMBER" "result=$RESULT"

  if [ "$RESULT" != "success" ]; then
    exit 1
  fi
  exit 0
fi

# --- n-children-merged: epic-lifecycle terminal condition (TASK-403 A3) ---
#
# Children are discovered the same way the executor itself resolves
# parent/child links (internal/executor/epic.go queryRecentSubIssues,
# internal/adapters/github/grouping.go ParseParentIssueNumber): every
# sub-issue body carries a stamped "Parent: GH-<N>" line, searchable via
# GitHub's body search rather than paging every pilot-labeled issue.

find_children() {
  local parent_num="$1"
  gh issue list \
    --repo "$REPO" \
    --state all \
    --search "\"Parent: GH-${parent_num}\" in:body" \
    --json number \
    --limit 100 \
    --jq '.[].number' 2>/dev/null
}

# Wait for the parent to reach a terminal (closed) state, or time out.
while [ "$(date -u +%s)" -lt "$DEADLINE" ]; do
  PARENT_STATE=$(gh issue view "$ISSUE_NUMBER" --repo "$REPO" --json state --jq '.state' 2>/dev/null)
  if [ "$PARENT_STATE" = "CLOSED" ]; then
    break
  fi
  sleep "$POLL_INTERVAL_SECONDS"
done

PARENT_JSON=$(gh issue view "$ISSUE_NUMBER" --repo "$REPO" --json state,labels)
PARENT_STATE=$(echo "$PARENT_JSON" | jq -r '.state')
PARENT_LABELS=$(echo "$PARENT_JSON" | jq -r '[.labels[].name] | join(",")')

contains() {
  # contains NEEDLE HAYSTACK... -- avoids associative arrays (bash 3.2
  # compatible, matching macOS's default /bin/bash as well as CI's bash 5).
  local needle="$1"
  shift
  for item in "$@"; do
    [ "$item" = "$needle" ] && return 0
  done
  return 1
}

# Direct children: issues whose body names this issue as Parent.
DIRECT_CHILDREN=()
while IFS= read -r n; do
  [ -z "$n" ] && continue
  DIRECT_CHILDREN+=("$n")
done < <(find_children "$ISSUE_NUMBER")

# Cascade check: BFS beyond the direct level. Any issue reachable only via
# a child (grandchild+) is an over-decomposition regression -- direct
# children are expected to execute, not decompose further.
#
# Every array expansion below is guarded by a length check first: bash
# before 4.4 (still macOS's default /bin/bash) treats "${arr[@]}" on a
# zero-length array as an unbound-variable error under `set -u`.
ALL_SEEN=()
CASCADE_CHILDREN=()
FRONTIER=()
if [ "${#DIRECT_CHILDREN[@]}" -gt 0 ]; then
  ALL_SEEN=("${DIRECT_CHILDREN[@]}")
  FRONTIER=("${DIRECT_CHILDREN[@]}")
fi
while [ "${#FRONTIER[@]}" -gt 0 ]; do
  NEXT=()
  for n in "${FRONTIER[@]}"; do
    while IFS= read -r kid; do
      [ -z "$kid" ] && continue
      if ! contains "$kid" "${ALL_SEEN[@]}"; then
        ALL_SEEN+=("$kid")
        CASCADE_CHILDREN+=("$kid")
        NEXT+=("$kid")
      fi
    done < <(find_children "$n")
  done
  FRONTIER=()
  if [ "${#NEXT[@]}" -gt 0 ]; then
    FRONTIER=("${NEXT[@]}")
  fi
done

PR_LIST_JSON=$(gh pr list --repo "$REPO" --state all --json number,headRefName,body,mergedAt --limit 100)

UNMERGED_CHILDREN=()
DUPLICATE_CHILDREN=()
if [ "${#DIRECT_CHILDREN[@]}" -gt 0 ]; then
  for child in "${DIRECT_CHILDREN[@]}"; do
    MERGED_COUNT=$(echo "$PR_LIST_JSON" | jq --arg n "$child" --arg prefix "$BRANCH_PREFIX" \
      '[.[] | select((.headRefName == ($prefix + $n)) or ((.body // "") | test("#" + $n + "(\\D|$)"))) | select(.mergedAt != null)] | length')
    if [ "$MERGED_COUNT" -eq 0 ]; then
      UNMERGED_CHILDREN+=("$child")
    elif [ "$MERGED_COUNT" -gt 1 ]; then
      DUPLICATE_CHILDREN+=("$child")
    fi
  done
fi

FAILED=()

CHILD_COUNT=${#DIRECT_CHILDREN[@]}
if [ "$CHILD_COUNT" -ge 2 ]; then
  echo "[PASS] child-count: $CHILD_COUNT child issue(s) created (>= 2)"
else
  echo "[FAIL] child-count: only $CHILD_COUNT child issue(s) created (need >= 2) -- single-child decomposition cascade (Defect A / TASK-401)"
  FAILED+=("child-count")
fi

if [ "${#UNMERGED_CHILDREN[@]}" -eq 0 ] && [ "${#DUPLICATE_CHILDREN[@]}" -eq 0 ]; then
  echo "[PASS] duplicate-pr: every child has exactly one merged PR"
else
  if [ "${#UNMERGED_CHILDREN[@]}" -gt 0 ]; then
    echo "[FAIL] duplicate-pr: child(ren) with no merged PR yet: ${UNMERGED_CHILDREN[*]}"
  fi
  if [ "${#DUPLICATE_CHILDREN[@]}" -gt 0 ]; then
    echo "[FAIL] duplicate-pr: child(ren) with more than one merged PR (Defect B / TASK-402): ${DUPLICATE_CHILDREN[*]}"
  fi
  FAILED+=("duplicate-pr")
fi

if [ "$PARENT_STATE" = "CLOSED" ]; then
  echo "[PASS] parent-closed: parent issue #$ISSUE_NUMBER is closed"
else
  echo "[FAIL] parent-closed: parent issue #$ISSUE_NUMBER is still $PARENT_STATE after ${TIMEOUT_MINUTES}m"
  FAILED+=("parent-closed")
fi

if echo ",$PARENT_LABELS," | grep -q ',pilot-needs-clarification,'; then
  echo "[FAIL] no-clarification-label: closed parent #$ISSUE_NUMBER still carries pilot-needs-clarification (TASK-395 class)"
  FAILED+=("no-clarification-label")
else
  echo "[PASS] no-clarification-label: parent carries no stale pilot-needs-clarification label"
fi

if [ "${#CASCADE_CHILDREN[@]}" -eq 0 ]; then
  echo "[PASS] cascade: no issues spawned beyond the direct children"
else
  echo "[FAIL] cascade: ${#CASCADE_CHILDREN[@]} issue(s) spawned beyond the direct children: ${CASCADE_CHILDREN[*]}"
  FAILED+=("cascade")
fi

if [ "${#FAILED[@]}" -eq 0 ]; then
  RESULT="success"
  STAGE="merged"
else
  RESULT="failure"
  STAGE="assertions-failed"
fi

FAILED_JOINED=""
if [ "${#FAILED[@]}" -gt 0 ]; then
  FAILED_JOINED=$(join_by , "${FAILED[@]}")
fi

echo "Final stage: $STAGE (result: $RESULT)"
echo "Failed assertions: ${FAILED_JOINED:-none}"

write_outputs \
  "stage=$STAGE" \
  "pr_number=" \
  "result=$RESULT" \
  "child_count=$CHILD_COUNT" \
  "failed_assertions=$FAILED_JOINED"

if [ "$RESULT" != "success" ]; then
  exit 1
fi
exit 0
