#!/usr/bin/env bash
#
# Fetch Sentry issues for the eigenlayer-avs project.
#
# Usage:
#   ./scripts/get-sentry-issues.sh                          # List recent unresolved issues
#   ./scripts/get-sentry-issues.sh list [resolved|all]      # List issues by status
#   ./scripts/get-sentry-issues.sh <ISSUE_ID>               # Fetch latest event details
#   ./scripts/get-sentry-issues.sh <ISSUE_ID> <EVENT_INDEX> # Fetch specific event
#   ./scripts/get-sentry-issues.sh <ISSUE_ID> resolve <COMMIT> # Resolve by commit
#
# Env (in .env at repo root):
#   SENTRY_AUTH_TOKEN  - Sentry API bearer token (required)
#   SENTRY_ORG         - Sentry organization slug (default: ava-protocol-public)
#   SENTRY_PROJECT     - Sentry project slug (default: eigenlayer-avs)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SENTRY_BASE="https://sentry.io/api/0"

# Load .env
if [[ -f "$REPO_ROOT/.env" ]]; then
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%%#*}"             # strip comments
    line="$(echo "$line" | xargs)" # trim whitespace
    [[ -z "$line" ]] && continue
    [[ "$line" != *=* ]] && continue
    key="${line%%=*}"
    val="${line#*=}"
    # Remove surrounding quotes
    val="${val%\"}"
    val="${val#\"}"
    val="${val%\'}"
    val="${val#\'}"
    export "$key=$val" 2>/dev/null || true
  done < "$REPO_ROOT/.env"
fi

SENTRY_AUTH_TOKEN="${SENTRY_AUTH_TOKEN:-}"
SENTRY_ORG="${SENTRY_ORG:-ava-protocol}"
SENTRY_PROJECT="${SENTRY_PROJECT:-eigenlayer-avs}"

if [[ -z "$SENTRY_AUTH_TOKEN" ]]; then
  echo "Missing SENTRY_AUTH_TOKEN in .env"
  echo ""
  echo "Add to .env:"
  echo "  SENTRY_AUTH_TOKEN=your_token_here"
  echo ""
  echo "Get one at: https://sentry.io/settings/account/api/auth-tokens/"
  exit 1
fi

# Check for jq
if ! command -v jq &>/dev/null; then
  echo "jq is required. Install with: brew install jq"
  exit 1
fi

sentry_get() {
  curl -fsS -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
       -H "Content-Type: application/json" \
       "$SENTRY_BASE$1"
}

sentry_put() {
  curl -fsS -X PUT \
       -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
       -H "Content-Type: application/json" \
       -d "$2" \
       "$SENTRY_BASE$1"
}

list_issues() {
  local query="$1"
  local encoded_query
  encoded_query=$(python3 -c "import urllib.parse; print(urllib.parse.quote('$query'))")

  local url="/projects/$SENTRY_ORG/$SENTRY_PROJECT/issues/?query=$encoded_query&sort=date&limit=25"
  local response
  response=$(sentry_get "$url")

  local count
  count=$(echo "$response" | jq 'length')

  if [[ "$count" == "0" ]] || [[ "$count" == "null" ]]; then
    echo "No issues found."
    return
  fi

  echo ""
  echo "=== Recent Issues ($count) ==="
  echo ""
  printf "%-12s %-8s %-8s %-22s %s\n" "ID" "Level" "Events" "Last Seen" "Title"
  echo "------------------------------------------------------------------------------------------------------------"

  echo "$response" | jq -r '.[] | "\(.id)\t\(.level // "-")\t\(.count // 0)\t\(.lastSeen // "-" | .[0:21])\t\(.title // .culprit // "-")"' | \
  while IFS=$'\t' read -r id level count lastSeen title; do
    printf "%-12s %-8s %-8s %-22s %s\n" "$id" "$level" "$count" "$lastSeen" "$title"
  done
}

