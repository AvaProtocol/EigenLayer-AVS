# Tenderly EventTrigger Simulation

## Overview

The Tenderly EventTrigger simulation provides **realistic testing** of EventTrigger conditions using **real current market data** from Tenderly Gateway. This feature allows you to test your event trigger configurations without waiting for actual blockchain events.

## Key Behavior

The simulation mode behaves **exactly like the regular `runTask`** execution:

- ✅ **Conditions Match**: Returns `found: true` with real event data
- ❌ **Conditions Don't Match**: Returns `found: false` with no event data
- 🔍 **Debug Info**: Includes `_raw` field with current market data for debugging

## Configuration

### Basic Setup

```javascript
{
  triggerType: "eventTrigger",
  triggerConfig: {
    simulationMode: true,  // 🔮 Enable Tenderly simulation
    queries: [{
      addresses: ["0x694AA1769357215DE4FAC081bf1f309aDC325306"], // Sepolia ETH/USD
      topics: [{ values: ["0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"] }],
      conditions: [{
        fieldName: "current",
        operator: "gt", 
        value: "240000000000"  // $2400 with 8 decimals
      }]
    }]
  }
}
```

### Tenderly Configuration

The system uses your Tenderly Gateway endpoint:
```
https://sepolia.gateway.tenderly.co/YOUR_API_KEY
```

## Examples

### Example 1: Basic Price Monitoring (No Conditions)
```javascript
// Always returns found: true with current price data
{
  simulationMode: true,
  queries: [{
    addresses: ["0x694AA1769357215DE4FAC081bf1f309aDC325306"],
    // No conditions - just fetch current data
  }]
}
```

### Example 2: Conditional Price Alert
```javascript
// Returns found: true ONLY if real ETH price > $2400
{
  simulationMode: true,
  queries: [{
    addresses: ["0x694AA1769357215DE4FAC081bf1f309aDC325306"],
    conditions: [{
      fieldName: "current",
      operator: "gt",
      value: "240000000000"  // $2400
    }]
  }]
}
```

### Example 3: Realistic Testing
```javascript
// Test high threshold - likely returns found: false
{
  simulationMode: true,
  queries: [{
    addresses: ["0x694AA1769357215DE4FAC081bf1f309aDC325306"],
    conditions: [{
      fieldName: "current", 
      operator: "gt",
      value: "440000000000"  // $4400 - unrealistic threshold
    }]
  }]
}
```

## Response Structure

The EventTrigger now returns proper protobuf-compliant responses that match the `EventTrigger.Output` schema.

### When Conditions Match
Returns an `EventTrigger.Output` with an `evm_log` containing the event data and debug information:

```json
{
  "event_trigger": {
    "evm_log": {
      "address": "0x694AA1769357215DE4FAC081bf1f309aDC325306",
      "topics": ["0x0559884fd3...", "0x0000000000000..."],
      "data": "0x000000000000000...",
      "block_number": 1234567,
      "transaction_hash": "0xabcdef...",
      "transaction_index": 0,
      "block_hash": "0x123456...",
      "index": 0,
      "removed": false,
      "debug_info": "{\"real_price_usd\":2423.39,\"real_price_raw\":\"242339098853\",\"contract\":\"0x694AA...\",\"simulation_mode\":true,\"debug_message\":\"Tenderly simulation using real current market data\"}"
    }
  }
}
```

### When Conditions Don't Match
Returns an empty `EventTrigger.Output` (no `evm_log` or `transfer_log`):

```json
{
  "event_trigger": {}
}
```

### Debug Information Structure
The `debug_info` field contains JSON-encoded debugging data:

```json
{
  "real_price_usd": 2423.39,
  "real_price_raw": "242339098853", 
  "contract": "0x694AA1769357215DE4FAC081bf1f309aDC325306",
  "chain_id": 11155111,
  "simulation_mode": true,
  "tenderly_used": true,
  "debug_message": "Tenderly simulation using real current market data"
}
```

## Supported Operators

- `gt` - Greater than
- `lt` - Less than  
- `eq` - Equal to
- `gte` - Greater than or equal
- `lte` - Less than or equal
- `ne` - Not equal

## Supported Price Feeds

Currently optimized for Chainlink price feeds:
- ✅ ETH/USD (Sepolia): `0x694AA1769357215DE4FAC081bf1f309aDC325306`
- ✅ Any Chainlink aggregator with `AnswerUpdated` events

