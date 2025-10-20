#!/bin/bash

# Clear Bundler Mempool
# Checks for stuck UserOps and clears them using debug_bundler_clearState
#
# Usage:
#   ./clear-staging-mempool.sh [network]
#
# Networks: sepolia (default), base, base-sepolia, ethereum
#
# Requirements:
#   - For staging: SSH tunnel must be active
#     ssh -i ~/.ssh/id_rsa -L 4437:localhost:4437 -N ap-staging1

set -e

# Get network from argument or default to sepolia
NETWORK="${1:-sepolia}"
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd )"
CONFIG_FILE="$PROJECT_ROOT/config/aggregator-$NETWORK.yaml"

# Check if config file exists
if [ ! -f "$CONFIG_FILE" ]; then
    echo "❌ Config file not found: $CONFIG_FILE"
    echo ""
    echo "Available networks:"
    ls -1 "$PROJECT_ROOT/config/aggregator-"*.yaml 2>/dev/null | sed 's/.*aggregator-/  - /' | sed 's/\.yaml$//' || echo "  (none found)"
    exit 1
fi

# Extract bundler URL from config (skip commented lines)
BUNDLER_URL=$(grep "bundler_url:" "$CONFIG_FILE" | grep -v "^#" | grep -v "^[[:space:]]*#" | head -1 | awk '{print $2}' | tr -d '"' | tr -d "'")

if [ -z "$BUNDLER_URL" ]; then
    echo "❌ Could not find bundler_url in $CONFIG_FILE"
    exit 1
fi

ENTRYPOINT="0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"

echo "🧹 Bundler Mempool Manager"
echo "=========================="
echo ""
echo "Network: $NETWORK"
echo "Config: $CONFIG_FILE"
echo "Bundler: $BUNDLER_URL"
echo "EntryPoint: $ENTRYPOINT"
echo ""

# Check if bundler is reachable
echo "📡 Checking bundler connectivity..."
SUPPORTED_ENTRYPOINTS=$(curl -s -X POST $BUNDLER_URL \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "eth_supportedEntryPoints",
    "params": [],
    "id": 1
  }' | jq -r '.result[]' 2>/dev/null)

if [ -z "$SUPPORTED_ENTRYPOINTS" ]; then
    echo "❌ Cannot reach bundler at $BUNDLER_URL"
    echo ""
    echo "Make sure SSH tunnel is active:"
    echo "  ssh -i ~/.ssh/id_rsa -L 4437:localhost:4437 -N ap-staging1"
    exit 1
fi

echo "✅ Bundler is reachable"
echo "   Supported EntryPoints:"
echo "$SUPPORTED_ENTRYPOINTS" | sed 's/^/     /'
echo ""

# Check mempool
echo "🔍 Checking mempool for stuck UserOps..."
MEMPOOL_RESPONSE=$(curl -s -X POST $BUNDLER_URL \
  -H "Content-Type: application/json" \
  -d "{
    \"jsonrpc\": \"2.0\",
    \"method\": \"debug_bundler_dumpMempool\",
    \"params\": [\"$ENTRYPOINT\"],
    \"id\": 1
  }")

# Check if debug methods are enabled
if echo "$MEMPOOL_RESPONSE" | jq -e '.error' > /dev/null 2>&1; then
    ERROR_MSG=$(echo "$MEMPOOL_RESPONSE" | jq -r '.error.message')
    echo "⚠️  Debug methods not available: $ERROR_MSG"
    echo ""
    echo "The bundler may not have debug RPC methods enabled."
    echo "You may need to restart the bundler with --debug flag."
    exit 1
fi

# Parse mempool
USEROP_COUNT=$(echo "$MEMPOOL_RESPONSE" | jq -r '.result | length' 2>/dev/null || echo "0")

if [ "$USEROP_COUNT" = "0" ] || [ "$USEROP_COUNT" = "null" ]; then
    echo "✅ Mempool is empty - no stuck UserOps"
    echo ""
    echo "If withdrawal tests are still failing, the issue is elsewhere."
    exit 0
fi

