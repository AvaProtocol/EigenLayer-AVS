#!/bin/bash

# Script to fetch Copilot AI comments from a GitHub PR
# Usage: sh scripts/get-copilot-comments.sh [pr_number] [verbose]
# Default PR: 987
# Set verbose to see all comment authors (useful for identifying bot names)
# Example: sh scripts/get-copilot-comments.sh 987 verbose

PR_NUMBER="${1:-987}"
REPO="AvaProtocol/EigenLayer-AVS"
VERBOSE="${2:-false}"
OUTPUT_FILE="pr-comments-${PR_NUMBER}.json"

echo "Fetching Copilot comments from PR #$PR_NUMBER..."
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
TMP_REVIEW=$(mktemp)
TMP_ISSUE=$(mktemp)

# Fetch review comments and save to temp file
gh api repos/$REPO/pulls/$PR_NUMBER/comments --jq '[.[] | select(.user.login == "github-copilot[bot]" or .user.login == "copilot" or .user.login == "Copilot" or (.user.login | ascii_downcase | contains("copilot"))) | {
  id: .id,
  path: .path,
  line: .line,
  diff_hunk: .diff_hunk,
  body: .body,
  created_at: .created_at,
  user: .user.login
}]' > "$TMP_REVIEW"

# Fetch issue/PR comments and save to temp file
gh api repos/$REPO/issues/$PR_NUMBER/comments --jq '[.[] | select(.user.login == "github-copilot[bot]" or .user.login == "copilot" or .user.login == "Copilot" or (.user.login | ascii_downcase | contains("copilot"))) | {
  id: .id,
  body: .body,
  created_at: .created_at,
  user: .user.login
}]' > "$TMP_ISSUE"

# Combine all comments into a single JSON structure using temp files
jq -n \
  --argfile review_comments "$TMP_REVIEW" \
  --argfile issue_comments "$TMP_ISSUE" \
  --arg pr_number "$PR_NUMBER" \
  --arg repo "$REPO" \
  '
    ($review_comments) as $review |
    ($issue_comments) as $issue |
    ($review | length) as $review_count |
    ($issue | length) as $issue_count |
    ($review_count + $issue_count) as $total |
    {
      pr_number: $pr_number,
      repository: $repo,
      summary: {
        total_comments: $total,
        review_comments: $review_count,
        issue_pr_comments: $issue_count
      },
      review_comments: $review,
      issue_pr_comments: $issue
    }
  ' > "$OUTPUT_FILE"

# Get counts for summary display
REVIEW_COUNT=$(jq '.summary.review_comments' "$OUTPUT_FILE")
ISSUE_COUNT=$(jq '.summary.issue_pr_comments' "$OUTPUT_FILE")
TOTAL=$(jq '.summary.total_comments' "$OUTPUT_FILE")

# Clean up temp files
rm -f "$TMP_REVIEW" "$TMP_ISSUE"

# Print summary to stdout
echo "=== Summary ==="
echo "Total Copilot comments found: $TOTAL"
echo "  - Review comments (inline): $REVIEW_COUNT"
echo "  - Issue/PR comments (general): $ISSUE_COUNT"
echo ""
echo "Output saved to: $OUTPUT_FILE"

