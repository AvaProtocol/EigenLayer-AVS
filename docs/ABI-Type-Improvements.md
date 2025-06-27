# ABI Type Improvements

## Overview

This document describes the improvements made to ensure that contract ABI field types correspond to their definitions in the contract ABI, rather than converting everything to strings.

## Problem

Previously, when parsing contract events and function returns using ABIs, all fields were being converted to strings regardless of their actual ABI type definitions. For example:

```json
{
  "current": "2450.63713059", // Should be a number for int256
  "decimals": "8", // Should be a number for uint8
  "roundId": "24201", // Should be a number for uint256
  "updatedAt": "1751000820" // Should be a number for uint256
}
```

## Solution

We updated the ABI parsing logic across multiple components to properly type fields according to their ABI definitions:

### 1. Shared ABI Utilities (`abi_utils.go`)

**File**: `core/taskengine/abi_utils.go`

- Created `ABIValueConverter` struct to centralize ABI value conversion logic
- Provides `ConvertABIValueToInterface()` for event parsing (returns interface{} types)
- Provides `ConvertABIValueToString()` for contract reads/writes (returns string types)
- Includes decimal formatting utilities (`FormatWithDecimals()`)
- Helper functions for topic value conversion

This eliminates code duplication across the three main ABI parsing components.

### 2. Event Trigger Parsing (`run_node_immediately.go`)

**File**: `core/taskengine/run_node_immediately.go`

- Updated `parseEventWithABI()` function to use shared `ABIValueConverter`
- Maintains decimal formatting for token amounts when requested
- Removed duplicated conversion logic

### 3. Contract Read Processing (`vm_runner_contract_read.go`)

**File**: `core/taskengine/vm_runner_contract_read.go`

- Updated `buildStructuredDataWithDecimalFormatting()` to use shared utilities
- Added proper ABI type awareness for contract read results
- Maintains backward compatibility with existing decimal formatting features

### 4. Contract Write Event Decoding (`vm_runner_contract_write.go`)

**File**: `core/taskengine/vm_runner_contract_write.go`

- Updated `decodeEvents()` function to use shared utilities
- Handles both indexed and non-indexed event parameters correctly
- Note: Contract write events are stored in protobuf `map[string]string` format, so values are still strings but properly converted

**Type Mapping**:

- `uint8`, `uint16`, `uint32`, `uint64` → `uint64` (as string for JSON compatibility)
- `int8`, `int16`, `int32`, `int64` → `int64` (as string for JSON compatibility)
- `uint256`, `int256` → `string` (to avoid precision loss)
- `bool` → `bool`
- `address` → `string` (hex format)
- `string` → `string`
- `bytes`, `bytes32` → `string` (hex format)

## Examples

### Before (All Strings)

```json
{
  "eventName": "AnswerUpdated",
  "current": "245063713059",
  "roundId": "24201",
  "updatedAt": "1751000820",
  "decimals": "8"
}
```

### After (Proper Types)

```json
{
  "eventName": "AnswerUpdated",
  "current": "245063713059", // string (uint256 - precision preserved)
  "currentRaw": "245063713059", // raw value when decimal formatting applied
  "roundId": "24201", // string (uint256 - precision preserved)
  "updatedAt": "1751000820", // string (uint256 - precision preserved)
  "decimals": 8 // number (uint8 - fits in standard types)
}
```

## Decimal Formatting

The system still supports decimal formatting for token amounts:

```json
{
  "eventName": "Transfer",
  "value": "1.5", // formatted with decimals
  "valueRaw": "1500000000000000000", // raw wei amount
  "decimals": 18
}
```

## Code Structure

### ABIValueConverter Usage

```go
// Create converter with decimal formatting support
converter := NewABIValueConverter(decimalsValue, fieldsToFormat)

// For event parsing (returns interface{})
value := converter.ConvertABIValueToInterface(decodedValue, abiType, fieldName)

// For contract reads/writes (returns string)
stringValue := converter.ConvertABIValueToString(decodedValue, abiType, fieldName)

// Access raw metadata for decimal formatting
rawMetadata := converter.GetRawFieldsMetadata()
```

### Benefits of Shared Utilities

1. **DRY Principle**: Eliminates code duplication across three files
2. **Consistency**: Ensures all ABI parsing uses the same conversion logic
3. **Maintainability**: Changes to type mapping only need to be made in one place
4. **Testability**: Shared utilities can be tested independently

## Testing

Added comprehensive tests in `core/taskengine/abi_typing_test.go`:

- `TestABIFieldTyping()`: Tests event parsing with various ABI types
- `TestContractReadABITyping()`: Tests contract read result typing
- `TestDecimalFormattingWithProperTypes()`: Tests decimal formatting integration

## Backward Compatibility

These changes maintain backward compatibility:

- Existing workflows continue to work
- String representations are still available for all numeric types
- Decimal formatting features are preserved
- API responses maintain the same structure

## Benefits

1. **Type Safety**: Fields now have appropriate types matching their ABI definitions
2. **Better Developer Experience**: No need to parse strings for numeric operations
3. **Precision Preservation**: Large numbers (uint256) remain as strings to avoid precision loss
4. **Consistency**: All ABI parsing components now use the same type mapping logic
5. **Future-Proof**: Easy to extend for new ABI types
6. **Maintainable**: Shared utilities reduce code duplication and improve maintainability

## Migration Guide

For existing applications:

1. **Event Triggers**: No changes needed - types are now more accurate
2. **Contract Reads**: Results may have different types - update type assertions if needed
3. **Contract Writes**: Event decoding now has proper types (though still stored as strings in protobuf)

Example code update:

```javascript
// Before
const decimals = parseInt(result.decimals);

// After
const decimals = result.decimals; // Already a number
```
