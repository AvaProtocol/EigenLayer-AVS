#!/bin/bash

# Script to query user operation status from bundler RPC
# Usage: ./scripts/query_userop.sh <user_op_hash> [status|receipt]

set -e

# Configuration
BUNDLER_RPC="${SEPOLIA_BUNDLER_RPC:-https://bundler-sepolia.avaprotocol.org/rpc?apikey=kt8qTj8MtmAQGsj17Urz1ySn4R}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

print_usage() {
    echo "Usage: $0 <user_op_hash> [status|receipt|both]"
    echo ""
    echo "Examples:"
    echo "  $0 0x1234567890abcdef... status    # Get user operation details"
    echo "  $0 0x1234567890abcdef... receipt   # Get user operation receipt"
    echo "  $0 0x1234567890abcdef... both      # Get both status and receipt"
    echo "  $0 0x1234567890abcdef...           # Default: both"
    echo ""
    echo "Environment variables:"
    echo "  SEPOLIA_BUNDLER_RPC - Override default bundler RPC URL"
}

# Validate arguments
if [ $# -lt 1 ] || [ $# -gt 2 ]; then
    print_usage
    exit 1
fi

USER_OP_HASH="$1"
QUERY_TYPE="${2:-both}"

# Validate hash format
if [[ ! "$USER_OP_HASH" =~ ^0x[a-fA-F0-9]{64}$ ]]; then
    echo -e "${RED}Error: Invalid user operation hash format. Expected 64 character hex string with 0x prefix${NC}"
    exit 1
fi

# Validate query type
if [[ ! "$QUERY_TYPE" =~ ^(status|receipt|both)$ ]]; then
    echo -e "${RED}Error: Invalid query type. Must be 'status', 'receipt', or 'both'${NC}"
    exit 1
fi

echo -e "${BLUE}Querying user operation: ${USER_OP_HASH}${NC}"
echo -e "${BLUE}Bundler RPC: ${BUNDLER_RPC}${NC}"
echo ""

# Function to make RPC call
make_rpc_call() {
    local method="$1"
    local params="$2"
    
    local response=$(curl -s -X POST "$BUNDLER_RPC" \
        -H "Content-Type: application/json" \
        -d "{
            \"jsonrpc\": \"2.0\",
            \"method\": \"$method\",
            \"params\": [$params],
            \"id\": 1
        }")
    
    # Check if response is valid JSON
    if echo "$response" | jq empty 2>/dev/null; then
        echo "$response" | jq '.'
    else
        echo -e "${RED}Non-JSON response from bundler:${NC}" >&2
        echo "$response" >&2
        echo '{"error": {"code": -1, "message": "Invalid response from bundler"}}'
    fi
}

# Function to format and display user operation status
show_user_op_status() {
    echo -e "${YELLOW}=== User Operation Status ===${NC}"
    local result=$(make_rpc_call "eth_getUserOperationByHash" "\"$USER_OP_HASH\"")
    
    if echo "$result" | jq -e '.error' > /dev/null; then
        echo -e "${RED}Error:${NC}"
        echo "$result" | jq '.error'
        return 1
    elif echo "$result" | jq -e '.result == null' > /dev/null; then
        echo -e "${YELLOW}User operation not found or not yet processed${NC}"
        return 1
    else
        echo -e "${GREEN}Found user operation:${NC}"
        echo "$result" | jq '.result'
        return 0
    fi
}

# Function to format and display user operation receipt
show_user_op_receipt() {
    echo -e "${YELLOW}=== User Operation Receipt ===${NC}"
    local result=$(make_rpc_call "eth_getUserOperationReceipt" "\"$USER_OP_HASH\"")
    
    if echo "$result" | jq -e '.error' > /dev/null; then
        echo -e "${RED}Error:${NC}"
        echo "$result" | jq '.error'
        return 1
    elif echo "$result" | jq -e '.result == null' > /dev/null; then
        echo -e "${YELLOW}Receipt not found - user operation may still be pending${NC}"
        return 1
    else
        echo -e "${GREEN}User operation receipt:${NC}"
        echo "$result" | jq '.result'
        
        # Extract and highlight key information
        local success=$(echo "$result" | jq -r '.result.success // "unknown"')
        local block_hash=$(echo "$result" | jq -r '.result.receipt.blockHash // "unknown"')
        local block_number=$(echo "$result" | jq -r '.result.receipt.blockNumber // "unknown"')
        local gas_used=$(echo "$result" | jq -r '.result.receipt.gasUsed // "unknown"')
        
        echo ""
        echo -e "${BLUE}Summary:${NC}"
        echo -e "  Success: ${success}"
        echo -e "  Block Hash: ${block_hash}"
        echo -e "  Block Number: ${block_number}"
        echo -e "  Gas Used: ${gas_used}"
        return 0
    fi
}

# Execute based on query type
case "$QUERY_TYPE" in
    "status")
        show_user_op_status
        ;;
    "receipt")
        show_user_op_receipt
        ;;
    "both")
        show_user_op_status
        echo ""
        show_user_op_receipt
        ;;
esac

echo ""
echo -e "${BLUE}Query completed${NC}"
