#!/bin/bash

# Smart Wallet Management Tool
# All-in-one tool for wallet deployment and troubleshooting
#
# Usage:
#   ./aa-wallet-toolkit.sh <mode> [options]
#
# Modes:
#   check              Check wallet deployment status
#   deploy             Deploy wallet on-chain
#   clear-mempool      Clear stuck UserOps from bundler
#   verify-paymaster   Verify paymaster configuration
#   all                Run all steps in sequence
#
# Options:
#   --config PATH      Path to aggregator config (default: config/aggregator-sepolia.yaml)
#   --salt NUM         Salt value for wallet derivation (default: 0)
#
# Environment Variables:
#   TEST_PRIVATE_KEY   Owner EOA private key (required for check/deploy modes)
#
# Examples:
#   # Run full diagnostic
#   TEST_PRIVATE_KEY=your_key ./aa-wallet-toolkit.sh all
#
#   # Check if wallet exists
#   TEST_PRIVATE_KEY=your_key ./aa-wallet-toolkit.sh check
#
#   # Deploy wallet
#   TEST_PRIVATE_KEY=your_key ./aa-wallet-toolkit.sh deploy
#
#   # Clear stuck UserOps
#   ./aa-wallet-toolkit.sh clear-mempool
#
#   # Verify paymaster configuration
#   ./aa-wallet-toolkit.sh verify-paymaster
#
#   # Check wallet with specific salt
#   TEST_PRIVATE_KEY=your_key ./aa-wallet-toolkit.sh check --salt 1

set -e

# Get script directory
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd )"

# Default values
MODE="${1:-check}"
shift || true  # Remove mode from args, ignore if no args

# Change to project root
cd "$PROJECT_ROOT"

# Run the Go script with all arguments
exec go run scripts/aa-wallet-toolkit.go --mode "$MODE" "$@"

