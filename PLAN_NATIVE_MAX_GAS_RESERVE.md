# ETH transfer MAX without gas sponsorship

- **Date**: 2026-11-14
- **Status**: Proposed
- **Related**: Studio #2122 (Sweep as a session Skill), #909 (Sweep's session sizing), the owner path's native reserve (`pkg/erc4337/preset/owner_native.go`)

## Problem

An ETHTransfer node with amount `MAX` sends the smart wallet's whole ETH
balance. On a chain with no Alchemy Gas Manager policy, the runner refuses it
(`core/taskengine/vm_runner_eth_transfer.go`, in `Execute`):

> cannot use MAX amount without sponsorship: set alchemy_paymaster_policy_id,
> or leave a gas reserve and transfer a fixed amount

The reason is real: the wallet pays its own gas from the same balance, so
sending all of it can't pay the prefund. But it means Sweep's last step, "Send
the ETH", works only on a sponsored chain. That excludes the local gateway
config and any chain where the swept wallet should pay its own gas.

The fix can't live in the workflow. Sweep's `sweepPlan` Code node runs in the
JavaScript sandbox with no RPC, no gas price and no view of the operation's gas
limits. The token transfers before the ETH step also pay gas from that ETH, so
a fixed reserve would either strand ETH or fail. Only the send path knows the
operation's maximum gas cost.

## What exists

- **The owner path already does this.** `OwnerBatch.ReserveNativeGas` with
  `priceOwnerBatch` and `OwnerBatch.Resolve` (`owner_native.go`) price the
  batch with the native value at 1 wei. They then set the value to the cap
  minus `MaxOwnerGasCost(op)` and re-estimate until the calldata stops
  changing, up to 8 rounds. Gas ceilings only rise between rounds, so the
  reserve can't oscillate. If it doesn't converge, or the balance doesn't cover
  the fee, preparation is refused. `MaxOwnerGasCost` is (call + verification +
  pre-verification gas, plus paymaster gas when present) × `maxFeePerGas`, the
  most the EntryPoint can take.
- **The session send prices after the runner fixes the amount.** The runner
  reads the balance for `MAX`, packs `execute(destination, balance, "")`, and
  calls `SendUserOpAuto` → `SendUserOpMAv2` (`send_v07.go`). That function
  wraps the calldata for hooks (`WrapExecuteUserOp`), prices it
  (`priceOperationV07`), signs with the session key and sends. Nothing
  revisits the value after pricing.

## Change

1. **Runner.** For `MAX` on an unsponsored chain, don't refuse. Read the
   balance as today and pass it to the send as a reserve cap. Sponsored `MAX`
   is unchanged and sends the whole balance.
2. **Send path.** `SendUserOpMAv2` takes an optional terminal native reserve:
   the recipient and the cap. Each round rebuilds the calldata through the same
   pack-and-wrap code, so a grant with execution hooks gets wrapped calldata
   every time:
   - estimate with the value at 1 wei;
   - set the value to the cap minus the reserve;
   - re-price, with ceilings that only rise, until the calldata stops changing;
   - then sign and send, as today.

   Move the owner loop into a helper both paths call, so there's one reserve
   rule. If the value comes out at zero or less, refuse with a typed error,
   `NATIVE_BALANCE_BELOW_FEE`.
3. **The reserve counts the EntryPoint deposit.** A v0.7 account pays only the
   prefund its deposit doesn't cover, so the reserve is
   `max(0, maxGasCost − deposit)`. The owner path reserved the whole max cost,
   which leaves up to that much behind when a deposit exists. When Sweep's ETH
   step follows token sends, there is one: their unused prefund returned to the
   deposit. If the deposit can't be read, reserve the full max cost; never
   assume a deposit.
4. **Session-grant preflight.** `preflightSessionGrantError` checks the full
   balance today. NativeTokenLimitModule charges value plus max gas against
   the native cap, which under the reserve totals the cap. So check the
   estimated value with the estimated gas before sending, and the exact value
   after pricing.
5. **Output.** The step's `value`, transfer object and log report the amount
   actually sent, not the balance. Metadata adds `gasReserve` in wei, so Studio
   can say "minus the network fee".
6. **Simulation.** The simulation path shows `MAX` as the full balance, with a
   stand-in fee. Show the balance minus `nativePreflightGasUnitsSteady` × the
   current max fee, so Test shows about what Run sends.
7. **Wait for earlier operations.** `MAX` reads the balance only after the
   run's earlier operations from the same wallet are included. A token send
   still pending, after a receipt timeout, hasn't paid its prefund yet, so a
   balance read before then would overstate the ETH.

## Edge cases

- **Balance below the fee:** the ETH step fails with `NATIVE_BALANCE_BELOW_FEE`
  and plain copy in Studio ("Not enough ETH left to pay the network fee"). The
  token sends before it have already happened.
- **Fee rises after pricing:** no shortfall. The signed `maxFeePerGas` caps
  the prefund, and the reserve was computed from it.
- **Bundler retry:** `SendUserOpV07WithRetry` re-signs only when it tightens
  the verification gas limit. A lower limit lowers the max cost, so the value
  stays covered.
- **L1 data fee on L2s:** the bundler charges it through `preVerificationGas`,
  so the max cost already includes it.
- **Another operation from the same wallet** between the balance read and
  inclusion, such as another Skill, can make the transfer revert in
  execution. The prefund is then spent. Sweep's own steps run in order;
  another Skill on the same wallet remains a small risk.
- **What stays behind:** after a self-funded sweep, the wallet's balance is
  zero, and the unused prefund sits in its EntryPoint deposit, where it pays
  the wallet's next operation. Withdrawing the deposit (`withdrawTo`) is out of
  scope.

## Studio and #909

- **Native cap.** #909 marks Sweep's ETH step `NativeUnsized` /
  `CapNeedsInput`, so the owner sets the ETH cap. NativeTokenLimitModule
  charges every self-funded operation's max gas against it, and the ETH step
  charges value plus max gas, which is the balance at that point. A cap that
  covers a whole sweep is the ETH balance plus the token sends' max gas.
  Review has to size it or say so. The preflight (change 4) refuses a short
  cap before anything is sent.
- **Template comment.** `sweepPlan`'s comment says ETH "needs the network's gas
  sponsorship". It must change in Studio's `templates/wallet-sweep.json` and
  #909's `sweepPlanSource` together, because they must match exactly.
- **Copy.** Review and Run describe the ETH step as "all the ETH, minus the
  network fee".

## Tests

- **Reserve helper** (`pkg/erc4337/preset`, following `owner_native_test.go`):
  - converges;
  - ceilings only rise;
  - refuses a balance at or below the fee;
  - a deposit lowers the reserve;
  - a failed deposit read falls back to the full reserve;
  - wrapped calldata is rebuilt each round;
  - the owner path behaves as before after the move.
- **Runner:**
  - unsponsored `MAX` sends the balance minus the reserve;
  - sponsored `MAX` sends the whole balance;
  - the output reports the amount sent;
  - the preflight counts value plus gas;
  - simulation shows the estimate.
- **Live, Sepolia, local gateway with sponsorship off:** a Sweep of USDC and
  ETH. Check that the destination got the reported value, the wallet's ETH
  balance is zero, and the leftover sits in the deposit.

## Out of scope

- The node's `reserveGas` field stays owner-only. The session path keys on
  `MAX`.
- Withdrawing the EntryPoint deposit as part of a sweep.
