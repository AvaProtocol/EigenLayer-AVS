#!/bin/bash

# Smart Wallet Management Tool
# Diagnostic tool for paymaster and bundler troubleshooting
#
# Usage:
#   ./aa-wallet-toolkit.sh <mode> [options]
#
# Modes:
#   clear-mempool      Clear stuck UserOps from bundler
#   verify-paymaster   Verify paymaster configuration
#   all                Run all diagnostic steps
#
# Options:
#   --config PATH      Path to aggregator config (default: config/aggregator-sepolia.yaml)
#
# Examples:
#   # Run full diagnostic
#   ./aa-wallet-toolkit.sh all
#
#   # Clear stuck UserOps
#   ./aa-wallet-toolkit.sh clear-mempool
#
#   # Verify paymaster configuration
#   ./aa-wallet-toolkit.sh verify-paymaster

set -e

# Get script directory
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd )"

# Default values
MODE="${1:-verify-paymaster}"
shift || true  # Remove mode from args, ignore if no args

# Change to project root
cd "$PROJECT_ROOT"

# Run the Go script with all arguments
exec go run scripts/aa-wallet-toolkit.go --mode "$MODE" "$@"

