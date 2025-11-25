# MAX Amount Resolution Plan

## Overview

This document outlines the plan for handling "MAX" as a special value for ETH input parameters in ContractRead and ContractWrite nodes. When "MAX" is detected, the system will fetch the wallet's ETH balance on-chain and deduct gas fees to calculate the actual input amount.

## Design Approach

### Core Concept
- **Whitelist-Based**: Only specific contract methods that accept ETH as input can use "MAX"
- **On-Demand Balance Retrieval**: Fetch balance from chain when "MAX" is detected
- **Gas Deduction**: For ETH inputs, deduct estimated gas fees from balance before use
- **Method Parameter Maps**: Maintain maps of method parameters for ContractRead and ContractWrite to identify ETH input fields

## Requirements

### 1. Method Parameter Maps

#### 1.1 ContractRead Method Map
Map contract addresses and method names to their parameter structures, identifying which parameters accept ETH.

**Structure:**
```go
type ContractReadMethodMap map[string]map[string]*MethodParamInfo

type MethodParamInfo struct {
    MethodName      string
    ETHInputIndices []int  // Indices of parameters that accept ETH
    TokenParamIndex int    // Index of parameter that specifies token address (if applicable)
    WalletParamIndex int   // Index of parameter that specifies wallet address (if applicable)
}
```

**Example:**
```go
var contractReadMethodMap = ContractReadMethodMap{
    "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3": { // QuoterV2
        "quoteExactInputSingle": &MethodParamInfo{
            MethodName:      "quoteExactInputSingle",
            ETHInputIndices: []int{}, // QuoterV2 doesn't take ETH directly
            TokenParamIndex: 0,       // tokenIn is first param in tuple
        },
    },
}
```

#### 1.2 ContractWrite Method Map
Map contract addresses and method names to their parameter structures, identifying which parameters accept ETH.

**Structure:**
```go
type ContractWriteMethodMap map[string]map[string]*MethodParamInfo

type MethodParamInfo struct {
    MethodName      string
    ETHInputIndices []int  // Indices of parameters that accept ETH (in tuple/struct)
    TokenParamIndex int    // Index of parameter that specifies token address (if applicable)
    WalletParamIndex int   // Index of parameter that specifies wallet address (if applicable)
    IsPayable       bool   // Whether method is payable (can receive ETH)
}
```

**Example:**
```go
var contractWriteMethodMap = ContractWriteMethodMap{
    "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E": { // SwapRouter02
        "exactInputSingle": &MethodParamInfo{
            MethodName:      "exactInputSingle",
            ETHInputIndices: []int{4}, // amountIn is 5th field (index 4) in tuple
            TokenParamIndex: 0,        // tokenIn is first field in tuple
            WalletParamIndex: 3,       // recipient is 4th field (index 3) in tuple
            IsPayable:       true,      // Method is payable
        },
    },
}
```

### 2. Whitelist System

#### 2.1 Whitelist Structure
Maintain a whitelist of contract methods that support "MAX" for ETH inputs.

**Structure:**
```go
type MAXWhitelistEntry struct {
    ContractAddress common.Address
    MethodName     string
    TokenType      string  // "ETH" or "ERC20"
    WalletSource   string  // "aa_sender", "settings.runner", or parameter index
    GasDeduction   bool    // Whether to deduct gas fees
}

var maxWhitelist = []MAXWhitelistEntry{
    {
        ContractAddress: common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E"), // SwapRouter02
        MethodName:     "exactInputSingle",
        TokenType:      "ETH",
        WalletSource:   "aa_sender", // Use aa_sender variable
        GasDeduction:   true,        // Deduct gas for ETH
    },
    // Add more entries as needed
}
```

#### 2.2 Whitelist Validation
- Before processing "MAX", check if contract + method is whitelisted
- If not whitelisted, return error: "MAX is not supported for this contract method"
- Only whitelisted methods can use "MAX" string

### 3. MAX Detection and Resolution

