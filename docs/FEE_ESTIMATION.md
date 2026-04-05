# Fee Estimation

## Overview

Workflow fees have two independent components:

1. **Operational costs** — gas fees for on-chain nodes, external API costs for paid services. These are estimated upfront and cover the cost of execution.
2. **Value-capture fees** — a percentage of transaction value on on-chain execution nodes. This is the revenue model, based on urgency/importance of the action.

Non-execution nodes (logic, reads, API calls) have **no fees**. We do not charge for logical computation.

## Value-Capture Tiers

Only on-chain execution nodes (`contract_write`, `eth_transfer`, `loop`) have value-capture fees. Tiers are pure pricing groups — meaning comes from classification logic, not the label.

### Decision rule

> If this action **fails or is delayed**, does the user **lose money immediately**?
>
> **YES** → Tier 3
> **NO, but improves outcome** → Tier 2
> **Simple execution** → Tier 1

### Tier definitions

| Tier | Default % | Classification criteria | Examples |
|------|-----------|----------------------|----------|
| Tier 1 | 0.03% | Simple execution — user could do manually | ETH transfers, simple swaps |
| Tier 2 | 0.09% | Optimization/convenience — nice to have, delay doesn't cause major loss | Rebalancing, DCA, scheduled swaps |
| Tier 3 | 0.18% | Risk/urgency — must execute or user loses money, time-sensitive | Liquidation prevention, auto-repay, stop-loss, emergency exits |

### V1 vs V2 classification

- **V1 (current):** All on-chain nodes default to Tier 1. Classification by protocol + action is not yet implemented.
- **V2 (future):** Classify by protocol + action type. For example:
  - `Uniswap.swap()` → Tier 1
  - `AAVE.repay()` → Tier 3
  - Rebalancing workflows → Tier 2

## What has no fee

| Node type | Fee | Reason |
|-----------|-----|--------|
| `contract_read` | Free | Read-only, no capital touched |
| `rest_api` | Free (value-capture), but may incur external API costs | Off-chain call |
| `graphql_query` | Free | Off-chain query |
| `custom_code` | Free | Logic/computation only |
| `branch` | Free | Control flow |
| `filter` | Free | Data filtering |
| `balance` | Free | Read-only check |

## Operational costs (estimated separately)

### Gas fees

Gas costs are estimated per on-chain node via `estimateCOGS()`, which returns a `NodeCOGS` entry per node. This covers the blockchain execution cost (EntryPoint, bundler, smart wallet operations). Gas estimation is independent of the value-capture tier.

Nodes that incur gas:
- `contract_write` — 150,000 gas estimate (conservative)
- `eth_transfer` — 50,000 gas estimate (smart wallet transfer)
- `loop` — 300,000 gas estimate (may contain contract writes)

### External API costs (future)

When a workflow calls a paid external API (e.g., X/Twitter search API at $0.01/request), that cost is our COGS. This will be estimated upfront like gas — the cost is passed through to the user. Not yet implemented.

## Configuration

Fee rates are optional. When not configured, hardcoded defaults are used.

```yaml
# In aggregator YAML config
fee_rates:
  tiers:
    tier_1: 0.03     # Default pricing group
    tier_2: 0.09     # Mid pricing group
    tier_3: 0.18     # High pricing group
```

### Pricing strategies

**Beta (free execution):**
```yaml
fee_rates:
  tiers:
    tier_1: 0.0
    tier_2: 0.0
    tier_3: 0.0
```

**Premium (higher rates):**
```yaml
fee_rates:
  tiers:
    tier_1: 0.05
    tier_3: 0.25
```

## Billing & Settlement

The API returns fees in mixed units: `execution_fee` in **USD**, `cogs` in **WEI**, `value_fee` in **PERCENTAGE**. Each field is self-describing via its `unit`. At settlement time, USD amounts are converted to native token (ETH) using the oracle price.

### Enforcement split

| Component | Unit | Enforcement | Rationale |
|-----------|------|-------------|-----------|
| `execution_fee` | USD | **Converted to ETH and included in UserOp** | Operational cost — converted at execution time, reverts if insufficient |
| `cogs` (gas, API) | WEI | **Atomic in UserOp** — native token cost | Real cost — blockchain rejects if insufficient balance |
| `value_fee` | PERCENTAGE | **Post-paid** — tracked off-chain after execution | Value-capture revenue — user experiences value first, pays after |

No account balance system or prepaid deposits. The blockchain is the enforcement layer for hard costs — if the user's smart wallet doesn't have enough ETH, the UserOp reverts automatically.

### How atomic fees work

The `execution_fee` and `cogs` are packaged into the UserOp alongside the workflow's contract calls. The smart wallet must have enough ETH to cover all of it. If not, the entire transaction is rejected by the blockchain — no execution happens, no cost to Ava.

### Why post-paid value fees?

1. **UX** — User gets value first, pays after. Critical for urgent workflows (liquidation protection).
2. **Bounded risk** — Ava only exposes margin (value_fee), never operational costs (gas/infra).
3. **Effective free trial** — Worst case is one unpaid value_fee per user, which is a customer acquisition cost.

### Settlement flow

```
1. Build UserOp:
   - Include workflow contract calls (swap, repay, transfer, etc.)
   - Include execution_fee + COGS transfer to Ava's address

2. Submit UserOp to bundler:
   - Wallet has enough ETH → executes → fees collected atomically
   - Wallet doesn't have enough ETH → reverts → nothing happens

3. After successful execution, calculate value_fee:
   - value_fee_eth = (tier_percentage × tx_value) / eth_price_at_execution
   - Track as outstanding balance (may go negative)

4. If outstanding value_fee not repaid:
   - Block future executions until settled
```

## Implementation

### Key files

| File | Purpose |
|------|---------|
| `core/taskengine/fee_estimator.go` | Fee estimation: COGS, value classification, execution fee |
| `core/config/config.go` | `FeeRatesConfig` struct, YAML parsing, defaults |
| `protobuf/avs.proto` | `EstimateFeesResp`, `NodeCOGS`, `ValueFee`, `ExecutionTier` |
| `config/aggregator.example.yaml` | Reference YAML with fee config |

### Fee estimation flow

```
EstimateFees(req)
  ├─ estimateSmartWalletCreation()   → wallet deployment gas (if needed)
  ├─ execution_fee                   → flat per-run platform fee ($0.02)
  ├─ estimateCOGS()                  → per-node gas costs (contract_write, eth_transfer, loop)
  ├─ classifyWorkflowValue()         → workflow-level tier classification (V1: rule-based)
  └─ total                           → execution_fee + COGS (value_fee settled post-execution)
```

### Response structure

`EstimateFeesResp` (flat, not nested):
- `chain_id`, `native_token` — chain context
- `execution_fee` — `Fee{amount, unit: "USD"}` flat per-run platform fee
- `cogs[]` — `NodeCOGS{fee: Fee{amount, unit: "WEI"}}` per-node operational costs
- `value_fee` — `ValueFee{fee: Fee{amount, unit: "PERCENTAGE"}}` workflow-level value capture
- `discounts[]` — any applied fee discounts
- `pricing_model` — `"v1"`
- `warnings` — non-fatal estimation notes

No totals — client computes. No `estimated_executions` — all fees are per-execution.

### Total workflow fee formula

```
Total per execution = execution_fee + Σ(cogs) + (tier_percentage × tx_value)
                      |____________atomic_____________|   |___post-paid___|
```

`execution_fee` and `cogs` are known at estimation time. `value_fee` depends on actual transaction value at execution time.
