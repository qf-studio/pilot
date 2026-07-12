#!/usr/bin/env bash
#
# canary-poll.sh — poll a sandbox repo for pipeline progression on a filed
# canary issue, until a terminal condition is met or the timeout elapses.
#
# TASK-403 A2: extracted from .github/workflows/pilot-canary.yml so a new
# canary scenario can be added by adding a workflow matrix row, without
# touching this script.
#
# Usage:
#   canary-poll.sh --repo OWNER/NAME --issue N --timeout-minutes M \
#                   --poll-interval-seconds S --assert MODE
#
# --assert modes:
#   merged             Terminal when a PR referencing --issue is merged.
#                      (default; the only mode implemented today)
#
#   n-children-merged  Reserved for TASK-403 A3 (epic-lifecycle scenario):
#                      terminal when every child issue of an epic parent
#                      has exactly one merged PR. Not yet implemented —
#                      passing this mode exits 2.
#
# Emits GitHub Actions step-output lines on stdout (append to
# $GITHUB_OUTPUT from the caller):
#   stage=<issue-filed|PR-opened|merged|merge-stalled>
#   pr_number=<n|empty>
#   result=<success|failure>
# All progress/log messages go to stderr. Exits 0 on success, 1 if the
# terminal condition was not reached, 2 on a usage error.

set -uo pipefail

log() {
  echo "$@" >&2
}

usage() {
  log "Usage: $0 --repo OWNER/NAME --issue N --timeout-minutes M --poll-interval-seconds S [--assert MODE]"
  exit 2
}

REPO=""
ISSUE_NUMBER=""
TIMEOUT_MINUTES=""
POLL_INTERVAL_SECONDS=""
ASSERT_MODE="merged"

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
    *)
      log "Unknown argument: $1"
      usage
      ;;
  esac
done

[ -n "$REPO" ] || usage
[ -n "$ISSUE_NUMBER" ] || usage
[ -n "$TIMEOUT_MINUTES" ] || usage
[ -n "$POLL_INTERVAL_SECONDS" ] || usage

if [ "$ASSERT_MODE" != "merged" ]; then
  log "Unsupported --assert mode '$ASSERT_MODE' (only 'merged' is implemented; 'n-children-merged' is reserved for TASK-403 A3)"
  exit 2
fi

DEADLINE=$(( $(date -u +%s) + TIMEOUT_MINUTES * 60 ))
STAGE="issue-filed"
PR_NUMBER=""
RESULT="failure"

while [ "$(date -u +%s)" -lt "$DEADLINE" ]; do
  if [ -z "$PR_NUMBER" ]; then
    # Pilot branches follow `pilot/GH-<issue-number>` (project
    # convention); fall back to a body reference in case the
    # branch-naming convention drifts.
    PR_NUMBER=$(gh pr list \
      --repo "$REPO" \
      --state all \
      --json number,headRefName,body \
      | jq -r --arg n "$ISSUE_NUMBER" \
        '[.[] | select(.headRefName == ("pilot/GH-" + $n) or ((.body // "") | test("#" + $n)))][0].number // empty')

    if [ -n "$PR_NUMBER" ]; then
      STAGE="PR-opened"
      log "Found PR #$PR_NUMBER referencing canary issue #$ISSUE_NUMBER."
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
      log "PR #$PR_NUMBER was closed without merging."
      break
    fi
  fi

  sleep "$POLL_INTERVAL_SECONDS"
done

if [ "$RESULT" != "success" ] && [ "$STAGE" != "merge-stalled" ] && [ -n "$PR_NUMBER" ]; then
  STAGE="merge-stalled"
fi

log "Final stage: $STAGE (result: $RESULT)"
echo "stage=$STAGE"
echo "pr_number=$PR_NUMBER"
echo "result=$RESULT"

if [ "$RESULT" != "success" ]; then
  exit 1
fi
