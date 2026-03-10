# Contract State Trigger — Design Doc for Aggregator Backend

## Context

AP Studio (Next.js frontend) needs a generalized **event trigger** that monitors
arbitrary smart contract view functions and fires when a condition on the return
value is met. The immediate use case is **AAVE V3 Health Factor monitoring**, but
the design is protocol-agnostic and will cover any "poll a view function, check a
condition" pattern (Compound, Morpho, Euler, etc.).

---

## User Scenario

> "Alert me on Telegram when my AAVE Health Factor drops below 1.5"

### Workflow shape (3 nodes, 2 edges)

```
┌──────────────┐     ┌──────────────────────────────┐     ┌──────────────┐
│   Settings   │────▶│  Event Trigger                │────▶│   Telegram   │
│              │     │  (contractState subtype)       │     │              │
│ • wallet     │     │  • AAVE Pool.getUserAccountData│     │ • chatId     │
│ • threshold  │     │  • condition: HF < threshold   │     │ • message    │
└──────────────┘     └──────────────────────────────┘     └──────────────┘
```

**Why 3 nodes instead of 5**: The current polling template uses
`Cron Trigger → ContractRead → Branch → Telegram` (5 nodes including Settings).
With a contract-state event trigger the aggregator takes over the poll + condition
check, collapsing three nodes into one. This gives:

- Real-time alerting (aggregator polling cadence) instead of cron-based delay
- Simpler workflow for the user to configure
- The trigger output already contains the matched return data, so downstream
  nodes (Telegram) can reference it directly

### What the user configures in the frontend

| Field | Example value | Notes |
|---|---|---|
| Contract address | `$CONTRACT_AAVE_V3_POOL$` | Resolved per chain at import time |
| Method | `getUserAccountData` | Pre-filled, locked for AAVE template |
| Method params | `["{{settings.runner}}"]` | User's wallet address (template var) |
| Return field | `getUserAccountData.healthFactor` | Which tuple element to watch |
| Condition | `lt` | Less than |
| Threshold | `1500000000000000000` | 1.5 × 10^18 (raw uint256) |

---

## What the Frontend Sends to the Aggregator

The frontend constructs an `EventTriggerDataType` config and sends it via the
existing `TriggerType.Event` path — no new trigger type enum value is needed.

```jsonc
{
  "type": "eventTrigger",
  "config": {
    "queries": [
      {
        "addresses": ["0x87870Bca3F3fD6335C3F4ce8392D69350B4fA4E2"],
        "topics": [],
        "contractAbi": [
          {
            "inputs": [
              { "internalType": "address", "name": "user", "type": "address" }
            ],
            "name": "getUserAccountData",
            "outputs": [
              { "internalType": "uint256", "name": "totalCollateralBase", "type": "uint256" },
              { "internalType": "uint256", "name": "totalDebtBase", "type": "uint256" },
              { "internalType": "uint256", "name": "availableBorrowsBase", "type": "uint256" },
              { "internalType": "uint256", "name": "currentLiquidationThreshold", "type": "uint256" },
              { "internalType": "uint256", "name": "ltv", "type": "uint256" },
              { "internalType": "uint256", "name": "healthFactor", "type": "uint256" }
            ],
            "stateMutability": "view",
            "type": "function"
          }
        ],
        "methodCalls": [
          {
            "methodName": "getUserAccountData",
            "methodParams": ["{{settings.runner}}"],
            "applyToFields": []
          }
        ],
        "conditions": [
          {
            "fieldName": "getUserAccountData.healthFactor",
            "operator": "lt",
            "value": "1500000000000000000",
            "fieldType": "uint256"
          }
        ],
        "maxEventsPerBlock": 1
      }
    ],
    "cooldownSeconds": 3600
  }
}
```

### Key differences from the existing OraclePrice trigger

The OraclePrice trigger already uses the same `EventTriggerDataType` with
`methodCalls` and `conditions`. The only structural differences are:

| | OraclePrice (working today) | Contract State (AAVE) |
|---|---|---|
| `methodParams` | `[]` (latestRoundData takes no args) | `["{{settings.runner}}"]` |
| `fieldType` in conditions | `"decimal"` | `"uint256"` |
| `contractAbi` | Chainlink Aggregator (hardcoded) | Provided per-query (AAVE Pool) |
| `cooldownSeconds` | not used | needed to prevent per-block spam |

---

## What We Need the Aggregator to Confirm / Support

### 1. `methodParams` with actual values

Today the OraclePrice trigger sends `methodParams: []` because
`latestRoundData()` takes no arguments. The AAVE use case sends
`methodParams: ["0xAbC...123"]` (a wallet address).

**Ask**: Confirm that non-empty `methodParams` are correctly passed to the
contract call. This is the only net-new capability required — everything else
in the config shape already exists.