#### 3.1 Detection Flow
1. **Template Resolution**: After template variables are resolved
2. **Parameter Parsing**: Parse method parameters (direct params or tuple/struct)
3. **MAX Detection**: Check if any parameter value equals "MAX" (case-insensitive)
4. **Whitelist Check**: Verify contract + method is whitelisted
5. **Balance Retrieval**: Fetch balance from chain
6. **Gas Deduction**: Calculate max available (balance - gas for ETH)
7. **Replacement**: Replace "MAX" with calculated value

#### 3.2 Detection Logic

**For ContractRead:**
```go
func (r *ContractReadProcessor) resolveMAXParameters(ctx context.Context, contractAddress common.Address, methodName string, resolvedParams []string) ([]string, error) {
    // Check if method is whitelisted
    if !isWhitelisted(contractAddress, methodName) {
        return resolvedParams, nil // Not whitelisted, skip MAX resolution
    }
    
    // Get method parameter info
    paramInfo := contractReadMethodMap[contractAddress.Hex()][methodName]
    if paramInfo == nil {
        return resolvedParams, nil // No parameter info, skip
    }
    
    // Check each parameter for "MAX"
    for _, ethIndex := range paramInfo.ETHInputIndices {
        if ethIndex < len(resolvedParams) {
            if strings.EqualFold(strings.TrimSpace(resolvedParams[ethIndex]), "MAX") {
                // Resolve MAX
                walletAddress, err := r.getWalletAddress(paramInfo)
                if err != nil {
                    return nil, fmt.Errorf("Error Code 3002: %v", err)
                }
                
                maxAmount, err := r.calculateMaxETHAmount(ctx, walletAddress)
                if err != nil {
                    return nil, err
                }
                
                resolvedParams[ethIndex] = maxAmount
            }
        }
    }
    
    return resolvedParams, nil
}
```

**For ContractWrite:**
```go
func (r *ContractWriteProcessor) resolveMAXInTuple(ctx context.Context, contractAddress common.Address, methodName string, tupleParam string) (string, error) {
    // Check if method is whitelisted
    if !isWhitelisted(contractAddress, methodName) {
        return tupleParam, nil // Not whitelisted, skip MAX resolution
    }
    
    // Get method parameter info
    paramInfo := contractWriteMethodMap[contractAddress.Hex()][methodName]
    if paramInfo == nil {
        return tupleParam, nil // No parameter info, skip
    }
    
    // Parse tuple/struct
    var tupleElements []interface{}
    if err := json.Unmarshal([]byte(tupleParam), &tupleElements); err != nil {
        return tupleParam, nil // Not a JSON array, skip
    }
    
    // Check each ETH input index for "MAX"
    for _, ethIndex := range paramInfo.ETHInputIndices {
        if ethIndex < len(tupleElements) {
            amountValue := fmt.Sprintf("%v", tupleElements[ethIndex])
            if strings.EqualFold(strings.TrimSpace(amountValue), "MAX") {
                // Resolve MAX
                walletAddress, err := r.getWalletAddress(paramInfo)
                if err != nil {
                    return "", fmt.Errorf("Error Code 3002: %v", err)
                }
                
                maxAmount, err := r.calculateMaxETHAmount(ctx, walletAddress)
                if err != nil {
                    return "", err
                }
                
                // Replace in tuple
                tupleElements[ethIndex] = maxAmount
                
                // Re-serialize
                if jsonBytes, err := json.Marshal(tupleElements); err == nil {
                    tupleParam = string(jsonBytes)
                }
            }
        }
    }
    
    return tupleParam, nil
}
```

### 4. Wallet Address Resolution

#### 4.1 Wallet Source Priority
Based on whitelist entry's `WalletSource` field:

1. **"aa_sender"**: Use `vm.vars["aa_sender"]` (smart wallet address)
2. **"settings.runner"**: Use `vm.vars["settings"]["runner"]`
3. **Parameter Index**: Extract wallet address from method parameter at specified index