## Use Cases

### 1. **Testing Event Triggers**
```javascript
// Test if your price alert would trigger right now
const result = await runTrigger({
  simulationMode: true,
  conditions: [{ fieldName: "current", operator: "gt", value: "250000000000" }]
});

if (result.event_trigger && result.event_trigger.evm_log) {
  const debugInfo = JSON.parse(result.event_trigger.evm_log.debug_info);
  console.log("Alert would trigger! Current price:", debugInfo.real_price_usd);
  console.log("Event data:", result.event_trigger.evm_log);
} else {
  console.log("Alert would NOT trigger - conditions not met");
}
```

### 2. **Workflow Validation**
```javascript
// Validate your trigger setup before deploying
const validation = await runTrigger({ simulationMode: true });
if (validation.event_trigger && validation.event_trigger.evm_log) {
  console.log("Trigger setup working!");
  const debugInfo = JSON.parse(validation.event_trigger.evm_log.debug_info);
  console.log("Current market conditions:", debugInfo);
} else {
  console.log("No event data returned");
}
```

### 3. **Demo & Education**
```javascript
// Show users what their event data looks like
const demo = await runTrigger({ 
  simulationMode: true,
  conditions: [] // No conditions for demo
});

if (demo.event_trigger && demo.event_trigger.evm_log) {
  console.log("Your event data will look like:", demo.event_trigger.evm_log);
  const debugInfo = JSON.parse(demo.event_trigger.evm_log.debug_info);
  console.log("Debug info:", debugInfo);
} else {
  console.log("No event data available");
}
```

## Comparison: Simulation vs Historical

| Feature | Simulation Mode | Historical Mode |
|---------|----------------|-----------------|
| **Data Source** | Real current prices via Tenderly | Historical blockchain events |
| **Speed** | Instant | Depends on search range |
| **Conditions** | Evaluated against current data | Evaluated against historical data |
| **Use Case** | Testing & validation | Production workflows |
| **Response** | found: true/false based on current conditions | found: true/false based on historical matches |

## Best Practices

1. **Test Realistic Thresholds**: Use market-relevant price thresholds
2. **Check Debug Data**: Always examine the `_raw` field for current market context
3. **Validate Before Production**: Test with `simulationMode: true` then deploy with `simulationMode: false`
4. **Handle Both Cases**: Your code should handle both `found: true` and `found: false` responses

## Troubleshooting

### Common Issues

**Issue**: Always getting empty event responses
**Solution**: Check if your conditions are realistic for current market prices. Examine the `debug_info` field when events are returned.

**Issue**: Tenderly connection fails
**Solution**: Verify your Tenderly Gateway URL is correct and accessible.

**Issue**: Wrong price format
**Solution**: Chainlink uses 8 decimals. $2400 = `240000000000` (2400 * 10^8).

### Debug Examples

```javascript
// Check current price without conditions
const current = await runTrigger({ simulationMode: true, conditions: [] });
if (current.event_trigger && current.event_trigger.evm_log) {
  const debugInfo = JSON.parse(current.event_trigger.evm_log.debug_info);
  console.log("Current ETH price:", debugInfo.real_price_usd);
} else {
  console.log("No event data returned");
}

// Test your exact conditions  
const test = await runTrigger({ 
  simulationMode: true, 
  conditions: [{ fieldName: "current", operator: "gt", value: "240000000000" }]
});

if (test.event_trigger && test.event_trigger.evm_log) {
  const debugInfo = JSON.parse(test.event_trigger.evm_log.debug_info);
  console.log("Would trigger: true");
  console.log("Current price:", debugInfo.real_price_usd);
  console.log("Threshold: 2400");
} else {
  console.log("Would trigger: false");
  console.log("Conditions not met by current market data");
}
```

## Implementation Details

The simulation system:
1. 🔍 Calls Tenderly Gateway's `eth_call` to get real `latestRoundData()` 
2. 📊 Uses the actual current price from Chainlink aggregator
3. ⚖️ Evaluates your conditions against this real price
4. ✅ Returns `found: true` + event data if conditions match
5. ❌ Returns `found: false` + debug info if conditions don't match
6. 🔍 Always includes `_raw` field with real market data for debugging

This ensures your `runTrigger` tests behave exactly like production `runTask` execution! 