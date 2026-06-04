#!/bin/bash

# Run `brew install tmux` to install tmux
#
# Usage:
#   ./scripts/start-gateway-dev.sh [--operator] [--session NAME] [--window NAME] [--no-attach] [--no-build]
#   ./scripts/start-gateway-dev.sh operator                                   # legacy positional form
#
# Standalone:                ./scripts/start-gateway-dev.sh --operator
#   → creates session 'gateway-dev' with one 4-pane window and attaches.
#
# Called by another script:  ./scripts/start-gateway-dev.sh --session studio --window avs --operator --no-attach
#   → if 'studio' session exists, adds an 'avs' window with the 4 panes; if not, creates 'studio' and uses its
#     first window. Returns without attaching so the caller controls the session.
#
# Panes in the AVS window (always 4):
#   0 (top-left)     Gateway       — REST :8080, gRPC :2206
#   1 (bottom-left)  Operator      — dials gateway on :2206  (only with --operator)
#   2 (top-right)    Worker Sepolia       — :50051
#   3 (bottom-right) Worker Base Sepolia  — :50052
#
# Bundlers: SEPOLIA_BUNDLER_URL + BASE_SEPOLIA_BUNDLER_URL are sourced from
# ./.env.local and exported into the tmux session so the YAMLs' ${VAR}
# placeholders resolve. Pull them from Railway:
#   railway link --service gateway
#   railway variables --kv | grep -E '^(SEPOLIA|BASE_SEPOLIA)_BUNDLER_URL=' > .env.local

set -e

SESSION_NAME="gateway-dev"
WINDOW_NAME=""           # empty → don't rename the first window of a new session
START_OPERATOR=false
ATTACH=true
BUILD=true

while [[ $# -gt 0 ]]; do
    case "$1" in
        --session)    SESSION_NAME="$2"; shift 2 ;;
        --window)     WINDOW_NAME="$2"; shift 2 ;;
        --operator|-o) START_OPERATOR=true; shift ;;
        --no-attach)  ATTACH=false; shift ;;
        --no-build)   BUILD=false; shift ;;
        operator)     START_OPERATOR=true; shift ;;   # backwards-compat with old positional form
        -h|--help)
            sed -n '3,28p' "$0" | sed 's/^# //; s/^#//'
            exit 0
            ;;
        *) echo "❌ Unknown argument: $1"; exit 1 ;;
    esac
done

