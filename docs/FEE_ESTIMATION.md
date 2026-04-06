# Fee Estimation

## Overview

Workflow fees have three components, each with its own unit:

| Component | Unit | Purpose |
|-----------|------|---------|
| `execution_fee` | USD | Flat per-run platform fee |
| `cogs[]` | WEI | Per-node operational costs (gas, future: external API) |
| `value_fee` | PERCENTAGE | Workflow-level value capture (% of tx value) |

Every fee field uses the `Fee{amount, unit}` structure — self-describing, no implicit units. Non-execution nodes (logic, reads, API calls) have **no fees**.

## Value-Capture Tiers

Tiers are pure pricing groups — meaning comes from classification logic, not the label.

### Decision rule

> If this workflow **fails or is delayed**, does the user **lose money immediately**?
>
> **YES** → Tier 3 &nbsp;|&nbsp; **NO, but improves outcome** → Tier 2 &nbsp;|&nbsp; **Simple execution** → Tier 1

### Tier definitions

| Tier | Default % | Classification criteria | Examples |
|------|-----------|----------------------|----------|
| Tier 1 | 0.03% | Simple execution — user could do manually | ETH transfers, simple swaps |
| Tier 2 | 0.09% | Optimization/convenience — delay doesn't cause major loss | Rebalancing, DCA, scheduled swaps |
| Tier 3 | 0.18% | Risk/urgency — must execute or user loses money | Liquidation prevention, auto-repay, stop-loss |

### V1 vs V2 classification

- **V1 (current):** Rule-based. All workflows with on-chain nodes default to Tier 1.
- **V2 (future):** LLM-based classification analyzing workflow purpose. e.g., `AAVE.repay()` → Tier 3, `Uniswap.swap()` → Tier 1.

## What has no fee

| Node type | Fee | Reason |
|-----------|-----|--------|
| `contract_read` | Free | Read-only, no capital touched |
| `rest_api` | Free (value-capture), but may incur external API COGS | Off-chain call |
| `graphql_query` | Free | Off-chain query |
| `custom_code` | Free | Logic/computation only |
| `branch` | Free | Control flow |
| `filter` | Free | Data filtering |
| `balance` | Free | Read-only check |

## Operational costs (COGS)

Gas costs are estimated per on-chain node via `estimateCOGS()`, returned as `NodeCOGS` entries with `Fee{amount, unit: "WEI"}`. Gas estimation is independent of the value-capture tier.

Default gas estimates:
- `contract_write` — 150,000 gas units
- `eth_transfer` — 50,000 gas units
- `loop` — 300,000 gas units

Smart wallet creation (if the user's wallet doesn't exist yet) is included as a COGS entry with `cost_type: "wallet_creation"`.

**External API costs (future):** When a workflow calls a paid external API (e.g., X/Twitter search), that COGS will be estimated and included in the `cogs[]` array.

## Configuration

Fee rates are optional. Pointer-based YAML parsing: omitted fields use defaults, explicit `0.0` configures free tiers.

```yaml
fee_rates:
  execution_fee_usd: 0.02   # Flat per-execution platform fee
  tiers:
    tier_1: 0.03             # Value-capture group 1
    tier_2: 0.09             # Value-capture group 2
    tier_3: 0.18             # Value-capture group 3
```

**Beta (free):**
```yaml
fee_rates:
  execution_fee_usd: 0.0
  tiers:
    tier_1: 0.0
    tier_2: 0.0
    tier_3: 0.0
```

## Billing & Settlement

The API returns fees in mixed units — each field is self-describing via `Fee.unit`.

### Enforcement split

| Component | Unit | Enforcement | Rationale |
|-----------|------|-------------|-----------|
| `execution_fee` | USD | **Converted to ETH at execution time, included in UserOp** | Reverts if insufficient balance |
| `cogs` (gas, API) | WEI | **Atomic in UserOp** — native token cost | Blockchain rejects if insufficient |
| `value_fee` | PERCENTAGE | **Post-paid** — tracked off-chain after execution | User experiences value first |

No account balance system or prepaid deposits. The blockchain enforces hard costs — if the smart wallet doesn't have enough ETH, the UserOp reverts automatically.

### Settlement flow

```
1. Build UserOp:
   - Include workflow contract calls
   - Include execution_fee (converted USD→ETH) + COGS transfer to Ava

2. Submit to bundler:
   - Sufficient balance → executes → fees collected atomically
   - Insufficient balance → reverts → nothing happens

3. After execution, calculate value_fee:
   - value_fee_eth = (tier_percentage × tx_value) / eth_price
   - Track as outstanding balance (may go negative)

4. If outstanding value_fee not repaid:
   - Block future executions until settled
```

## Implementation

### Key files

| File | Purpose |
|------|---------|
| `core/taskengine/fee_estimator.go` | `EstimateFees()`, `estimateCOGS()`, `classifyWorkflowValue()`, `buildCOGSFromSteps()` |
| `core/config/config.go` | `FeeRatesConfig` struct, YAML parsing with pointer fields |
| `protobuf/avs.proto` | `Fee`, `NativeToken`, `NodeCOGS`, `ValueFee`, `EstimateFeesResp`, `Execution` |
| `config/aggregator.example.yaml` | Reference YAML with fee config |

### Fee estimation flow