### 2. Template variable resolution in `methodParams`

The frontend sends `"{{settings.runner}}"` as a method param. This is the same
template variable syntax used elsewhere in workflows (e.g., branch expressions,
contractRead params).

**Ask**: Confirm that `methodParams` values go through the same template variable
resolution pass before the RPC call is made. If the aggregator already resolves
`{{...}}` in all string fields, this should work automatically.

### 3. `fieldName` path for tuple return values

`getUserAccountData` returns a 6-element tuple. The condition references a
specific element: `getUserAccountData.healthFactor`.

**Ask**: What `fieldName` format does the aggregator expect for tuple elements?
Options:

- By output name: `getUserAccountData.healthFactor` (preferred, uses ABI output names)
- By index: `getUserAccountData.result[5]`
- Something else?

The frontend will adapt to whichever format the aggregator supports. Please
document the exact path syntax.

### 4. `fieldType: "uint256"` in conditions

The OraclePrice trigger uses `fieldType: "decimal"` for Chainlink's 8-decimal
prices. AAVE health factor is a raw `uint256` (scaled by 10^18) and should be
compared as a big integer, not a floating-point decimal.

**Ask**: Does the conditions engine support `fieldType: "uint256"` for
big-integer comparisons? If not, what field type should we use?

### 5. `cooldownSeconds`

The `EventTriggerDataType` interface includes `cooldownSeconds?: number` but
it's unclear if the aggregator implements it.

Without cooldown, every polling cycle where `HF < threshold` would fire a new
trigger execution → the user gets spammed with Telegram messages every block.

**Ask**: Is `cooldownSeconds` implemented? If so, confirm the behavior:
after a trigger fires, suppress further firings for N seconds even if the
condition remains true.

---

## AAVE Contract Reference

### Supported chains

| Chain | Chain ID | AAVE V3 Pool Address |
|---|---|---|
| Ethereum | 1 | `0x87870Bca3F3fD6335C3F4ce8392D69350B4fA4E2` |
| Base | 8453 | `0xA238Dd80C259a72e81d7e4664a9801593F98d1c5` |
| Sepolia | 11155111 | `0x6Ae43d3271ff6888e7Fc43Fd7321a503ff738951` |
| Base Sepolia | 84532 | `0x07eA79F68B2B3df564D0A34F8e19D9B1e339814b` |

### `getUserAccountData(address user)` returns

| Index | Name | Type | Description |
|---|---|---|---|
| 0 | totalCollateralBase | uint256 | Total collateral in base currency (USD, 8 decimals) |
| 1 | totalDebtBase | uint256 | Total debt in base currency |
| 2 | availableBorrowsBase | uint256 | Remaining borrow capacity |
| 3 | currentLiquidationThreshold | uint256 | Weighted average liquidation threshold |
| 4 | ltv | uint256 | Weighted average loan-to-value |
| 5 | healthFactor | uint256 | Health factor scaled by 10^18. 1.0 = 10^18, 1.5 = 1.5×10^18. When user has no debt, returns `type(uint256).max` |

### Health factor threshold examples

| Display value | Raw uint256 value |
|---|---|
| 1.0 (liquidation) | `1000000000000000000` |
| 1.25 | `1250000000000000000` |
| 1.5 (common alert) | `1500000000000000000` |
| 2.0 (conservative) | `2000000000000000000` |

---

## Expected Trigger Output

When the condition matches, the trigger output should contain the full method
call result so downstream nodes can reference the data. Expected shape:

```jsonc
{
  "data": [
    {
      "methodCalls": [
        {
          "methodName": "getUserAccountData",
          "result": [
            "150000000000",       // totalCollateralBase
            "100000000000",       // totalDebtBase
            "20000000000",        // availableBorrowsBase
            "8250",               // currentLiquidationThreshold
            "7500",               // ltv
            "1400000000000000000" // healthFactor (1.4 — below 1.5 threshold)
          ]
        }
      ]
    }
  ]
}
```

The Telegram node references this as:
`{{eventTrigger.data[0].methodCalls[0].result[5]}}` for the raw health factor.

---

## Summary of Asks

| # | Ask | Effort Estimate |
|---|---|---|
| 1 | Confirm non-empty `methodParams` work in contract calls | Verification only |
| 2 | Confirm `{{...}}` template vars resolve inside `methodParams` | Verification only |
| 3 | Document `fieldName` path syntax for tuple return values | Documentation |
| 4 | Confirm/add `fieldType: "uint256"` support in conditions | Possibly small change |
| 5 | Confirm `cooldownSeconds` is implemented | Verification or small feature |

Items 1-2 are the critical path. If `methodParams` already works with values and
template variables, the frontend can ship the AAVE Health Factor template with
**zero aggregator code changes**.
