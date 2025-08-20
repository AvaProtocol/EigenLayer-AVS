#!/usr/bin/env bash

# Fetch GitHub Actions job logs by Job ID, or resolve by Run ID + Job Name
#
# Requirements:
#   - GitHub CLI (gh) authenticated with access to the repo
#
# Usage examples:
#   ./scripts/gh_job_logs.sh --job-id 48456232023
#   ./scripts/gh_job_logs.sh --run-id 17088073264 --job-name "Unit Test (core/migrator)"
#   ./scripts/gh_job_logs.sh --job-id 48456232023 --out custom.log
#
# Notes:
#   - When using --job-id, logs are streamed as plain text.
#   - When using --run-id + --job-name, the script first resolves the Job ID, then downloads logs.

set -euo pipefail

print_usage() {
  cat <<USAGE
Usage:
  $0 --job-id <job_id> [--out <file>]
  $0 --run-id <run_id> --job-name <name> [--out <file>]

Options:
  --job-id      GitHub Actions Job ID
  --run-id      GitHub Actions Run ID (to resolve a job by name)
  --job-name    Job name to resolve within the run
  --out         Optional output file to save logs (defaults to stdout)
  -h, --help    Show this help
USAGE
}

# Hardcoded repository for this repo
REPO="AvaProtocol/EigenLayer-AVS"
JOB_ID=""
RUN_ID=""
JOB_NAME=""
OUTFILE=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --job-id)
      JOB_ID="$2"; shift 2 ;;
    --run-id)
      RUN_ID="$2"; shift 2 ;;
    --job-name)
      JOB_NAME="$2"; shift 2 ;;
    --out)
      OUTFILE="$2"; shift 2 ;;
    -h|--help)
      print_usage; exit 0 ;;
    *)
      echo "Unknown argument: $1" >&2
      print_usage; exit 1 ;;
  esac
done

if ! command -v gh >/dev/null 2>&1; then
  echo "Error: gh (GitHub CLI) not found in PATH" >&2
  exit 1
fi

# Resolve job ID from run-id + job-name if needed
if [[ -z "$JOB_ID" ]]; then
  if [[ -z "$RUN_ID" || -z "$JOB_NAME" ]]; then
    echo "Error: either --job-id or both --run-id and --job-name must be provided" >&2
    print_usage
    exit 1
  fi
  echo "Resolving job id for run ${RUN_ID} and job name '${JOB_NAME}' in ${REPO}..." >&2
  JOB_ID=$(gh api \
    repos/${REPO}/actions/runs/${RUN_ID}/jobs \
    --jq ".jobs[] | select(.name==\"${JOB_NAME}\") | .id" 2>/dev/null || true)
  if [[ -z "$JOB_ID" ]]; then
    echo "Error: Could not resolve job id for job '${JOB_NAME}' in run ${RUN_ID}" >&2
    exit 1
  fi
fi

echo "Downloading logs for job ${JOB_ID} from ${REPO}..." >&2

# Decide output filename automatically if not provided
if [[ -z "$OUTFILE" ]]; then
  TS=$(date -u +%Y%m%dT%H%M%SZ)
  if [[ -n "$JOB_ID" ]]; then
    OUTFILE="job_${JOB_ID}_${TS}.log"
  else
    SAFE_NAME=$(echo "${JOB_NAME}" | tr ' /()' '_' | tr -cs 'A-Za-z0-9_-' '_')
    OUTFILE="run_${RUN_ID}_${SAFE_NAME}_${TS}.log"
  fi
fi

# The job logs endpoint returns a text stream. Save to outfile and echo path.
gh api repos/${REPO}/actions/jobs/${JOB_ID}/logs > "$OUTFILE"
echo "Logs saved to $OUTFILE" >&2