**Implementation:**
```go
func (r *ContractWriteProcessor) getWalletAddress(paramInfo *MethodParamInfo) (common.Address, error) {
    switch paramInfo.WalletSource {
    case "aa_sender":
        if aaSenderVar, ok := r.vm.vars["aa_sender"]; ok {
            if aaSenderStr, ok := aaSenderVar.(string); ok && aaSenderStr != "" {
                return common.HexToAddress(aaSenderStr), nil
            }
        }
        return common.Address{}, fmt.Errorf("aa_sender variable not found")
        
    case "settings.runner":
        if settingsVar, ok := r.vm.vars["settings"]; ok {
            if settingsMap, ok := settingsVar.(map[string]interface{}); ok {
                if runnerStr, ok := settingsMap["runner"].(string); ok && runnerStr != "" {
                    return common.HexToAddress(runnerStr), nil
                }
            }
        }
        return common.Address{}, fmt.Errorf("settings.runner not found")
        
    default:
        // Parameter index (e.g., "3" for recipient in exactInputSingle)
        // Extract from resolved parameters
        return common.Address{}, fmt.Errorf("wallet source not supported: %s", paramInfo.WalletSource)
    }
}
```

### 5. Balance Retrieval and Gas Deduction

#### 5.1 Balance Retrieval
```go
func (r *ContractWriteProcessor) calculateMaxETHAmount(ctx context.Context, walletAddress common.Address) (string, error) {
    // Get native ETH balance
    balance, err := r.client.BalanceAt(ctx, walletAddress, nil)
    if err != nil {
        return "", fmt.Errorf("Error Code 3004: failed to get ETH balance: %w", err)
    }
    
    if balance.Cmp(big.NewInt(0)) == 0 {
        return "", fmt.Errorf("Error Code 3006: ETH balance is zero")
    }
    
    // Estimate gas cost
    gasCost, err := r.estimateGasCost(ctx)
    if err != nil {
        // Use conservative buffer
        gasCost = big.NewInt(1000000000000000) // 0.001 ETH
        r.vm.logger.Warn("Gas estimation failed, using conservative buffer", "error", err)
    }
    
    // Calculate max available
    maxAmount := new(big.Int).Sub(balance, gasCost)
    if maxAmount.Cmp(big.NewInt(0)) <= 0 {
        return "", fmt.Errorf("Error Code 3003: insufficient balance: %s wei available, but %s wei required for gas fees", balance.String(), gasCost.String())
    }
    
    return maxAmount.String(), nil
}
```

#### 5.2 Gas Estimation
```go
func (r *ContractWriteProcessor) estimateGasCost(ctx context.Context) (*big.Int, error) {
    // Get current gas price
    gasPrice, err := r.client.SuggestGasPrice(ctx)
    if err != nil {
        return nil, fmt.Errorf("Error Code 3005: failed to get gas price: %w", err)
    }
    
    // Use conservative gas limit for swap transactions
    gasLimit := big.NewInt(250000) // 250k gas
    
    // Calculate total gas cost
    gasCost := new(big.Int).Mul(gasLimit, gasPrice)
    
    return gasCost, nil
}
```

### 6. Error Handling

#### 6.1 Error Codes
```go
const (
    ErrCodeNotWhitelisted        = 3001  // Contract method not whitelisted for MAX
    ErrCodeWalletAddressNotFound = 3002  // Cannot determine wallet address
    ErrCodeInsufficientBalance   = 3003  // Insufficient balance after gas deduction
    ErrCodeBalanceRetrievalFailed = 3004 // Failed to retrieve balance from chain
    ErrCodeGasEstimationFailed   = 3005  // Gas estimation failed (fallback used)
    ErrCodeZeroBalance           = 3006  // Balance is zero
)
```

#### 6.2 Error Messages
```
Error Code 3001: "MAX is not supported for contract {address} method {methodName}. This method is not whitelisted."
Error Code 3002: "Cannot determine wallet address for MAX resolution. Ensure aa_sender or settings.runner is set."
Error Code 3003: "Insufficient balance: {balance} wei available, but {gasCost} wei required for gas fees."
Error Code 3004: "Failed to retrieve ETH balance from chain: {error details}."
Error Code 3005: "Failed to estimate gas costs. Using conservative buffer of 0.001 ETH."
Error Code 3006: "ETH balance is zero. Cannot calculate MAX amount."
```

