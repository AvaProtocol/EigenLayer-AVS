#!/bin/bash

# Script to fetch the newest comments from Copilot and Claude AI bots from a GitHub PR
# Usage: sh scripts/get-copilot-comments.sh <pr_number> [verbose]
# Set verbose to see all comment authors (useful for identifying bot names)
# Example: sh scripts/get-copilot-comments.sh 987 verbose

if [ -z "$1" ]; then
    echo "Error: PR number is required"
    echo "Usage: sh scripts/get-copilot-comments.sh <pr_number> [verbose]"
    exit 1
fi

PR_NUMBER="$1"
REPO_OWNER="AvaProtocol"
REPO_NAME="EigenLayer-AVS"
REPO="$REPO_OWNER/$REPO_NAME"
VERBOSE="${2:-false}"
OUTPUT_FILE="eigenlayer-avs-pr-comments-${PR_NUMBER}.json"

echo "Fetching newest Copilot and Claude comments from PR #$PR_NUMBER..."
echo "Repository: $REPO"
echo ""

# If verbose, show all comment authors to help identify bot names
if [ "$VERBOSE" = "verbose" ] || [ "$VERBOSE" = "-v" ] || [ "$VERBOSE" = "--verbose" ]; then
    echo "=== All Review Comment Authors ==="
    gh api repos/$REPO/pulls/$PR_NUMBER/comments --jq '.[] | {user: .user.login, body: (.body[:50] + "...")}'
    echo ""
    echo "=== All Issue/PR Comment Authors ==="
    gh api repos/$REPO/issues/$PR_NUMBER/comments --jq '.[] | {user: .user.login, body: (.body[:50] + "...")}'
    echo ""
fi

# Create temporary files to avoid bash variable issues with special characters
TMP_GRAPHQL=$(mktemp)
TMP_COPILOT_REVIEW=$(mktemp)
TMP_CLAUDE_REVIEW=$(mktemp)
TMP_COPILOT_ISSUE=$(mktemp)
TMP_CLAUDE_ISSUE=$(mktemp)

# Use GraphQL to fetch all review threads with resolution status
gh api graphql -f query='
query($owner: String!, $name: String!, $pr: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $pr) {
      reviewThreads(first: 100) {
        nodes {
          isResolved
          comments(first: 10) {
            nodes {
              id
              path
              body
              createdAt
              author {
                login
              }
              line
              diffHunk
            }
          }
        }
      }
    }
  }
}' -f owner="$REPO_OWNER" -f name="$REPO_NAME" -F pr="$PR_NUMBER" > "$TMP_GRAPHQL"

# Extract all Copilot review comments (both resolved and unresolved)
# Note: Copilot appears as "Copilot" (capital C) in review comments
jq '[
  .data.repository.pullRequest.reviewThreads.nodes[] |
  .comments.nodes[] |
  select(.author.login == "github-copilot[bot]" or .author.login == "copilot" or .author.login == "Copilot" or .author.login == "copilot-pull-request-reviewer" or (.author.login | ascii_downcase | contains("copilot"))) |
  {
    id: .id,
    path: .path,
    line: .line,
    diff_hunk: .diffHunk,
    body: .body,
    created_at: .createdAt,
    user: .author.login
  }
] | sort_by(.created_at)' "$TMP_GRAPHQL" > "$TMP_COPILOT_REVIEW"

# Extract all Claude review comments (both resolved and unresolved)
jq '[
  .data.repository.pullRequest.reviewThreads.nodes[] |
  .comments.nodes[] |
  select(.author.login == "claude[bot]" or .author.login == "claude-code[bot]" or .author.login == "Claude" or (.author.login | ascii_downcase | contains("claude"))) |
  {
    id: .id,
    path: .path,
    line: .line,
    diff_hunk: .diffHunk,
    body: .body,
    created_at: .createdAt,
    user: .author.login
  }
] | sort_by(.created_at)' "$TMP_GRAPHQL" > "$TMP_CLAUDE_REVIEW"

# Fetch issue/PR comments and get all Copilot comments
# Note: Copilot typically doesn't post issue comments, but check anyway
gh api repos/$REPO/issues/$PR_NUMBER/comments --jq '[.[] | select(.user.login == "github-copilot[bot]" or .user.login == "copilot" or .user.login == "Copilot" or .user.login == "copilot-pull-request-reviewer" or (.user.login | ascii_downcase | contains("copilot"))) | {
  id: .id,
  body: .body,
  created_at: .created_at,
  user: .user.login
}] | sort_by(.created_at)' > "$TMP_COPILOT_ISSUE"

# Fetch issue/PR comments and get all Claude comments
gh api repos/$REPO/issues/$PR_NUMBER/comments --jq '[.[] | select(.user.login == "claude[bot]" or .user.login == "claude-code[bot]" or .user.login == "Claude" or (.user.login | ascii_downcase | contains("claude"))) | {
  id: .id,
  body: .body,
  created_at: .created_at,
  user: .user.login
}] | sort_by(.created_at)' > "$TMP_CLAUDE_ISSUE"

