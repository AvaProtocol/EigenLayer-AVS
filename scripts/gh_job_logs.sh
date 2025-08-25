#!/bin/bash

# GitHub Job Log Fetcher
# Fetches logs from GitHub Actions jobs for debugging purposes
# Usage: ./scripts/gh_job_logs.sh --job-id <JOB_ID>
#        ./scripts/gh_job_logs.sh --run-id <RUN_ID> [--job-name <JOB_NAME>]

set -euo pipefail

# Default values
JOB_ID=""
RUN_ID=""
JOB_NAME=""
REPO="AvaProtocol/EigenLayer-AVS"

# Parse command line arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --job-id)
      JOB_ID="$2"
      shift 2
      ;;
    --run-id)
      RUN_ID="$2"
      shift 2
      ;;
    --job-name)
      JOB_NAME="$2"
      shift 2
      ;;
    --repo)
      REPO="$2"
      shift 2
      ;;
    -h|--help)
      echo "Usage: $0 --job-id <JOB_ID>"
      echo "       $0 --run-id <RUN_ID> [--job-name <JOB_NAME>]"
      echo ""
      echo "Options:"
      echo "  --job-id <ID>     Fetch logs for specific job ID"
      echo "  --run-id <ID>     Fetch logs for run ID (requires job-name or will list jobs)"
      echo "  --job-name <NAME> Specific job name within run"
      echo "  --repo <REPO>     Repository (default: AvaProtocol/EigenLayer-AVS)"
      echo "  -h, --help        Show this help message"
      exit 0
      ;;
    *)
      echo "Unknown option $1"
      exit 1
      ;;
  esac
done

# Validate inputs
if [[ -z "$JOB_ID" && -z "$RUN_ID" ]]; then
  echo "Error: Must provide either --job-id or --run-id"
  echo "Use --help for usage information"
  exit 1
fi

# If run-id provided but no job-id, need to resolve job-id
if [[ -n "$RUN_ID" && -z "$JOB_ID" ]]; then
  echo "Fetching jobs for run $RUN_ID..."
  
  if [[ -n "$JOB_NAME" ]]; then
    # Try to find job by name
    JOB_ID=$(gh run view "$RUN_ID" --repo "$REPO" --json jobs --jq ".jobs[] | select(.name == \"$JOB_NAME\") | .databaseId" || echo "")
    if [[ -z "$JOB_ID" ]]; then
      echo "Error: Job '$JOB_NAME' not found in run $RUN_ID"
      echo "Available jobs:"
      gh run view "$RUN_ID" --repo "$REPO" --json jobs --jq '.jobs[] | "  - \(.name) (ID: \(.databaseId))"'
      exit 1
    fi
  else
    # List available jobs and exit
    echo "Available jobs in run $RUN_ID:"
    gh run view "$RUN_ID" --repo "$REPO" --json jobs --jq '.jobs[] | "  - \(.name) (ID: \(.databaseId))"'
    echo ""
    echo "Use: $0 --job-id <JOB_ID> to fetch logs for a specific job"
    exit 0
  fi
fi

# Generate output filename
TS=$(date -u +%Y%m%dT%H%M%SZ)
OUTFILE="job_${JOB_ID}_${TS}.log"

echo "Fetching logs for job $JOB_ID..."
echo "Repository: $REPO"
echo "Output file: $OUTFILE"

# Fetch the job logs
if gh run view --log --job="$JOB_ID" --repo "$REPO" > "$OUTFILE"; then
  echo "✅ Logs saved to: $OUTFILE"
  echo "📊 Log file size: $(wc -l < "$OUTFILE") lines"
  
  # Show quick summary of test failures if any
  if grep -q -- "--- FAIL:" "$OUTFILE" 2>/dev/null; then
    echo ""
    echo "🚨 Test Failures Found:"
    grep -n -- "--- FAIL:" "$OUTFILE" | head -20
    if [[ $(grep -c -- "--- FAIL:" "$OUTFILE") -gt 20 ]]; then
      echo "... and $(( $(grep -c -- "--- FAIL:" "$OUTFILE") - 20 )) more test failures"
    fi
  elif grep -q "FAIL\|ERROR\|error:" "$OUTFILE" 2>/dev/null; then
    echo ""
    echo "🚨 Found failures/errors in logs:"
    grep -n "FAIL\|ERROR\|error:" "$OUTFILE" | head -5
    if [[ $(grep -c "FAIL\|ERROR\|error:" "$OUTFILE") -gt 5 ]]; then
      echo "... and $(( $(grep -c "FAIL\|ERROR\|error:" "$OUTFILE") - 5 )) more"
    fi
  fi
else
  echo "❌ Failed to fetch logs for job $JOB_ID"
  exit 1
fi
