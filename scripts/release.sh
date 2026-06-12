#!/bin/bash

# Simple wrapper script for promoting and publishing releases
# Usage: ./release.sh

echo "🚀 EigenLayer AVS Release & Docker Publishing Script"
echo "=================================================="
echo

# Check if we're in the right directory
if [ ! -f ".github/scripts/promote-and-publish.sh" ]; then
    echo "❌ Error: This script must be run from the repository root"
    echo "   Expected to find: .github/scripts/promote-and-publish.sh"
    exit 1
fi

# Auto-load .env if present so RAILWAY_API_TOKEN (and any other release
# secrets) reach the child script without a manual `source .env`. set -a
# exports every assignment that follows; the matching set +a turns it
# back off so we don't pollute the rest of the shell. Only the
# repo-root .env is sourced — not .env.local or contracts/.env, which
# are scoped to other tooling.
if [ -f ".env" ]; then
    set -a
    # shellcheck disable=SC1091
    source .env
    set +a
fi

# Run the main script
exec .github/scripts/promote-and-publish.sh