# Combine into a single JSON structure
jq -n \
  --argfile copilot_review "$TMP_COPILOT_REVIEW" \
  --argfile claude_review "$TMP_CLAUDE_REVIEW" \
  --argfile copilot_issue "$TMP_COPILOT_ISSUE" \
  --argfile claude_issue "$TMP_CLAUDE_ISSUE" \
  --arg pr_number "$PR_NUMBER" \
  --arg repo "$REPO" \
  '
    {
      pr_number: $pr_number,
      repository: $repo,
      copilot: {
        review_comments: (if ($copilot_review | type) == "array" then $copilot_review else [] end),
        issue_comments: (if ($copilot_issue | type) == "array" then $copilot_issue else [] end)
      },
      claude: {
        review_comments: (if ($claude_review | type) == "array" then $claude_review else [] end),
        issue_comments: (if ($claude_issue | type) == "array" then $claude_issue else [] end)
      }
    }
  ' > "$OUTPUT_FILE"

# Clean up temp files
rm -f "$TMP_GRAPHQL" "$TMP_COPILOT_REVIEW" "$TMP_CLAUDE_REVIEW" "$TMP_COPILOT_ISSUE" "$TMP_CLAUDE_ISSUE"

# Print summary to stdout
echo "=== Summary ==="

# Helper function to convert UTC timestamp to Pacific time
convert_to_pacific() {
  local utc_timestamp="$1"
  if [ -z "$utc_timestamp" ] || [ "$utc_timestamp" = "null" ]; then
    echo "None"
    return
  fi
  
  # Use Python for reliable timezone conversion (works on both macOS and Linux)
  # Format: 2026-01-08T21:41:54Z -> 2026-01-08 13:41:54 PST/PDT
  # Try zoneinfo first (Python 3.9+), fallback to pytz, then to dateutil
  python3 -c "
from datetime import datetime
import sys

try:
    # Parse UTC timestamp
    utc_str = '$utc_timestamp'.replace('Z', '+00:00')
    utc_dt = datetime.fromisoformat(utc_str)
    
    # Try zoneinfo (Python 3.9+)
    try:
        from zoneinfo import ZoneInfo
        pacific_dt = utc_dt.astimezone(ZoneInfo('America/Los_Angeles'))
        print(pacific_dt.strftime('%Y-%m-%d %H:%M:%S %Z'))
    except ImportError:
        # Fallback to pytz
        try:
            import pytz
            utc_dt = utc_dt.replace(tzinfo=pytz.UTC)
            pacific = pytz.timezone('America/Los_Angeles')
            pacific_dt = utc_dt.astimezone(pacific)
            print(pacific_dt.strftime('%Y-%m-%d %H:%M:%S %Z'))
        except ImportError:
            # Fallback to dateutil
            try:
                from dateutil import tz
                pacific = tz.gettz('America/Los_Angeles')
                utc_dt = utc_dt.replace(tzinfo=tz.UTC)
                pacific_dt = utc_dt.astimezone(pacific)
                print(pacific_dt.strftime('%Y-%m-%d %H:%M:%S %Z'))
            except ImportError:
                # Last resort: manual calculation (PST is UTC-8, PDT is UTC-7)
                # This is approximate and doesn't handle DST perfectly
                hour = int(utc_str[11:13])
                if hour >= 8:
                    hour_pst = hour - 8
                else:
                    hour_pst = hour + 16
                print('$utc_timestamp'.replace('T', ' ').replace('Z', ' PST'))
except Exception as e:
    # Fallback: return original timestamp if conversion fails
    print('$utc_timestamp')
" 2>/dev/null || echo "$utc_timestamp"
}

COPILOT_REVIEW_COUNT=$(jq '.copilot.review_comments | length' "$OUTPUT_FILE")
COPILOT_ISSUE_COUNT=$(jq '.copilot.issue_comments | length' "$OUTPUT_FILE")
CLAUDE_REVIEW_COUNT=$(jq '.claude.review_comments | length' "$OUTPUT_FILE")
CLAUDE_ISSUE_COUNT=$(jq '.claude.issue_comments | length' "$OUTPUT_FILE")

echo "Copilot:"
if [ "$COPILOT_REVIEW_COUNT" -gt 0 ]; then
  echo "  - Review comments: $COPILOT_REVIEW_COUNT found"
  jq -r '.copilot.review_comments[] | "    • \(.path):\(.line) - \(.created_at)"' "$OUTPUT_FILE"
else
  echo "  - Review comments: None"
fi
if [ "$COPILOT_ISSUE_COUNT" -gt 0 ]; then
  echo "  - Issue/PR comments: $COPILOT_ISSUE_COUNT found"
  jq -r '.copilot.issue_comments[] | "    • \(.created_at)"' "$OUTPUT_FILE"
else
  echo "  - Issue/PR comments: None"
fi
echo "Claude:"
if [ "$CLAUDE_REVIEW_COUNT" -gt 0 ]; then
  echo "  - Review comments: $CLAUDE_REVIEW_COUNT found"
  jq -r '.claude.review_comments[] | "    • \(.path):\(.line) - \(.created_at)"' "$OUTPUT_FILE"
else
  echo "  - Review comments: None"
fi
if [ "$CLAUDE_ISSUE_COUNT" -gt 0 ]; then
  echo "  - Issue/PR comments: $CLAUDE_ISSUE_COUNT found"
  jq -r '.claude.issue_comments[] | "    • \(.created_at)"' "$OUTPUT_FILE"
else
  echo "  - Issue/PR comments: None"
fi
echo ""
echo "Output saved to: $OUTPUT_FILE"