## Implementation Plan

### Phase 1: Build Method Parameter Maps

#### 1.1 Create Method Parameter Map Structures
- Define `MethodParamInfo` struct
- Define `ContractReadMethodMap` and `ContractWriteMethodMap` types
- Create initialization functions

#### 1.2 Populate ContractRead Method Map
- Add QuoterV2 `quoteExactInputSingle` (if applicable)
- Add other ContractRead methods that accept ETH

#### 1.3 Populate ContractWrite Method Map
- Add SwapRouter02 `exactInputSingle`
- Add other ContractWrite methods that accept ETH
- Identify ETH input parameter indices

### Phase 2: Build Whitelist System

#### 2.1 Create Whitelist Structure
- Define `MAXWhitelistEntry` struct
- Create whitelist validation function
- Add initial whitelist entries

#### 2.2 Whitelist Validation Logic
- Check contract address + method name against whitelist
- Return error if not whitelisted
- Extract wallet source from whitelist entry

### Phase 3: Implement MAX Resolution

#### 3.1 ContractRead Integration
- Add `resolveMAXParameters` function
- Call after template resolution, before calldata generation
- Handle both direct parameters and tuple parameters

#### 3.2 ContractWrite Integration
- Add `resolveMAXInTuple` function
- Call after template resolution and JSON array expansion
- Replace "MAX" with calculated value in tuple

#### 3.3 Wallet Address Resolution
- Implement `getWalletAddress` function
- Support "aa_sender", "settings.runner", and parameter index sources

### Phase 4: Balance Retrieval and Gas Deduction

#### 4.1 Balance Retrieval
- Implement `calculateMaxETHAmount` function
- Use RPC `BalanceAt` to get ETH balance
- Handle errors gracefully

#### 4.2 Gas Estimation
- Implement `estimateGasCost` function
- Get gas price from RPC
- Use conservative gas limit (250k)
- Fallback to fixed buffer (0.001 ETH)

### Phase 5: Error Handling

#### 5.1 Error Code Definitions
- Define error code constants
- Create error message templates

#### 5.2 Error Propagation
- Return errors with proper error codes
- Log warnings for fallback scenarios
- Don't fail node execution for gas estimation failures (use fallback)

## Testing Strategy

### Unit Tests
- Test method parameter map lookup
- Test whitelist validation
- Test MAX detection in parameters
- Test wallet address resolution
- Test balance retrieval
- Test gas estimation and fallback
- Test error cases

### Integration Tests
- Test ContractRead with MAX for ETH input
- Test ContractWrite with MAX for ETH input in tuple
- Test with whitelisted contract methods
- Test with non-whitelisted contract methods (should fail)
- Test with real RPC calls (Sepolia testnet)
- Test error scenarios (missing wallet, insufficient balance, etc.)

## Edge Cases

1. **Non-Whitelisted Methods**: Return error, don't process MAX
2. **Missing Wallet Address**: Return error with code 3002
3. **Zero Balance**: Return error with code 3006
4. **Insufficient Balance**: Return error with code 3003
5. **Gas Estimation Failure**: Use fallback buffer, log warning
6. **RPC Failure**: Return error with code 3004
7. **Case Sensitivity**: "MAX", "max", "Max" all treated the same
8. **Whitespace**: Trim whitespace before comparison

## Future Enhancements

1. **ERC20 Support**: Extend whitelist to support ERC20 tokens (no gas deduction)
2. **Dynamic Gas Estimation**: Use actual transaction gas estimation instead of fixed limit
3. **Gas Price Caching**: Cache gas price within same execution context
4. **Multiple MAX Values**: Support multiple "MAX" values in same transaction
5. **Whitelist Management**: API or config file for managing whitelist
