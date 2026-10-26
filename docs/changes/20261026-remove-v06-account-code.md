# Remove the v0.6 account code

## Problem

The gateway creates and executes Modular Account v2 accounts on EntryPoint v0.7 only:

- the v0.6 send path and the VerifyingPaymaster were removed (`20260807-retire-v06-send-path.md`);
- `account_provider` accepted only `modular_account_v2`;
- legacy-factory wallet records are retired (`20261020-retire-legacy-factory-wallets.md`).

The code that served v0.6 accounts was still there:

- **`core/chainio/aa`:**
  - generated bindings for the SimpleAccountFactory, the v0.6 EntryPoint and the VerifyingPaymaster, with their ABIs;
  - the SimpleAccount ABI and its `executeBatch` / `executeBatchWithValues` packers;
  - a package-global factory that derivation read, set from config at boot and again before each send;
  - provider routing that derived a SimpleAccount address for any factory other than MA v2.
- **Config:**
  - `smart_wallet.factory_address` and `entrypoint_address` (both v0.6 addresses);
  - `paymaster_address`, ignored with a boot warning;
  - `account_provider`, which had one valid value.

One remnant was still live. The EntryPoint nonce reader (`aa.GetNonce`) read the address in a package global, which was set from `entrypoint_address`. No deployed config sets that field, so it fell back to the v0.6 EntryPoint (`0x5FF1…2789`). MA v2 nonces live on the v0.7 EntryPoint, so these reads returned v0.6 nonces:

- `GET /api/v1/wallets/{address}:getNonce`;
- the worker's `GetNonce` and `GetNonceByAddress` RPCs.

The REST endpoint also read key 0. Modular Account v2 selects the validation from the nonce key, and the owner's fallback signer is key `0x1` (entity 0, global validation). Key 0 selects no validation and its sequence never advances, so an external signer that used the returned value built an operation the account would not validate.

## Change

- **`aa` package:**
  - Removed the v0.6 bindings, the ABIs, the SimpleAccount packers, the factory global, and the `AccountProvider` routing.
  - `DeriveSenderAddress(conn, owner, factory, salt)` and `DeriveInitCode(owner, factory, salt)` take the factory from a wallet record or request. Any factory but MA v2 is refused with `ErrLegacyFactory` instead of being derived. `RequireMAv2Factory` performs that check.
  - `UnpackExecuteCalldata` decodes `execute` and the MA v2 `executeBatch`.
  - `UserOperationRevertReasonEvent` replaces the v0.6 binding for decoding revert reasons. The event is the same in v0.7.
- **`pkg/erc4337`:**
  - Removed the v0.6 `UserOperation` type and its parser.
  - Removed the bundler client's v0.6 send, estimate and simulate methods, the per-sender `NonceManager`, and the self-hosted bundler's mempool dump and flush helpers. Nothing called them.
  - The client keeps `GetUserOperationByHash` and `GetUserOperationReceipt`.
  - `go.mod` drops `go-playground/validator` and the direct `mapstructure` requirement, which only the parser used.
- **Nonce reads** use `aa.EntryPointNonce`, which reads the v0.7 EntryPoint. This covers the REST endpoint and both worker RPCs. It rejects keys outside uint192, and the worker's `GetNonce` rejects a negative salt.
- **`GET /wallets/{address}:getNonce`:**
  - Reads the owner's key `0x1`. It returns the full nonce, `key << 64 | sequence`, for use as `userOp.nonce` unchanged. The OpenAPI description says so. Workflow execution was not affected: the send path already read the nonce at its validation entity's key.
  - Checks ownership against the wallet record on the chain being read. It checked the gateway's default chain, so a wallet registered only on another chain returned 404, and a default-chain record opened every chain's nonce.
- **Config:**
  - Removed `factory_address`, `entrypoint_address`, `paymaster_address` and `account_provider` from `smart_wallet` (top level and per chain), and from the worker's `smart_wallet`.
  - YAML loading ignores unknown keys, so existing configs still load. A config that sets these keys now gets no boot warning and no refusal.
  - `SmartWalletConfig.EntryPointAddress()` returns the v0.7 EntryPoint.
- **Call sites** use the MA v2 factory directly:
  - The atomic batch always packs MA v2 `executeBatch`.
  - The MA v2-only guards in the deploy check, the session preflights, native withdraw and `SendUserOpAuto` are gone.
  - Log fields that reported the account provider, the configured factory or the paymaster are removed.
- **Unchanged:**
  - Stored wallet records keep their `factory`. `GetWallet`, `SetWallet` and the session-policy check still recognize a retired legacy wallet from its stored record without deriving it.
  - The worker proto is unchanged.
  - `smart_wallet.controller_private_key` is unchanged.

## Tests

- **Removed:** tests of the dual-provider routing and of `account_provider` validation; the v0.6 EntryPoint-deposit withdrawal test, whose single-argument `withdrawTo` exists on neither EntryPoint; and the manual "withdraw all" test of the v0.6 paymaster reimbursement.
- **`testutil`:** removed the v0.6 helpers `EnsureWalletDeployed` and `GetTestSmartWalletAddress`. The live tests that used them now require a deployed fixture, and the failure message names `scripts/fixture_wallet -deploy`.
- **Moved to MA v2:** the `aa` round-trip and inner-call tests now pack MA v2 batches.
- **Added:**
  - `TestGetWalletNonceReadsOwnerValidationKey` pins the REST nonce key to `0x1` and the response to the full nonce.
  - `TestGetWalletNonceChecksOwnershipOnTheReadChain` covers both directions of the per-chain ownership check.
  - `TestEntryPointNonceRejectsKeysOutsideUint192` covers negative and oversized keys.

## Deploying

No migration is needed: `make storage-check` reports no breaking change. Remove the dead keys from the avs-infra configs whenever convenient. Until then they are ignored.
