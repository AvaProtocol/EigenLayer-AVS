# Fix: ERC20 Approve State Propagation in Workflow Simulation

- **Date**: 2026-04-16
- **Status**: Implemented
- **Branch**: `test/uniswap-simulate-propagation`
- **Related**: #413, #517

## Context

When simulating a Uniswap stop-loss workflow (source of truth: `studio/templates/stop-loss-on-uniswap.json`), the `approve()` node succeeded but its allowance state did not propagate to the downstream `exactInputSingle()` swap node. The swap consistently failed with `ERC20: transfer amount exceeds allowance`.

PR #517 introduced `SimulationStateMap` with `MergeRawStateDiff` to carry Tenderly's `raw_state_diff` between sequential simulation steps. The assumption was that Tenderly returns storage changes in that field, which are then fed as `state_objects` overrides into the next simulation.

## Root cause

Tenderly's HTTP `/simulate` endpoint (with `simulation_type: "full"`) returns `raw_state_diff: null` for `approve()` calls. The field key exists in the response at `transaction.transaction_info.raw_state_diff`, but the value is always `null`. This makes `MergeRawStateDiff` a silent no-op for the approve step, so the downstream swap never sees the new allowance.

### Diagnosis path

1. Wrote an integration test (`TestSimulateTask_StopLossWorkflow_Sepolia`) using a salt:0 wallet with real Sepolia USDC balance but **zero** on-chain allowance to SwapRouter02. This made the test conclusive: swap can only succeed if the approve's allowance propagates.
2. Confirmed propagation failure: approve succeeded, swap failed with `ERC20: transfer amount exceeds allowance`.
3. Added a raw Tenderly HTTP diagnostic test confirming `raw_state_diff` is `null` (not absent, not empty array — literally `null`).
4. Verified the extraction path (`transaction.transaction_info.raw_state_diff`) is correct by inspecting all response keys — the code was looking in the right place, but there was no data to extract.

## Fix

After a successful `approve()` simulation, explicitly compute and inject the `allowance[owner][spender]` storage slot into `SimulationStateMap`. This bypasses the missing `raw_state_diff` by directly setting the slot that `transferFrom` will check during the swap.

### Changes

| File | Change |
|---|---|
| `core/taskengine/simulation_state.go` | Added `erc20AllowanceSlot(owner, spender, mappingSlot)` — computes `keccak256(spender, keccak256(owner, slot))` for nested mapping lookup. Added `commonAllowanceSlots` (indices 1, 2, 3, 10) covering OpenZeppelin, legacy, and USDC implementations. |
| `core/taskengine/vm_runner_contract_write.go` | Added `isMethodWithParams(methodName, target, params, minParams)` predicate. After approve sim succeeds, injects the allowance value across all common slots. Extracted `simSuccess` variable to deduplicate the repeated `simulationState != nil && simulationResult != nil && simulationResult.Success` guard. |
| `core/taskengine/simulate_uniswap_workflow_test.go` | Integration test: approve→swap via `SimulateTask` on Sepolia with zero on-chain allowance. Derived from the Studio stop-loss-on-uniswap template. Includes diagnostic output on failure and balance pre-flight check. |

### How it works

```
approve(SwapRouter02, 4 USDC) simulation succeeds
    ↓
isMethodWithParams(methodName, "approve", params, 2) → true
    ↓
For each slot in commonAllowanceSlots [1, 2, 3, 10]:
    slotHash = keccak256(spender || keccak256(owner || slot))
    simulationState.SetStorageSlot(USDC, slotHash, 0x...amount)
    ↓
exactInputSingle() simulation runs with state_objects
    containing allowance[wallet][router] = 4 USDC
    ↓
transferFrom succeeds → swap succeeds
```

## What this does NOT fix

- **Tenderly `raw_state_diff` being null**: the Tenderly API behavior is unchanged. If they fix it in the future, `MergeRawStateDiff` will start working and the explicit injection becomes a harmless redundancy.
- **Arbitrary state propagation between nodes**: only `approve()` gets explicit injection. Other methods still rely on `raw_state_diff` (which works for non-approve calls where Tenderly does return diffs).
- **User-facing `erc20_overrides` API** (#413): users still cannot seed arbitrary ERC20 state in `RunNodeImmediately`. That requires a new proto field and is tracked separately.

## Sepolia test addresses

| Name | Address | Source |
|---|---|---|
| USDC | `0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238` | `studio/app/lib/erc20/sepolia.json` |
| WETH | `0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14` | same |
| SwapRouter02 | `0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E` | `studio/app/lib/uniswap/v3/data/sepolia.json` |
| QuoterV2 | `0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3` | same |
| Test wallet | salt:0 derived from `OWNER_EOA` | zero on-chain USDC allowance to SwapRouter02 |
