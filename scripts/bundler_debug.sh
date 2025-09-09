#!/bin/bash

# Script to debug bundler status and connectivity
# Usage: ./scripts/bundler_debug.sh

set -e

# Configuration
BUNDLER_RPC="${SEPOLIA_BUNDLER_RPC:-}"

# Validate required environment variable
if [ -z "$BUNDLER_RPC" ]; then
    echo "Error: SEPOLIA_BUNDLER_RPC environment variable is not set."
    echo "Please set it to your bundler RPC URL with API key."
    echo "Example: export SEPOLIA_BUNDLER_RPC='https://bundler-sepolia.avaprotocol.org/rpc?apikey=YOUR_API_KEY'"
    exit 1
fi

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}=== Bundler Debug Information ===${NC}"
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
        echo -e "${RED}Non-JSON response:${NC}" >&2
        echo "$response" >&2
        echo '{"error": {"code": -1, "message": "Invalid response from bundler"}}'
    fi
}

# Function to test basic connectivity
test_connectivity() {
    echo -e "${YELLOW}=== Testing Basic Connectivity ===${NC}"
    local result=$(make_rpc_call "eth_chainId" "")
    
    if echo "$result" | jq -e '.result' > /dev/null 2>&1; then
        local chain_id=$(echo "$result" | jq -r '.result')
        echo -e "${GREEN}✓ Bundler is responding${NC}"
        echo -e "${GREEN}✓ Chain ID: ${chain_id} ($([ "$chain_id" = "0xaa36a7" ] && echo "Sepolia" || echo "Unknown"))${NC}"
        return 0
    else
        echo -e "${RED}✗ Bundler connectivity failed${NC}"
        echo "$result"
        return 1
    fi
}

# Function to check supported EntryPoints
check_entrypoints() {
    echo -e "${YELLOW}=== Supported EntryPoints ===${NC}"
    local result=$(make_rpc_call "eth_supportedEntryPoints" "")
    
    if echo "$result" | jq -e '.result' > /dev/null 2>&1; then
        echo -e "${GREEN}✓ Supported EntryPoints:${NC}"
        echo "$result" | jq -r '.result[]' | while read -r entry_point; do
            echo -e "  • $entry_point"
        done
        return 0
    else
        echo -e "${RED}✗ Failed to get EntryPoints${NC}"
        echo "$result"
        return 1
    fi
}

# Function to test gas estimation (expected to fail for non-deployed accounts)
test_gas_estimation() {
    echo -e "${YELLOW}=== Testing Gas Estimation ===${NC}"
    echo -e "${BLUE}Note: This test uses a dummy account and is expected to fail with 'AA20 account not deployed'${NC}"
    
    local dummy_userop='{
        "sender": "0x69bb9251D3c2066DcF7aDAFe3E7CE36E76990617",
        "nonce": "0x0",
        "initCode": "0x",
        "callData": "0x",
        "callGasLimit": "0x0",
        "verificationGasLimit": "0x0",
        "preVerificationGas": "0x0",
        "maxFeePerGas": "0x0",
        "maxPriorityFeePerGas": "0x0",
        "paymasterAndData": "0x",
        "signature": "0x"
    }'
    
    local entry_point="0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"
    local result=$(make_rpc_call "eth_estimateUserOperationGas" "$dummy_userop, \"$entry_point\"")
    
    if echo "$result" | jq -e '.error.message' | grep -q "AA20"; then
        echo -e "${GREEN}✓ Gas estimation endpoint is working (expected AA20 error)${NC}"
        return 0
    elif echo "$result" | jq -e '.error' > /dev/null 2>&1; then
        echo -e "${YELLOW}⚠ Gas estimation returned different error:${NC}"
        echo "$result" | jq '.error'
        return 0
    elif echo "$result" | jq -e '.result' > /dev/null 2>&1; then
        echo -e "${GREEN}✓ Gas estimation succeeded (unexpected but good)${NC}"
        echo "$result" | jq '.result'
        return 0
    else
        echo -e "${RED}✗ Gas estimation test failed${NC}"
        echo "$result"
        return 1
    fi
}

# Function to check bundler capabilities
check_bundler_capabilities() {
    echo -e "${YELLOW}=== Bundler Capabilities ===${NC}"
    
    # Test various methods to see what's supported
    local methods=(
        "eth_chainId"
        "eth_supportedEntryPoints" 
        "web3_clientVersion"
        "net_version"
    )
    
    for method in "${methods[@]}"; do
        local result=$(make_rpc_call "$method" "")
        if echo "$result" | jq -e '.result' > /dev/null 2>&1; then
            echo -e "${GREEN}✓ $method: supported${NC}"
            if [ "$method" = "web3_clientVersion" ]; then
                local version=$(echo "$result" | jq -r '.result')
                echo -e "  Version: $version"
            fi
        elif echo "$result" | jq -e '.error.message' | grep -q "not found"; then
            echo -e "${YELLOW}⚠ $method: not supported${NC}"
        else
            echo -e "${RED}✗ $method: error${NC}"
        fi
    done
}

# Function to provide troubleshooting tips
provide_tips() {
    echo ""
    echo -e "${BLUE}=== Troubleshooting Tips ===${NC}"
    echo "If you're experiencing issues with user operations:"
    echo ""
    echo "1. ${YELLOW}RPC Range Limits:${NC}"
    echo "   - The bundler may hit RPC provider limits when querying large block ranges"
    echo "   - Consider using a different RPC provider or increasing limits"
    echo ""
    echo "2. ${YELLOW}Gas Limit Issues:${NC}"
    echo "   - User operations may be skipped if bundle gas limit is reached"
    echo "   - Try submitting operations during less congested periods"
    echo ""
    echo "3. ${YELLOW}Operation Status:${NC}"
    echo "   - Use ./scripts/query_userop.sh <hash> to check specific operations"
    echo "   - Check EntryPoint contract events directly on Etherscan"
    echo ""
    echo "4. ${YELLOW}Common Error Codes:${NC}"
    echo "   - AA20: Account not deployed (normal for new smart wallets)"
    echo "   - AA21: Insufficient funds"
    echo "   - AA13: initCode failed or OOG"
}

# Run all tests
echo "Starting bundler diagnostic tests..."
echo ""

# Track overall status
OVERALL_STATUS=0

if ! test_connectivity; then
    OVERALL_STATUS=1
fi

echo ""

if ! check_entrypoints; then
    OVERALL_STATUS=1
fi

echo ""

test_gas_estimation

echo ""

check_bundler_capabilities

provide_tips

echo ""
if [ $OVERALL_STATUS -eq 0 ]; then
    echo -e "${GREEN}=== Overall Status: HEALTHY ===${NC}"
else
    echo -e "${RED}=== Overall Status: ISSUES DETECTED ===${NC}"
fi
