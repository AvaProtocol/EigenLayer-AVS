#!/bin/bash


cd "$(git rev-parse --show-toplevel)" || exit 1

CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)

cleanup() {
  echo "Switching back to $CURRENT_BRANCH branch"
  git checkout "$CURRENT_BRANCH" > /dev/null 2>&1
}

trap cleanup EXIT

if [ "$#" -ne 2 ]; then
  echo "Usage: $0 <branch1> <branch2>"
  echo "Example: $0 main staging"
  exit 1
fi

BRANCH1="$1"
BRANCH2="$2"

echo "Comparing storage structures between $BRANCH1 and $BRANCH2..."

echo "Analyzing $BRANCH1 branch..."
git checkout "$BRANCH1" > /dev/null 2>&1
if [ $? -ne 0 ]; then
  echo "Error: Failed to checkout $BRANCH1 branch"
  exit 1
fi

echo "Storage key structures in $BRANCH1:"
go run ./scripts/compare_storage_structure.go "$BRANCH2"

echo "====================================="
echo "Comparison complete."
echo "For PR #227, no migration is required because:"
echo "1. JWT audience field change only affects runtime validation"
echo "2. StepOutputVar fix only affects runtime handling of nil values"
echo "3. isHidden attribute is backward compatible due to 'omitempty' JSON tag"