fetch_issue_details() {
  local issue_id="$1"
  local event_index="${2:-0}"

  local url="/issues/$issue_id/events/?full=true"
  local response
  response=$(sentry_get "$url")

  local count
  count=$(echo "$response" | jq 'length')

  if [[ "$count" == "0" ]] || [[ "$count" == "null" ]]; then
    echo "No events found for this issue."
    return
  fi

  # Select event by index
  local event
  event=$(echo "$response" | jq ".[$event_index] // .[0]")

  # Header
  echo ""
  echo "=== Event ==="
  echo "$event" | jq -r '"event_id: \(.eventID // .id // "-")"'
  echo "$event" | jq -r '"timestamp: \(.dateCreated // .timestamp // .datetime // "-")"'
  echo "$event" | jq -r '"environment: \(.environment // "-")"'
  echo "$event" | jq -r '"release: \(.release // .releaseId // "-")"'
  echo "$event" | jq -r '"platform: \(.platform // "-")"'
  echo "$event" | jq -r '"transaction: \(.transaction // "-")"'
  echo "$event" | jq -r '"server_name: \(.contexts.server_name // "-")"'
  echo "$event" | jq -r '"trace_id: \(.contexts.trace.trace_id // .contexts.trace.traceId // "-")"'

  # Exception
  local exception
  exception=$(echo "$event" | jq '[.entries[] | select(.type == "exception")] | .[0].data.values[0] // empty')
  if [[ -n "$exception" ]]; then
    echo ""
    echo "=== Exception ==="
    echo "$exception" | jq -r '"type: \(.type // "-")"'
    echo "$exception" | jq -r '"value: \(.value // "-")"'
    local mechanism
    mechanism=$(echo "$exception" | jq '.mechanism // empty')
    if [[ -n "$mechanism" ]]; then
      echo "mechanism: $mechanism"
    fi
  fi

  # Tags
  local tag_count
  tag_count=$(echo "$event" | jq '.tags | length')
  if [[ "$tag_count" -gt 0 ]]; then
    echo ""
    echo "=== Tags ($tag_count) ==="
    echo "$event" | jq -r '.tags[] | "\(.key): \(.value)"'
  fi

  # Contexts
  echo ""
  echo "=== Contexts ==="
  echo "$event" | jq -r '.contexts | to_entries[] | select(.key == "runtime" or .key == "os" or .key == "device") | "\(.key): \(.value)"'

  # Breadcrumbs
  local breadcrumbs
  breadcrumbs=$(echo "$event" | jq '[.entries[] | select(.type == "breadcrumbs")] | .[0].data.values // []')
  local bc_count
  bc_count=$(echo "$breadcrumbs" | jq 'length')
  if [[ "$bc_count" -gt 0 ]]; then
    echo ""
    echo "=== Breadcrumbs ($bc_count) ==="
    echo "$breadcrumbs" | jq -r '.[] | "\(.timestamp // "") [\(.category // .type // "")] \(if .level then "[\(.level)]" else "" end) \(.message // "")"'
  fi

  # Stack frames
  local frames
  frames=$(echo "$event" | jq '[.entries[] | select(.type == "exception")] | .[0].data.values[0].stacktrace.frames // []')
  local frame_count
  frame_count=$(echo "$frames" | jq 'length')
  if [[ "$frame_count" -gt 0 ]]; then
    echo ""
    echo "=== Stack Frames ($frame_count) ==="
    echo "$frames" | jq -r '.[] | "\(.filename // .abs_path // "?"):\(.lineno // "?"):\(.colno // "?") in \(.function // "<anonymous>")\(if .in_app then " in_app" else "" end)\(if .module then " module=\(.module)" else "" end)"'
  fi
}

resolve_issue() {
  local issue_id="$1"
  local commit="$2"

  if [[ -z "$commit" ]]; then
    echo "Usage: $0 <ISSUE_ID> resolve <COMMIT_HASH>"
    exit 1
  fi

  local body
  body=$(jq -cn --arg commit "$commit" '{"status":"resolved","statusDetails":{"inCommit":{"commit":$commit,"repository":"AvaProtocol/EigenLayer-AVS"}}}')

  local response
  response=$(sentry_put "/issues/$issue_id/" "$body")

  local status
  status=$(echo "$response" | jq -r '.status // "unknown"')

  echo "Resolved issue $issue_id by commit $commit. (status: $status)"
}

# --- Main ---
CMD="${1:-list}"
LOG_LABEL="list"

case "$CMD" in
  list)
    status="${2:-}"
    case "$status" in
      resolved) query="is:resolved" ;;
      all)      query="" ;;
      *)        query="is:unresolved" ;;
    esac
    list_issues "$query"
    ;;
  [0-9]*)
    ISSUE_ID="$1"
    LOG_LABEL="$ISSUE_ID"
    MODE="${2:-}"

    case "$MODE" in
      resolve)
        resolve_issue "$ISSUE_ID" "${3:-}"
        ;;
      [0-9]*)
        fetch_issue_details "$ISSUE_ID" "$MODE"
        ;;
      *)
        fetch_issue_details "$ISSUE_ID" 0
        ;;
    esac
    ;;
  *)
    echo "Usage:"
    echo "  $0                          # List recent unresolved issues"
    echo "  $0 list [resolved|all]      # List issues by status"
    echo "  $0 <ISSUE_ID>               # Fetch latest event details"
    echo "  $0 <ISSUE_ID> <EVENT_INDEX> # Fetch specific event"
    echo "  $0 <ISSUE_ID> resolve <COMMIT> # Resolve by commit"
    exit 1
    ;;
esac
