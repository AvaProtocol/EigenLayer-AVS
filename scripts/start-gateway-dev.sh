#!/bin/bash

# Run `brew install tmux` to install tmux
# Usage: ./scripts/start-gateway-dev.sh
#
# Starts the gateway + worker architecture locally for development:
#   - Gateway (single process, replaces per-chain aggregators)
#   - Worker: Sepolia (chain 11155111)
#   - Worker: Base Sepolia (chain 84532)
#   - Operator (optional, connects to gateway)
#
# This replaces the per-chain aggregator setup. Instead of running
# `make aggregator-sepolia` and `make aggregator-base-sepolia` separately,
# a single gateway routes by chain_id to per-chain workers.
#
# Prerequisites:
#   - Go toolchain (for building)
#   - tmux

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

START_OPERATOR=${1:-false}

echo "Starting gateway + worker dev environment"
echo "  Gateway:            localhost:2206 (gRPC), localhost:8080 (HTTP)"
echo "  Worker Sepolia:     localhost:50051 (internal gRPC)"
echo "  Worker Base Sepolia: localhost:50052 (internal gRPC)"
if [[ "$START_OPERATOR" == "operator" ]]; then
    echo "  Operator:           connects to gateway at localhost:2206"
fi
echo ""

SESSION_NAME="gateway-dev"

# Kill any existing session first
tmux kill-session -t $SESSION_NAME 2>/dev/null || true

# Build first
echo "Building binary..."
cd "$PROJECT_DIR" && make build
echo ""

# Create tmux session
tmux new-session -d -s $SESSION_NAME -c "$PROJECT_DIR"

# Layout:
# +-----------------------+-----------------------+
# |                       |                       |
# |  Pane 0: Gateway      |  Pane 2: Worker       |
# |                       |       Sepolia         |
# |                       |                       |
# +-----------------------+-----------------------+
# |                       |                       |
# |  Pane 1: Operator     |  Pane 3: Worker       |
# |  (or empty)           |      Base Sepolia     |
# |                       |                       |
# +-----------------------+-----------------------+

# Split horizontally: left 50%, right 50%
tmux split-window -h -p 50 -c "$PROJECT_DIR"

# Split left side vertically: gateway (top), operator (bottom)
tmux select-pane -t 0
tmux split-window -v -p 50 -c "$PROJECT_DIR"

# Split right side vertically: worker-sepolia (top), worker-base-sepolia (bottom)
tmux select-pane -t 2
tmux split-window -v -p 50 -c "$PROJECT_DIR"

# Pane 0: Gateway
tmux send-keys -t 0 "./out/ap aggregator --config=config/gateway-dev.yaml 2>&1 | tee gateway-dev.log" Enter

# Pane 1: Operator (optional)
if [[ "$START_OPERATOR" == "operator" ]]; then
    tmux send-keys -t 1 "./out/ap operator --config=config/operator-sepolia.yaml 2>&1 | tee operator-dev.log" Enter
else
    tmux send-keys -t 1 "echo 'Operator not started. Re-run with: ./scripts/start-gateway-dev.sh operator'" Enter
fi

# Pane 2: Worker Sepolia
tmux send-keys -t 2 "./out/ap worker --config=config/worker-sepolia-dev.yaml 2>&1 | tee worker-sepolia-dev.log" Enter

# Pane 3: Worker Base Sepolia
tmux send-keys -t 3 "./out/ap worker --config=config/worker-base-sepolia-dev.yaml 2>&1 | tee worker-base-sepolia-dev.log" Enter

# Focus on gateway pane
tmux select-pane -t 0

echo "Tmux session '$SESSION_NAME' started"
echo "  Attach with: tmux attach -t $SESSION_NAME"
echo "  Detach with: Ctrl+B then D"
echo "  Kill session: tmux kill-session -t $SESSION_NAME"
echo ""

# Attach to the session
tmux attach-session -t $SESSION_NAME