command -v tmux >/dev/null 2>&1 || { echo "❌ tmux not found. Run: brew install tmux"; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Source bundler URLs from .env.local (so YAML ${VAR} expansion resolves).
if [[ -f "$PROJECT_DIR/.env.local" ]]; then
    set -a
    # shellcheck disable=SC1090
    source "$PROJECT_DIR/.env.local"
    set +a
fi

if [[ -z "$SEPOLIA_BUNDLER_URL" || -z "$BASE_SEPOLIA_BUNDLER_URL" ]]; then
    echo "❌ Missing bundler URLs. Set in $PROJECT_DIR/.env.local:"
    echo "     SEPOLIA_BUNDLER_URL=https://..."
    echo "     BASE_SEPOLIA_BUNDLER_URL=https://..."
    echo ""
    echo "   Pull from Railway:"
    echo "     railway link --service gateway"
    echo "     railway variables --kv | grep -E '^(SEPOLIA|BASE_SEPOLIA)_BUNDLER_URL=' > .env.local"
    exit 1
fi

# Build up-front so tmux isn't created with broken binaries.
if [[ "$BUILD" == "true" ]]; then
    echo "Building binary..."
    (cd "$PROJECT_DIR" && make build)
    echo ""
fi

# Session: reuse if it exists (caller-managed); otherwise create.
if tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
    EXISTING_SESSION=true
else
    EXISTING_SESSION=false
    tmux new-session -d -s "$SESSION_NAME" -c "$PROJECT_DIR"
fi

# Window: when standalone, use the session's first window. When attaching to an
# existing session, always create a new window so we don't clobber the caller's
# panes.
if [[ "$EXISTING_SESSION" == "false" ]]; then
    if [[ -n "$WINDOW_NAME" ]]; then
        tmux rename-window -t "$SESSION_NAME:0" "$WINDOW_NAME"
        WIN="$SESSION_NAME:$WINDOW_NAME"
    else
        WIN="$SESSION_NAME:0"
    fi
else
    TARGET_WIN="${WINDOW_NAME:-avs}"
    # `-t "$SESSION_NAME"` (no colon) is interpreted by tmux as "the window
    # whose name matches $SESSION_NAME" — which clashes with the caller's
    # first window. Trailing colon + `-a` means "session-level target, insert
    # after current window" so the new index is always free.
    tmux new-window -a -t "$SESSION_NAME:" -n "$TARGET_WIN" -c "$PROJECT_DIR"
    WIN="$SESSION_NAME:$TARGET_WIN"
fi

# Bundler vars need to live on the session so panes inherit them (the binary
# reads ${VAR} from its own env, not from the YAML).
tmux set-environment -t "$SESSION_NAME" SEPOLIA_BUNDLER_URL "$SEPOLIA_BUNDLER_URL"
tmux set-environment -t "$SESSION_NAME" BASE_SEPOLIA_BUNDLER_URL "$BASE_SEPOLIA_BUNDLER_URL"

# 4-pane layout in the AVS window:
# +-----------------+-----------------+
# | 0 Gateway       | 2 Worker Sepolia|
# +-----------------+-----------------+
# | 1 Operator      | 3 Worker B-Sep  |
# +-----------------+-----------------+
tmux split-window -t "$WIN" -h -p 50 -c "$PROJECT_DIR"
tmux select-pane  -t "$WIN.0"
tmux split-window -t "$WIN" -v -p 50 -c "$PROJECT_DIR"
tmux select-pane  -t "$WIN.2"
tmux split-window -t "$WIN" -v -p 50 -c "$PROJECT_DIR"

tmux send-keys -t "$WIN.0" "./out/ap aggregator --config=config/gateway-dev.yaml 2>&1 | tee gateway-dev.log" Enter

if [[ "$START_OPERATOR" == "true" ]]; then
    tmux send-keys -t "$WIN.1" "./out/ap operator --config=config/operator-sepolia.yaml 2>&1 | tee operator-dev.log" Enter
else
    tmux send-keys -t "$WIN.1" "echo 'Operator not started. Re-run with --operator (or pass operator as the first positional arg).'" Enter
fi

tmux send-keys -t "$WIN.2" "./out/ap worker --config=config/worker-sepolia-dev.yaml 2>&1 | tee worker-sepolia-dev.log" Enter
tmux send-keys -t "$WIN.3" "./out/ap worker --config=config/worker-base-sepolia-dev.yaml 2>&1 | tee worker-base-sepolia-dev.log" Enter

tmux select-pane -t "$WIN.0"

echo "Gateway:             http://localhost:8080  (REST), localhost:2206 (gRPC)"
echo "Worker Sepolia:      localhost:50051"
echo "Worker Base-Sepolia: localhost:50052"
echo "Bundler (Sepolia):   ${SEPOLIA_BUNDLER_URL%%\?*}"
echo "Bundler (Base-Sep):  ${BASE_SEPOLIA_BUNDLER_URL%%\?*}"
echo ""

if [[ "$ATTACH" == "true" ]]; then
    echo "Tmux session '$SESSION_NAME' started"
    echo "  Attach:  tmux attach -t $SESSION_NAME"
    echo "  Detach:  Ctrl-B then D"
    echo "  Kill:    tmux kill-session -t $SESSION_NAME"
    echo ""
    tmux attach-session -t "$SESSION_NAME"
fi
