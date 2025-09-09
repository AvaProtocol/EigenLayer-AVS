#!/bin/bash

# Script to search for recent user operations in EntryPoint events
# Usage: ./scripts/search_recent_userops.sh [sender_address] [blocks_back]

set -e

# Configuration
SEPOLIA_RPC="${SEPOLIA_RPC:-https://sepolia.infura.io/v3/YOUR_KEY}"
ENTRYPOINT_ADDRESS="0x0000000071727De22E5E9d8BAf0edAc6f37da032"
USER_OP_EVENT_SIGNATURE="0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

print_usage() {
    echo "Usage: $0 [sender_address] [blocks_back]"
    echo ""
    echo "Examples:"
    echo "  $0                                          # Show all recent UserOperationEvents"
    echo "  $0 0x1234...5678                          # Filter by sender address"
    echo "  $0 \"\" 100                                  # Search last 100 blocks"
    echo "  $0 0x1234...5678 50                       # Filter by sender in last 50 blocks"
    echo ""
    echo "Environment variables:"
    echo "  SEPOLIA_RPC - Sepolia RPC endpoint (required)"
}

# Parse arguments
SENDER_FILTER="$1"
BLOCKS_BACK="${2:-20}"

# Validate SEPOLIA_RPC is set and not the placeholder
if [[ "$SEPOLIA_RPC" == *"YOUR_KEY"* ]]; then
    echo -e "${RED}Error: SEPOLIA_RPC environment variable must be set to a valid RPC endpoint${NC}"
    echo "Example: export SEPOLIA_RPC=https://sepolia.infura.io/v3/YOUR_ACTUAL_KEY"
    exit 1
fi

echo -e "${BLUE}=== Searching Recent User Operations ===${NC}"
echo -e "${BLUE}RPC Endpoint: ${SEPOLIA_RPC}${NC}"
echo -e "${BLUE}EntryPoint: ${ENTRYPOINT_ADDRESS}${NC}"
echo -e "${BLUE}Blocks to search: ${BLOCKS_BACK}${NC}"
if [[ -n "$SENDER_FILTER" ]]; then
    echo -e "${BLUE}Sender filter: ${SENDER_FILTER}${NC}"
fi
echo ""

# Function to make RPC call
make_rpc_call() {
    local method="$1"
    local params="$2"
    
    local response=$(curl -s -X POST "$SEPOLIA_RPC" \
        -H "Content-Type: application/json" \
        -d "{
            \"jsonrpc\": \"2.0\",
            \"method\": \"$method\",
            \"params\": [$params],
            \"id\": 1
        }")
    
    if echo "$response" | jq empty 2>/dev/null; then
        echo "$response"
    else
        echo -e "${RED}Non-JSON response from RPC:${NC}" >&2
        echo "$response" >&2
        return 1
    fi
}

# Get latest block
echo -e "${YELLOW}Getting latest block...${NC}"
latest_response=$(make_rpc_call "eth_blockNumber" "")
if ! echo "$latest_response" | jq -e '.result' > /dev/null; then
    echo -e "${RED}Failed to get latest block${NC}"
    exit 1
fi

latest_block_hex=$(echo "$latest_response" | jq -r '.result')
latest_block_dec=$((latest_block_hex))
from_block_dec=$((latest_block_dec - BLOCKS_BACK))
from_block_hex=$(printf "0x%x" $from_block_dec)

echo -e "${GREEN}Latest block: $latest_block_dec ($latest_block_hex)${NC}"
echo -e "${GREEN}Searching from block: $from_block_dec ($from_block_hex)${NC}"
echo ""

# Build topics array for filtering
topics_json="[\"$USER_OP_EVENT_SIGNATURE\""
if [[ -n "$SENDER_FILTER" ]]; then
    # Pad sender address to 32 bytes for topic filtering
    sender_padded="0x000000000000000000000000${SENDER_FILTER#0x}"
    topics_json="$topics_json, null, \"$sender_padded\""
fi
topics_json="$topics_json]"

# Get logs
echo -e "${YELLOW}Fetching UserOperationEvent logs...${NC}"
logs_response=$(make_rpc_call "eth_getLogs" "{
    \"address\": \"$ENTRYPOINT_ADDRESS\",
    \"topics\": $topics_json,
    \"fromBlock\": \"$from_block_hex\",
    \"toBlock\": \"latest\"
}")

if ! echo "$logs_response" | jq -e '.result' > /dev/null; then
    echo -e "${RED}Failed to fetch logs:${NC}"
    echo "$logs_response" | jq '.error // .'
    exit 1
fi

# Process and display results
logs=$(echo "$logs_response" | jq -r '.result')
log_count=$(echo "$logs" | jq 'length')

echo -e "${GREEN}Found $log_count UserOperationEvent(s)${NC}"
echo ""

if [[ "$log_count" == "0" ]]; then
    echo -e "${YELLOW}No user operations found in the specified range${NC}"
    if [[ -n "$SENDER_FILTER" ]]; then
        echo -e "${YELLOW}Try without sender filter or increase block range${NC}"
    fi
    exit 0
fi

# Display each user operation
echo "$logs" | jq -r '.[] | @base64' | while read -r encoded_log; do
    log=$(echo "$encoded_log" | base64 -d)
    
    # Extract basic info
    block_number_hex=$(echo "$log" | jq -r '.blockNumber')
    block_number_dec=$((block_number_hex))
    tx_hash=$(echo "$log" | jq -r '.transactionHash')
    
    # Extract topics (user op hash and sender)
    user_op_hash=$(echo "$log" | jq -r '.topics[1]')
    sender_topic=$(echo "$log" | jq -r '.topics[2]')
    sender_address="0x${sender_topic: -40}"  # Last 20 bytes
    
    # Extract data (contains success, paymaster, etc.)
    data=$(echo "$log" | jq -r '.data')
    
    echo -e "${BLUE}━━━ User Operation ━━━${NC}"
    echo -e "${GREEN}User Op Hash:${NC} $user_op_hash"
    echo -e "${GREEN}Sender:${NC} $sender_address"
    echo -e "${GREEN}Block:${NC} $block_number_dec ($block_number_hex)"
    echo -e "${GREEN}TX Hash:${NC} $tx_hash"
    
    # Try to decode some data fields (this is simplified)
    if [[ ${#data} -gt 2 ]]; then
        # Extract success (first 32 bytes after 0x)
        success_hex="0x${data:2:64}"
        if [[ "$success_hex" == "0x0000000000000000000000000000000000000000000000000000000000000001" ]]; then
            echo -e "${GREEN}Status:${NC} ✓ Success"
        else
            echo -e "${RED}Status:${NC} ✗ Failed"
        fi
    fi
    
    echo -e "${GREEN}Etherscan:${NC} https://sepolia.etherscan.io/tx/$tx_hash"
    echo ""
done

echo -e "${BLUE}Search completed${NC}"
