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

# Run the main script
exec .github/scripts/promote-and-publish.sh
