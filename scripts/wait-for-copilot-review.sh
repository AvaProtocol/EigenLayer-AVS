#!/bin/bash

# Wait for Copilot to complete its review on a GitHub PR
# Usage: sh scripts/wait-for-copilot-review.sh <pr_number> [timeout_seconds]
# Default timeout: 600 seconds (10 minutes)

if [ -z "$1" ]; then
    echo "Error: PR number is required" >&2
    echo "Usage: $0 <pr_number> [timeout_seconds]" >&2
    exit 1
fi

PR_NUMBER="$1"
MAX_WAIT="${2:-600}"
POLL_INTERVAL=30
REPO="AvaProtocol/studio"
BOT_LOGIN="copilot-pull-request-reviewer[bot]"

if ! [[ "$PR_NUMBER" =~ ^[0-9]+$ ]]; then
    echo "Error: PR number must be a positive integer" >&2
    exit 1
fi

echo "Waiting for Copilot review on PR #$PR_NUMBER (timeout: ${MAX_WAIT}s)..."

START=$(date +%s)
while true; do
    ELAPSED=$(($(date +%s) - START))
    if [ $ELAPSED -gt $MAX_WAIT ]; then
        echo "Timeout after ${MAX_WAIT}s. Copilot review may still be in progress."
        exit 1
    fi

    # Get the latest Copilot review state using gh's built-in --jq to avoid
    # control character parse errors that occur when piping to external jq
    STATE=$(gh api "repos/$REPO/pulls/$PR_NUMBER/reviews" \
        --jq "[.[] | select(.user.login == \"$BOT_LOGIN\")] | sort_by(.submitted_at) | last | .state" 2>/dev/null)

    if [ -n "$STATE" ] && [ "$STATE" != "null" ]; then

        case "$STATE" in
            COMMENTED|APPROVED|CHANGES_REQUESTED)
                echo "Copilot review complete! State: $STATE (after ${ELAPSED}s)"
                exit 0
                ;;
            PENDING)
                echo "  [${ELAPSED}s] Copilot is still analyzing..."
                ;;
            *)
                echo "  [${ELAPSED}s] Unexpected state: $STATE"
                ;;
        esac
    else
        echo "  [${ELAPSED}s] No Copilot review found yet..."
    fi

    # Check if Copilot is still in requested_reviewers (review hasn't started yet)
    REQUESTED=$(gh api "repos/$REPO/pulls/$PR_NUMBER/requested_reviewers" \
        --jq '[.users[] | select(.login == "Copilot")] | length' 2>/dev/null)
    if [ "$REQUESTED" = "0" ] && [ -z "$STATE" -o "$STATE" = "null" ]; then
        echo "  [${ELAPSED}s] Warning: Copilot is not in requested reviewers and has no review."
    fi

    sleep $POLL_INTERVAL
done
