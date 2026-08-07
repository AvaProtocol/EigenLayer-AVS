# Retiring the v0.6 send path and the self-funded VerifyingPaymaster

**Date:** 2026-08-07
**Status:** Implemented
**Branch:** `chore/retire-v06-send-path`
**Related:** [#718](https://github.com/AvaProtocol/EigenLayer-AVS/issues/718), [#713](https://github.com/AvaProtocol/EigenLayer-AVS/pull/713) (v0.7 cutover)

## Summary

Every chain runs Modular Account v2 on EntryPoint v0.7 and sponsors gas through
the Alchemy Gas Manager policy. The v0.6 send path and the self-funded
`VerifyingPaymaster` were retired in deployment but still present in code. Both
are now gone: ~3,200 lines removed, ~160 added.

## Problem

`SendUserOpAuto` dispatched per chain and still carried a v0.6 branch:

```go
if smartWalletConfig.UsesModularAccountV2() { ... SendUserOpMAv2(...) }
return SendUserOp(smartWalletConfig, owner, callData, paymasterReq, ...)
```

That `else` was reachable from five call sites — contract write, ETH transfer,
`rpc_server` (×2), `worker/server` — whenever a chain set
`account_provider: simple_account`. No deployed chain did, but nothing stopped
one from doing so, and the branch it led to targeted an EntryPoint the fleet no
longer transacts against.

The two live tests covering it (`TestSendUserOp`,
`TestUserOpExecutionSuccessWithPaymaster`) had also been failing CI on every PR
with `AA31 paymaster deposit too low`, because the Sepolia paymaster's
EntryPoint deposit had drained. `pkg/erc4337/preset/builder_test.go` already
recorded the intended resolution: *"When the v0.6 path is removed post-cutover,
this test goes with it."* — the path first, the test following.

## Decision

Remove the path and its tests together, rather than deleting the tests and
leaving a config-selectable production branch with no coverage.

- **`SendUserOp` / `SendUserOpWithWsClient` deleted** along with the rest of
  `builder.go` (1,634 lines). `SendUserOpAuto` is now the single entry point and
  calls `SendUserOpMAv2` directly.
- **`SendUserOpAutoWithWsClient` deleted.** It existed so the aggregator's
  long-lived WebSocket could be reused for receipt watching; the MA v2 path
  opens its own, so there was nothing left to reuse.
- **`paymasterReq` and `executionFeeWei` removed from the send signature.** Both
  applied only to the v0.6 verifying paymaster and were already documented as
  ignored on the MA v2 path — parameters that quietly meant nothing.
- **`waitForUserOpConfirmation` kept**, moved to `receipt_watch.go`. It is the
  one piece of `builder.go` the v0.7 path uses, and it is EntryPoint-version
  agnostic: the caller supplies which EntryPoint to watch and the
  `UserOperationEvent` layout is identical across versions.
- **Paymaster probing removed from config load.** `owner()` / `verifyingSigner()`
  were called at boot on a contract nothing invokes, making startup depend on a
  live RPC round-trip for an unused address.
  `smart_wallet.paymaster_address` still parses so existing configs load, and is
  logged once as ignored.

### `account_provider: simple_account` is refused at load

This was the open question in #718, and the evidence settled it: **no avs-infra
config sets `account_provider` at all**, so every deployed chain takes the
`modular_account_v2` default. Refusing `simple_account` therefore rejects a
value nothing is using.

Accepting it and failing at send time was the alternative. Rejected: a boot
failure names the config line, whereas a send-time failure names nothing and
surfaces per operation, after users have already been handed legacy addresses.

## Alternatives

- **Fund the paymaster and keep the tests.** Buys a green run for a path nothing
  uses; the deposit drains again. (The deposit was in fact topped up separately
  to unblock CI — this change removes the reason it was needed.)
- **Delete the tests only.** Leaves a config-selectable production branch
  untested — a silent trapdoor rather than a loud failure.
- **Keep `simple_account` accepted but inert.** Derivation would still hand out
  v0.6 addresses that no send path can execute against.

## Also removed, as consequences

- `shouldUsePaymaster` on both contract-write and ETH-transfer processors: it
  only ever gated the v0.6 request, and already returned false on MA v2.
- The MAX-transfer `SkipReimbursement` special case — there is no reimbursement
  leg to skip.
- `DEFAULT_CALL_GAS_LIMIT` / `DEFAULT_VERIFICATION_GAS_LIMIT` /
  `DEFAULT_PREVERIFICATION_GAS`, and the AA21 log block that compared against
  them to hide "not yet estimated" placeholders. The v0.7 path seeds different
  values, so that comparison had stopped hiding anything and only obscured the
  gas figures it was meant to filter.
- `ExecuteUserOpReq.UsePaymaster` is no longer set by the aggregator or read by
  the worker. Left on the wire for older callers rather than removed, which
  would be a proto break.

## Out of scope

v0.6 **derivation** and storage — `DefaultFactoryProxyAddressHex`,
`aa.NewSimpleFactory`, the v0.6 factory on wallet records. Existing wallet rows
still reference it, and `ProviderForFactory` still has to recognise it to route
per wallet. This change is about the SEND path.

The pre-cutover wallet audit recorded in `SendUserOpAuto`'s comment — that
legacy v0.6 SimpleAccounts hold no meaningful balances — is not re-run here.
Those wallets already cannot execute on a cut-over chain; this makes that
permanent rather than reversible by config, so it is worth re-confirming before
merge.

## Verification

- `go build ./...`, `go vet ./...` clean.
- `core/config`, `pkg/erc4337/...`, `worker`, `aggregator/...` suites green.
- `TestSimpleAccountIsRefusedAtLoad` pins the new refusal and that its message
  names the supported value.
- `TestValidateAccountProvider` updated: empty and `modular_account_v2` accepted,
  `simple_account` no longer.
- `Unit Test (pkg/erc4337/preset)` no longer depends on a funded paymaster.