echo "⚠️  Found $USEROP_COUNT stuck UserOp(s) in mempool"
echo ""

# Show details of stuck UserOps
echo "📋 Stuck UserOps Details:"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

for i in $(seq 0 $((USEROP_COUNT - 1))); do
    SENDER=$(echo "$MEMPOOL_RESPONSE" | jq -r ".result[$i].sender" 2>/dev/null)
    NONCE=$(echo "$MEMPOOL_RESPONSE" | jq -r ".result[$i].nonce" 2>/dev/null)
    INIT_CODE=$(echo "$MEMPOOL_RESPONSE" | jq -r ".result[$i].initCode" 2>/dev/null)
    PAYMASTER=$(echo "$MEMPOOL_RESPONSE" | jq -r ".result[$i].paymasterAndData" 2>/dev/null)
    
    # Convert hex nonce to decimal
    NONCE_DEC=$(printf "%d" "$NONCE" 2>/dev/null || echo "unknown")
    
    HAS_INIT_CODE="false"
    if [ "$INIT_CODE" != "0x" ] && [ "$INIT_CODE" != "" ] && [ "$INIT_CODE" != "null" ]; then
        HAS_INIT_CODE="true"
    fi
    
    HAS_PAYMASTER="false"
    if [ "$PAYMASTER" != "0x" ] && [ "$PAYMASTER" != "" ] && [ "$PAYMASTER" != "null" ]; then
        HAS_PAYMASTER="true"
    fi
    
    echo "[$((i + 1))] Sender: $SENDER"
    echo "    Nonce: $NONCE (decimal: $NONCE_DEC)"
    echo "    Has InitCode: $HAS_INIT_CODE"
    echo "    Has Paymaster: $HAS_PAYMASTER"
    echo ""
done

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

echo "🗑️  Clearing bundler state to drop stuck UserOps..."

CLEAR_RESPONSE=$(curl -s -X POST $BUNDLER_URL \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "debug_bundler_clearState",
    "params": [],
    "id": 1
  }')

# Check if it succeeded
if echo "$CLEAR_RESPONSE" | jq -e '.error' > /dev/null 2>&1; then
    ERROR_MSG=$(echo "$CLEAR_RESPONSE" | jq -r '.error.message')
    echo "❌ Failed to clear state: $ERROR_MSG"
    echo ""
    echo "If debug methods are disabled, you may need to restart the bundler:"
    echo "  ssh ap-staging1 'docker restart voltaire-bundler'"
    exit 1
fi

RESULT=$(echo "$CLEAR_RESPONSE" | jq -r '.result' 2>/dev/null)
echo "✅ State cleared: $RESULT"

# Wait a moment for state to clear
echo ""
echo "⏳ Waiting 2 seconds..."
sleep 2

# Check mempool again
echo ""
echo "🔍 Re-checking mempool..."
MEMPOOL_AFTER=$(curl -s -X POST $BUNDLER_URL \
  -H "Content-Type: application/json" \
  -d "{
    \"jsonrpc\": \"2.0\",
    \"method\": \"debug_bundler_dumpMempool\",
    \"params\": [\"$ENTRYPOINT\"],
    \"id\": 1
  }")

USEROP_COUNT_AFTER=$(echo "$MEMPOOL_AFTER" | jq -r '.result | length' 2>/dev/null || echo "0")

echo ""
if [ "$USEROP_COUNT_AFTER" = "0" ] || [ "$USEROP_COUNT_AFTER" = "null" ]; then
    echo "✅ Success! Mempool is now empty"
else
    echo "⚠️  Mempool still has $USEROP_COUNT_AFTER UserOp(s)"
    echo ""
    if [ "$USEROP_COUNT_AFTER" -lt "$USEROP_COUNT" ]; then
        echo "Progress: Reduced from $USEROP_COUNT to $USEROP_COUNT_AFTER"
        echo "Some UserOps may need funding or have other issues."
    else
        echo "No change. UserOps may be invalid or need funding."
    fi
    echo ""
    echo "Check bundler logs on staging server for details:"
    echo "  ssh ap-staging1 'docker logs voltaire-bundler --tail 100'"
fi

