#!/usr/bin/env bash
# Poll a sandbox repo for a canary issue's pipeline progression and assert
# it reaches the expected terminal condition (TASK-403 A2).
#
# Contract: callers select a terminal condition via --assert. Today only
# `merged` (single issue -> single PR -> merged) is implemented. The flag
# is designed so a future condition such as `n-children-merged` (epic
# scenario, TASK-403 A3) can be added as a new case below without changing
# this script's flags or any caller's invocation of the `merged` mode.
#
# Usage:
#   canary-poll.sh --repo OWNER/NAME --issue N \
#     --timeout-minutes M --poll-interval-seconds S \
#     --assert merged [--branch-prefix PREFIX]
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
  merged)
    ;;
  n-children-merged)
    echo "canary-poll.sh: --assert n-children-merged is reserved but not yet implemented (see TASK-403 A3)" >&2
    exit 2
    ;;
  *)
    echo "canary-poll.sh: unsupported --assert mode '$ASSERT_MODE' (supported: merged)" >&2
    exit 2
    ;;
esac

DEADLINE=$(( $(date -u +%s) + TIMEOUT_MINUTES * 60 ))
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

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "stage=$STAGE"
    echo "pr_number=$PR_NUMBER"
    echo "result=$RESULT"
  } >> "$GITHUB_OUTPUT"
fi

if [ "$RESULT" != "success" ]; then
  exit 1
fi
