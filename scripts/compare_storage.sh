#!/bin/bash


cd "$(git rev-parse --show-toplevel)" || exit 1

CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)

cleanup() {
  echo "Switching back to $CURRENT_BRANCH branch"
  git checkout "$CURRENT_BRANCH" > /dev/null 2>&1
}

trap cleanup EXIT

if [ "$#" -ne 2 ]; then
  echo "Usage: $0 <old_branch> <new_branch>"
  echo "Example: $0 main staging"
  exit 1
fi

OLD_BRANCH="$1"
NEW_BRANCH="$2"

echo "Comparing storage structures between $OLD_BRANCH and $NEW_BRANCH..."

git fetch origin "$OLD_BRANCH" > /dev/null 2>&1
if [ $? -ne 0 ]; then
  echo "Error: Failed to fetch $OLD_BRANCH branch"
  exit 1
fi

git fetch origin "$NEW_BRANCH" > /dev/null 2>&1
if [ $? -ne 0 ]; then
  echo "Error: Failed to fetch $NEW_BRANCH branch"
  exit 1
fi

echo "Running storage structure comparison..."
go run ./scripts/compare_storage_structure.go "$OLD_BRANCH" "$NEW_BRANCH"

echo "====================================="
echo "Comparison complete."
echo "Review the analysis above to determine if a migration is needed."
echo "Remember to check for:"
echo "1. Storage key structure changes"
echo "2. Non-backward-compatible data structure changes"
echo "3. Changes that affect how data is serialized/deserialized"
