#!/bin/bash

# UserOp Analysis Script
# Usage: ./analyze-userop.sh <userop-hash-or-tx-hash>
#
# Required environment variables (set in .env file):
# - SEPOLIA_BUNDLER_RPC: Sepolia bundler RPC URL with API key
# - ETHERSCAN_API_KEY: API key for Etherscan API

# Load environment variables from .env file
if [ -f ".env" ]; then
    echo "📁 Loading environment from .env file..."
    export $(grep -v '^#' .env | xargs)
else
    echo "⚠️ Warning: .env file not found in current directory"
fi

if [ $# -eq 0 ]; then
    echo "❌ Error: UserOp hash or transaction hash is required"
    echo "Usage: $0 <userop-hash-or-tx-hash>"
    echo "Example: $0 0xaa6b1771d3ca23a4650a3a3d15dd3ab4351b12a8273e71a1db529b4bd3f71f51"
    exit 1
fi

# Validate required environment variables
if [ -z "$SEPOLIA_BUNDLER_RPC" ]; then
    echo "❌ Error: SEPOLIA_BUNDLER_RPC is required"
    echo "Please set SEPOLIA_BUNDLER_RPC in your .env file"
    echo "Example: SEPOLIA_BUNDLER_RPC=https://bundler-sepolia.avaprotocol.org/rpc?apikey=your-api-key"
    exit 1
fi

if [ -z "$ETHERSCAN_API_KEY" ]; then
    echo "❌ Error: ETHERSCAN_API_KEY is required"
    echo "Please set ETHERSCAN_API_KEY in your .env file"
    exit 1
fi

HASH=$1
BUNDLER_URL="$SEPOLIA_BUNDLER_RPC"
ETHERSCAN_URL="https://api-sepolia.etherscan.io"

echo "🔍 Analyzing UserOp/Transaction: $HASH"
echo "=================================================="
echo "🔧 Configuration:"
echo "  Bundler URL: $BUNDLER_URL"
echo "  Etherscan URL: $ETHERSCAN_URL"
echo "  Etherscan API Key: ${ETHERSCAN_API_KEY:0:10}..." # Show first 10 chars only
echo ""

# Check if it's a UserOp hash or transaction hash by querying bundler first
echo "📡 Step 1: Checking bundler for UserOp status..."
echo "🔍 Bundler request: $BUNDLER_URL"
echo "🔍 Request payload: {\"jsonrpc\":\"2.0\",\"method\":\"eth_getUserOperationByHash\",\"params\":[\"$HASH\"],\"id\":1}"

USEROP_RESPONSE=$(curl -s -X POST "$BUNDLER_URL" \
  -H "Content-Type: application/json" \
  -d "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getUserOperationByHash\",\"params\":[\"$HASH\"],\"id\":1}")

echo "🔍 Raw bundler response: $USEROP_RESPONSE"

if echo "$USEROP_RESPONSE" | grep -q '"result"'; then
    echo "✅ Found UserOp in bundler"
    
    # Extract transaction hash from UserOp result if available
    TX_HASH=$(echo "$USEROP_RESPONSE" | jq -r '.result.transactionHash // empty')
    if [ -n "$TX_HASH" ] && [ "$TX_HASH" != "null" ]; then
        echo "📝 Associated transaction hash: $TX_HASH"
        HASH_TO_ANALYZE=$TX_HASH
    else
        echo "⚠️ UserOp found but no transaction hash (still pending?)"
        HASH_TO_ANALYZE=$HASH
    fi
else
    echo "❌ UserOp not found in bundler, treating as transaction hash"
    HASH_TO_ANALYZE=$HASH
fi

echo ""
echo "📡 Step 2: Analyzing transaction on Etherscan..."
echo "🔍 Etherscan request: ${ETHERSCAN_URL}/api?module=proxy&action=eth_getTransactionByHash&txhash=$HASH_TO_ANALYZE&apikey=${ETHERSCAN_API_KEY:0:10}..."

# Get transaction details from Etherscan
TX_RESPONSE=$(curl -s "${ETHERSCAN_URL}/api?module=proxy&action=eth_getTransactionByHash&txhash=$HASH_TO_ANALYZE&apikey=$ETHERSCAN_API_KEY")

echo "🔍 Raw Etherscan response: $TX_RESPONSE"

if echo "$TX_RESPONSE" | grep -q '"result"'; then
    echo "✅ Transaction found on Etherscan"
    
    # Extract key transaction details
    STATUS=$(echo "$TX_RESPONSE" | jq -r '.result.status // "unknown"')
    FROM=$(echo "$TX_RESPONSE" | jq -r '.result.from // "unknown"')
    TO=$(echo "$TX_RESPONSE" | jq -r '.result.to // "unknown"')
    VALUE=$(echo "$TX_RESPONSE" | jq -r '.result.value // "0"')
    GAS_USED=$(echo "$TX_RESPONSE" | jq -r '.result.gas // "unknown"')
    GAS_PRICE=$(echo "$TX_RESPONSE" | jq -r '.result.gasPrice // "unknown"')
    
    echo "📊 Transaction Details:"
    echo "  Status: $STATUS"
    echo "  From: $FROM"
    echo "  To: $TO" 
    echo "  Value: $VALUE wei"
    echo "  Gas Limit: $GAS_USED"
    echo "  Gas Price: $GAS_PRICE wei"
    
    # Get transaction receipt for more details
    echo ""
    echo "📡 Step 3: Getting transaction receipt..."
    RECEIPT_RESPONSE=$(curl -s "${ETHERSCAN_URL}/api?module=proxy&action=eth_getTransactionReceipt&txhash=$HASH_TO_ANALYZE&apikey=$ETHERSCAN_API_KEY")
    echo "🔍 Raw receipt response: $RECEIPT_RESPONSE"
    
    if echo "$RECEIPT_RESPONSE" | grep -q '"result"'; then
        RECEIPT_STATUS=$(echo "$RECEIPT_RESPONSE" | jq -r '.result.status // "unknown"')
        GAS_USED_ACTUAL=$(echo "$RECEIPT_RESPONSE" | jq -r '.result.gasUsed // "unknown"')
        
        echo ""
        echo "📋 Receipt Details:"
        echo "  Receipt Status: $RECEIPT_STATUS (0x1 = success, 0x0 = failed)"
        echo "  Gas Used: $GAS_USED_ACTUAL"
        
        if [ "$RECEIPT_STATUS" = "0x0" ]; then
            echo ""
            echo "❌ TRANSACTION FAILED"
            echo "🔍 Common UserOp failure reasons:"
            echo "  1. Insufficient gas (gas limit too low)"
            echo "  2. Invalid signature (wrong private key or message)"
            echo "  3. Insufficient balance (can't pay gas fees)"
            echo "  4. Invalid nonce (already used or too high)"
            echo "  5. Contract execution reverted (target contract failed)"
        elif [ "$RECEIPT_STATUS" = "0x1" ]; then
            echo ""
            echo "✅ TRANSACTION SUCCESSFUL"
            
            # Decode UserOperationEvent from logs to check UserOp execution status
            USEROP_EVENT_TOPIC="0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f"
            USEROP_EVENT=$(echo "$RECEIPT_RESPONSE" | jq -r ".result.logs[] | select(.topics[0] == \"$USEROP_EVENT_TOPIC\")")
            
            if [ -n "$USEROP_EVENT" ] && [ "$USEROP_EVENT" != "null" ]; then
                echo "🔍 UserOperationEvent found - decoding execution result..."
                
                # Extract UserOp execution data from the event
                USEROP_DATA=$(echo "$USEROP_EVENT" | jq -r '.data')
                USEROP_HASH_FROM_EVENT=$(echo "$USEROP_EVENT" | jq -r '.topics[1]')
                SENDER_FROM_EVENT=$(echo "$USEROP_EVENT" | jq -r '.topics[2]')
                PAYMASTER_FROM_EVENT=$(echo "$USEROP_EVENT" | jq -r '.topics[3]')
                
                echo "🔍 UserOperationEvent Analysis:"
                echo "  UserOp Hash: $USEROP_HASH_FROM_EVENT"
                echo "  Sender: $SENDER_FROM_EVENT"
                echo "  Paymaster: $PAYMASTER_FROM_EVENT"
                echo "  Event Data: $USEROP_DATA"
                
                # Decode the data field (contains nonce, success, actualGasCost, actualGasUsed)
                if [ ${#USEROP_DATA} -gt 130 ]; then
                    # UserOperationEvent data structure:
                    # - bytes 0-64: nonce (uint256)
                    # - bytes 64-128: success (bool, but stored as uint256)
                    # - bytes 128-192: actualGasCost (uint256)
                    # - bytes 192-256: actualGasUsed (uint256)
                    
                    NONCE_HEX="0x${USEROP_DATA:2:64}"
                    SUCCESS_HEX="0x${USEROP_DATA:66:64}"
                    ACTUAL_GAS_COST_HEX="0x${USEROP_DATA:130:64}"
                    ACTUAL_GAS_USED_HEX="0x${USEROP_DATA:194:64}"
                    
                    # Convert hex to decimal for readability
                    NONCE_DEC=$((NONCE_HEX))
                    ACTUAL_GAS_COST_DEC=$((ACTUAL_GAS_COST_HEX))
                    ACTUAL_GAS_USED_DEC=$((ACTUAL_GAS_USED_HEX))
                    
                    echo ""
                    echo "📊 UserOp Execution Details:"
                    echo "  Nonce Used: $NONCE_DEC"
                    echo "  Actual Gas Used: $ACTUAL_GAS_USED_DEC units"
                    echo "  Actual Gas Cost: $ACTUAL_GAS_COST_DEC wei"
                    
                    # Calculate effective gas price
                    if [ $ACTUAL_GAS_USED_DEC -gt 0 ]; then
                        EFFECTIVE_GAS_PRICE=$((ACTUAL_GAS_COST_DEC / ACTUAL_GAS_USED_DEC))
                        echo "  Effective Gas Price: $EFFECTIVE_GAS_PRICE wei/gas"
                    fi
                    
                    # Compare with estimated gas from transaction
                    if [ -n "$GAS_USED" ] && [ "$GAS_USED" != "unknown" ]; then
                        GAS_LIMIT_DEC=$((GAS_USED))
                        GAS_EFFICIENCY=$((ACTUAL_GAS_USED_DEC * 100 / GAS_LIMIT_DEC))
                        echo "  Gas Efficiency: $ACTUAL_GAS_USED_DEC / $GAS_LIMIT_DEC = ${GAS_EFFICIENCY}% utilized"
                        
                        if [ $GAS_EFFICIENCY -lt 50 ]; then
                            echo "  ⚠️ Low gas efficiency - consider reducing gas limits"
                        elif [ $GAS_EFFICIENCY -gt 95 ]; then
                            echo "  ⚠️ High gas utilization - consider increasing gas limits for safety"
                        else
                            echo "  ✅ Good gas efficiency"
                        fi
                    fi
                    
                    if [ "$SUCCESS_HEX" = "0x0000000000000000000000000000000000000000000000000000000000000001" ]; then
                        echo "  ✅ UserOp Execution: SUCCESS"
                        echo ""
                        echo "🎉 UserOp executed successfully on-chain!"
                        echo "💰 Gas efficiency: Used $ACTUAL_GAS_USED_DEC gas (estimated vs actual comparison available in logs)"
                    elif [ "$SUCCESS_HEX" = "0x0000000000000000000000000000000000000000000000000000000000000000" ]; then
                        echo "  ❌ UserOp Execution: FAILED"
                        echo ""
                        echo "🔍 USEROPS EXECUTION FAILED ANALYSIS:"
                        echo "  The transaction was included in a block BUT the UserOp execution failed"
                        echo "  This means the EntryPoint processed the UserOp but the target call reverted"
                        echo ""
                        
                        # Analyze gas costs vs smart wallet balance
                        echo "💰 BALANCE vs GAS COST ANALYSIS:"
                        SENDER_CLEAN=$(echo "$SENDER_FROM_EVENT" | sed 's/0x000000000000000000000000/0x/')
                        echo "  Smart Wallet: $SENDER_CLEAN"
                        echo "  Gas Cost Paid: $ACTUAL_GAS_COST_DEC wei"
                        
                        # Get smart wallet balance after transaction
                        echo "  Checking smart wallet balance..."
                        BALANCE_RESPONSE=$(curl -s "${ETHERSCAN_URL}/api?module=account&action=balance&address=$SENDER_CLEAN&tag=latest&apikey=$ETHERSCAN_API_KEY")
                        CURRENT_BALANCE=$(echo "$BALANCE_RESPONSE" | jq -r '.result // "0"')
                        CURRENT_BALANCE_DEC=$((CURRENT_BALANCE))
                        
                        echo "  Current Balance: $CURRENT_BALANCE_DEC wei"
                        
                        # Calculate what balance was before transaction
                        BALANCE_BEFORE=$((CURRENT_BALANCE_DEC + ACTUAL_GAS_COST_DEC))
                        echo "  Balance Before Tx: $BALANCE_BEFORE wei"
                        
                        if [ $BALANCE_BEFORE -lt $ACTUAL_GAS_COST_DEC ]; then
                            echo "  ❌ INSUFFICIENT BALANCE: Wallet had $BALANCE_BEFORE wei but needed $ACTUAL_GAS_COST_DEC wei"
                            echo "     Shortfall: $((ACTUAL_GAS_COST_DEC - BALANCE_BEFORE)) wei"
                        else
                            echo "  ✅ Balance was sufficient for gas costs"
                        fi
                        
                        echo ""
                        echo "🛠️ Most likely failure reasons (in order):"
                        echo "  1. 💸 Insufficient ETH balance in smart wallet for gas fees"
                        echo "  2. 🪙 Insufficient token balance for transfer amount"
                        echo "  3. 🔒 Target contract access denied or permissions"
                        echo "  4. 📝 Invalid calldata for target contract"
                        echo "  5. ⛽ Gas limit too low for contract execution"
                        echo ""
                        echo "💡 Debugging steps:"
                        echo "  1. Fund smart wallet with more ETH for gas fees"
                        echo "  2. Check token balance in smart wallet (not just ETH)"
                        echo "  3. Verify contract call parameters are correct"
                        echo "  4. Test the contract call directly (bypass UserOp)"
                    else
                        echo "  ⚠️ Unknown UserOp execution status: $SUCCESS_HEX"
                    fi
                else
                    echo "⚠️ UserOp event data too short to decode: ${#USEROP_DATA} chars"
                fi
            else
                echo "⚠️ No UserOperationEvent found in transaction logs"
            fi
        fi
    else
        echo "❌ Could not get transaction receipt"
    fi
    
    echo ""
    echo "🔗 View on Etherscan: https://sepolia.etherscan.io/tx/$HASH_TO_ANALYZE"
    
else
    echo "❌ Transaction not found on Etherscan"
    echo "Possible reasons:"
    echo "  1. Transaction hash is incorrect"
    echo "  2. Transaction is too recent (not indexed yet)"
    echo "  3. Wrong network (this script checks Sepolia)"
fi

echo ""
echo "🔍 Analysis complete!"