```
EstimateFees(req)
  ├─ resolveRunnerAndWalletCreation() → runner address + wallet creation COGS (if needed)
  ├─ execution_fee                    → Fee{amount: "0.02", unit: "USD"}
  ├─ estimateCOGS()                   → NodeCOGS[] with Fee{amount, unit: "WEI"} per on-chain node
  ├─ classifyWorkflowValue()          → ValueFee{fee: Fee{amount, unit: "PERCENTAGE"}, tier, ...}
  └─ response                         → flat EstimateFeesResp (no totals, client computes)
```

### Proto messages

```protobuf
message Fee {
  string amount = 1;    // Numeric value as string
  string unit = 2;      // "USD", "WEI", "PERCENTAGE"
}

message NativeToken {
  string symbol = 1;    // e.g., "ETH"
  int32 decimals = 2;   // e.g., 18
}

message NodeCOGS {
  string node_id = 1;
  string cost_type = 2; // "gas", "external_api", "wallet_creation"
  Fee fee = 3;           // {amount: "150000000000000", unit: "WEI"}
  string gas_units = 4;
}

message ValueFee {
  Fee fee = 1;                       // {amount: "0.03", unit: "PERCENTAGE"}
  ExecutionTier tier = 2;
  string value_base = 3;            // "input_token_value"
  string classification_method = 4; // "rule_based" or "llm"
  float confidence = 5;
  string reason = 6;
}
```

### EstimateFeesResp example responses

**Workflow 1: Alert-only** (contract_read → branch → rest_api)
```json
{
  "success": true,
  "chain_id": "11155111",
  "native_token": { "symbol": "ETH", "decimals": 18 },
  "execution_fee": { "amount": "0.020000", "unit": "USD" },
  "cogs": [],
  "value_fee": {
    "fee": { "amount": "0", "unit": "PERCENTAGE" },
    "tier": "EXECUTION_TIER_UNSPECIFIED",
    "value_base": "",
    "classification_method": "rule_based",
    "confidence": 1.0,
    "reason": "Workflow has no on-chain execution nodes — no value-capture fee"
  },
  "discounts": [],
  "pricing_model": "v1"
}
```

**Workflow 2: Simple swap** (contract_read → branch → contract_write)
```json
{
  "success": true,
  "chain_id": "11155111",
  "native_token": { "symbol": "ETH", "decimals": 18 },
  "execution_fee": { "amount": "0.020000", "unit": "USD" },
  "cogs": [
    {
      "node_id": "write1",
      "cost_type": "gas",
      "fee": { "amount": "2575744500000", "unit": "WEI" },
      "gas_units": "150000"
    }
  ],
  "value_fee": {
    "fee": { "amount": "0.03", "unit": "PERCENTAGE" },
    "tier": "EXECUTION_TIER_1",
    "value_base": "input_token_value",
    "classification_method": "rule_based",
    "confidence": 1.0,
    "reason": "V1 default: workflow contains on-chain execution nodes"
  },
  "discounts": [],
  "pricing_model": "v1"
}
```

**Workflow 3: Liquidation protection** (contract_read → branch → contract_write → eth_transfer, new wallet)
```json
{
  "success": true,
  "chain_id": "11155111",
  "native_token": { "symbol": "ETH", "decimals": 18 },
  "execution_fee": { "amount": "0.020000", "unit": "USD" },
  "cogs": [
    {
      "node_id": "repay1",
      "cost_type": "gas",
      "fee": { "amount": "2575744500000", "unit": "WEI" },
      "gas_units": "150000"
    },
    {
      "node_id": "transfer1",
      "cost_type": "gas",
      "fee": { "amount": "858581500000", "unit": "WEI" },
      "gas_units": "50000"
    },
    {
      "node_id": "_wallet_creation",
      "cost_type": "wallet_creation",
      "fee": { "amount": "6730592094800", "unit": "WEI" }
    }
  ],
  "value_fee": {
    "fee": { "amount": "0.03", "unit": "PERCENTAGE" },
    "tier": "EXECUTION_TIER_1",
    "value_base": "input_token_value",
    "classification_method": "rule_based",
    "confidence": 1.0,
    "reason": "V1 default: workflow contains on-chain execution nodes"
  },
  "discounts": [],
  "pricing_model": "v1",
  "warnings": ["Gas estimates use conservative fallback values. Actual costs may vary."]
}
```

### Execution response (after task runs)

The `Execution` message uses the same `Fee`/`NodeCOGS`/`ValueFee` structure:

```protobuf
message Execution {
  Fee execution_fee = 7;            // Fee{amount: "0.020000", unit: "USD"}
  repeated NodeCOGS cogs = 9;      // Actual gas from step receipts (via buildCOGSFromSteps)
  ValueFee value_fee = 10;         // Workflow-level value fee (via buildValueFee)
}
```

- `execution_fee` — always populated from config (known upfront)
- `cogs[]` — populated from actual step-level gas receipts after execution (real costs, not estimates)
- `value_fee` — populated after execution based on step composition (nil for pending executions where steps are unknown)
- Step-level gas tracking (`gas_used`, `gas_price`, `total_gas_cost` per step) is preserved as raw execution data

### Total fee formula

```
Total per execution = execution_fee + Σ(cogs) + (value_fee.percentage × tx_value)
                      |_______atomic (in UserOp)_______|   |____post-paid____|
```

`execution_fee` and `cogs` are known at estimation time. `value_fee` depends on actual transaction value at execution time. No totals in the API — client computes.
