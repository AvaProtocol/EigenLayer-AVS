# ETH transfer MAX without gas sponsorship

- **Date**: 2026-11-14
- **Status**: Implemented; the live check is pending
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
   - set the value (change 3);
   - re-price, with ceilings that only rise, until the calldata stops changing;
   - then sign and send, as today.

   Move the owner loop into a helper both paths call, so there's one reserve
   rule.
3. **The value: what the balance and the permission both allow.** Let `B` be
   the wallet's balance, `G` the signed maximum gas cost, `D` its EntryPoint
   deposit and `C` the native permission's remaining limit.

   - **Balance.** A v0.7 account pays only the prefund its deposit doesn't
     cover, so the balance must keep `max(0, G − D)`. The owner path reserved
     all of `G`, which leaves up to that much behind when a deposit exists.
     Sweep's ETH step has one: the token sends' unused prefund returned to the
     deposit.
   - **Permission.** NativeTokenLimitModule debits the whole `G` at validation,
     whatever pays it, and the value at execution
     ([v2.0.0](https://github.com/alchemyplatform/modular-account/blob/v2.0.0/src/modules/permissions/NativeTokenLimitModule.sol)).
     So the permission needs value + `G`.

   `value = min(B − max(0, G − D), C − G)`.

   The balance term alone would charge `B + min(G, D)`, not `B`. With `B` =
   100, `G` = 10 and `D` = 5 it sends 95 and needs 105 of permission, and a
   remaining limit of 100 fails the step. With the permission term it sends 90
   and leaves 5 in the wallet.

   `G` only rises between rounds, so the value only falls, and the loop still
   converges.

   - Read `C` on chain with `aa.ReadNativeSpendLimit`, as `CheckBudgetSpend`
     does. The token sends before the ETH step have already spent part of the
     permission. `PreflightNativePermission` compares against the granted cap,
     which overstates what's left.
   - A failed limit read refuses the step. A failed deposit read takes `D` = 0.
     Never assume either.
   - An operation that carries the grant's install, such as the ETH step of a
     sweep with no tokens, starts from the granted limit. On chain the limit
     reads 0 until that operation's validation installs it.
   - If the value comes out at zero or less, refuse with a typed error:
     `NATIVE_BALANCE_BELOW_FEE` when the balance term is the smaller one,
     `NATIVE_CAP_BELOW_FEE` when the permission term is.
4. **Checks before sending.** `preflightSessionGrantError` checked the full
   balance, which refused a permission that covers the gas but not all the
   ETH. For a self-funded `MAX` it now checks 0 wei: the recipient, the
   grant's state, and that the permission covers the step's own gas. The send
   reads the remaining limit, sizes the value to it (change 3) and asserts
   value + `G` ≤ `C` before signing. A sponsored `MAX` and a fixed amount keep
   today's check.
5. **Output.** The step's `value`, transfer object and log report the amount
   actually sent, not the balance. Metadata adds `gasReserve` in wei, so Studio
   can say "minus the network fee". When the permission term set the value, it
   also adds `limitedBy: "permission"`. When any ETH stays in the wallet, it
   adds `leftInWallet` in wei. A loop iteration's output carries the same
   fields in its `metadata`, next to `transactionHash` (#915), so Sweep's ETH
   loop reads like a standalone send. The result describes the operation as
   signed, after any bundler retry (Edge cases). `NATIVE_BALANCE_BELOW_FEE` carries
   `INSUFFICIENT_BALANCE`; `NATIVE_CAP_BELOW_FEE` keeps its code in the
   message, like the other session-policy refusals. Both carry the amounts as
   error details.
6. **Simulation.** The simulation path showed `MAX` as the full balance, with
   a stand-in fee. A self-funded `MAX` now applies the same rule as the send,
   so Test shows about what Run sends:
   - `G` is the gas the preflight prices for this grant (the steady operation
     once the grant is applied, the install before) at the current max fee.
   - `D` is the wallet's EntryPoint deposit; a failed read takes 0.
   - `C` is the remaining limit, or the granted limit while the install is
     pending; a failed read refuses.
   - The permission check uses 0 wei, as Run's does. The grant report still
     observes the full balance, so Review's sizing (#909) is unchanged.
   - In a grant report, a permission that doesn't cover the fee is noted as a
     native cap miss, so Review says the cap is too low, and Test shows what
     the balance allows. A strict simulation refuses, as Run does.
7. **Wait for earlier operations.** `MAX` reads the balance only after the
   run's earlier operations from the same wallet are included. A token send
   still pending, after a receipt timeout, hasn't paid its prefund yet, so a
   balance read before then would overstate the ETH. The prerequisite gate
   (`reconciledWritePrerequisite`) already holds a write behind its graph
   ancestors, which covers Sweep's token loop. `MAX` also checks the run's
   other earlier writes on the same chain, reconciles each once, and fails
   with `WRITE_PREREQUISITE_FAILED` while one may still be pending.

## A short permission completes part of a sweep

Each step checks only its own operation, when it reaches it. The ETH step's
check (`executeRealETHTransfer`) runs after the token loop has sent its
operations, and the workflow's admission checks the permission's state and
authority, not the run's native total. So with a permission that covers the
token sends but not all the ETH:

- the token sends go through;
- the ETH step sends what the remaining limit allows (change 3), and the
  output says how much stayed;
- if the remaining limit doesn't cover the ETH step's own gas, the ETH step
  fails with `NATIVE_CAP_BELOW_FEE` and the ETH stays.

This plan doesn't add a check of the whole run before its first send. Every
transfer that does happen goes to the reviewed destination, and the rest stays
in the wallet, so rerunning with a larger permission finishes the sweep. Such a
check would also need to know the run's remaining operations when the first
token send starts. #909 sizes them at Review from `sweepPlan`'s loops; the
runner doesn't have them. It is listed under Out of scope.

A correctly sized permission (below) can still end this way. A fee rise, ETH
received after Review, or another Skill on the wallet can each change the
numbers between Review and the ETH step.

## Edge cases

- **Balance below the fee:** the ETH step fails with `NATIVE_BALANCE_BELOW_FEE`
  and plain copy in Studio ("Not enough ETH left to pay the network fee"). The
  token sends before it have already happened.
- **Permission below the ETH step's gas:** the ETH step fails with
  `NATIVE_CAP_BELOW_FEE` after the token sends (above).
- **Fee rises after pricing:** no shortfall. The signed `maxFeePerGas` caps
  the prefund, and the reserve was computed from it.
- **Bundler retry:** the send re-signs only when it tightens the verification
  gas limit after an efficiency rejection. A lower limit lowers `G`, which
  frees part of the reserve. Before re-signing, the reserve sizes the value
  again from the new `G`. It keeps the old value only when the new calldata
  would cost more calldata gas than the bytes the bundler priced
  `preVerificationGas` for; then the freed gas stays in the wallet and the
  output reports it as `leftInWallet`. Either way the value stays covered.
  The retry re-signs through the same signer as the first send, so an
  operation that installs the grant keeps its deferred action and the
  owner's signature (#912).
- **L1 data fee on L2s:** the bundler charges it through `preVerificationGas`,
  so the max cost already includes it.
- **Another operation from the same wallet** between the balance read and
  inclusion, such as another Skill, can make the transfer revert in
  execution. The prefund is then spent. Sweep's own steps run in order;
  another Skill on the same wallet remains a small risk.
- **What stays behind:** when the permission covers it, the wallet's balance
  ends at zero, and the unused prefund sits in its EntryPoint deposit, where it
  pays the wallet's next operation. When the permission term sets the value,
  the balance keeps the difference, which the output reports. Withdrawing the
  deposit (`withdrawTo`) is out of scope.

## Studio and #909

- **Native cap.** #909 marks Sweep's ETH step `NativeUnsized` /
  `CapNeedsInput`, so the owner sets the ETH cap. The module charges every
  self-funded operation's whole max gas, whatever pays it, and the ETH step's
  value on top. The run charges the token sends' max gas, plus the ETH value,
  plus the ETH step's max gas. The ETH value is at most the balance at Review,
  `B₀`. So a cap that sweeps everything is

  `B₀ + Σ G(token sends) + G(ETH send)`

  at Review's fee estimate, with headroom. A deposit funded before the sweep
  doesn't lower it.

  Review sizes the cap that way, or says the ETH step may leave some ETH
  behind. A smaller cap completes part of the sweep (above).
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
  - wrapped calldata is rebuilt each round;
  - the owner path behaves as before after the move.
- **Value against deposit and permission.** With `B` = 100 and `G` = 10, the
  deposit at zero, partial and full coverage, each against the remaining
  limit:

  | `D` | `C` ≥ `B` + `D` | `C` = `B` | `C` ≤ `G` |
  | --- | --- | --- | --- |
  | 0 | sends 90 | sends 90 | `NATIVE_CAP_BELOW_FEE` |
  | 5 | sends 95 | sends 90, 5 stays | `NATIVE_CAP_BELOW_FEE` |
  | 10 | sends 100 | sends 90, 10 stays | `NATIVE_CAP_BELOW_FEE` |

  - A failed limit read refuses.
  - A failed deposit read takes `D` = 0.
- **Runner:**
  - unsponsored `MAX` sends the value from change 3;
  - sponsored `MAX` sends the whole balance;
  - the output reports the amount sent, and `limitedBy` when the permission
    set it;
  - the check counts value plus gas against the remaining limit, not the
    granted cap;
  - simulation shows the estimate.
- **Token sends, then ETH, with a short permission** (two cases):
  - The permission covers the token sends but not all the ETH. The tokens are
    sent, and the ETH step sends what's left of the permission and reports
    the rest.
  - The permission covers the token sends but not the ETH step's gas. The
    tokens are sent, and the ETH step fails with `NATIVE_CAP_BELOW_FEE`.
- **Live, Sepolia, local gateway with sponsorship off:** a Sweep of USDC and
  ETH, with a cap sized as Review sizes it. Check that:
  - the destination got the reported value;
  - the wallet's ETH balance is zero;
  - the leftover sits in the deposit.

## Out of scope

- The node's `reserveGas` field stays owner-only. The session path keys on
  `MAX`.
- Withdrawing the EntryPoint deposit as part of a sweep.
- A native check of the whole run before its first send. It would turn a
  partial sweep into a refusal before anything moves. It needs the run's
  remaining operations at the first send, which the runner doesn't have
  today.
