# Batch send to wallets: a recognized graph, 20 native recipients, gas sized from the grant

- **Date**: 2026-10-28
- **Status**: Implemented
- **Branch**: feat/batch-send-recognizer
- **Related**: AvaProtocol/studio `PLAN_SEND_TOKENS_SKILLS.md` (Batch send to wallets), docs/changes/20261014-native-cap-self-funded-gas.md

## Problem

Studio's new Batch send to wallets Skills (`on-demand-batch-send`, `recurring-batch-send`) send rows of ETH and ERC-20 to several wallets, each row with its own amount. A loop compiles one runner for every row, so the graph sends ERC-20 rows and ETH rows in two loops over `code1`'s outputs. The gateway could not authorize the graph, for three reasons.

1. **The coverage walk can't read the graph.** It binds a loop's `{{value}}` only over a settings list, or over the two shipped graphs it recognizes by source (split and the old batch). Both new loops iterate `{{code1.data.…}}`, so every fund move was unresolved. The result was `target_unresolved` and no `nativeRecipients`. The old batch recognizer is ERC-20 only: one ETH row makes it give up.
2. **`MaxNativeRecipients` was 5.** It came from A0's fixed verification-gas seeds. #828 removed those: the bundler now prices session operations.
3. **Self-funded gas was budgeted from the same seeds.** The first operation was budgeted at 2,000,000 units and a steady send at 500,000. NativeTokenLimitModule charges a self-funded operation its signed verification + call + pre-verification gas at maxFee. Under bundler pricing, a 5-row install signs 3.36M units and a steady send 767k. A self-funded native grant could therefore fail its install, or later runs, with `ExceededNativeTokenLimit`, even at 5 rows.

## Change

1. **`boundBatchSendLoop`** (`session_skill_loop_bound.go`) recognizes either loop of the compiled Batch send graph.
   - **Match:** `code1` must equal the shipped source, `batchSendFundingSource`. Each loop's runner must match: an ERC-20 `transfer` over `{{code1.data.tokenSends}}`, or a native transfer over `{{code1.data.ethSends}}`.
   - **Sizing:** from `settings.transfers`, giving one transfer site per token (`tokenTotalSites`, shared with the old batch) and one native site per ETH row. Every recipient is listed, and each send counts as one UserOp.
   - **Refusal:** a row it can't read leaves the loop unrecognized, as for the old batch. A Studio edit to `code1` needs this copy updated first.
2. **`MaxNativeRecipients = 20`**, measured with `scripts/spike/native_recipient_cap`. It uses the production path: `HooksFor`, an install-first replace, and `SendUserOpMAv2` with bundler-priced gas, self-funded. Every case mined on Sepolia:

   | Case | Signed units | Actual gas |
   | --- | --- | --- |
   | 20 rows, first install with deploy | 5,407,610 | 4,405,208 |
   | 20 rows, replace of a 20-row grant | 5,805,602 | 4,757,279 |
   | 20 rows, replace tearing down four 20-row grants | 8,580,582 | 6,840,893 |
   | 20 rows + 8 capped ERC-20 targets, replace of the same shape | 10,797,774 | 8,967,288 |
   | Steady send on an installed grant, any size | 767,036 | 316,114 – 492,386 |

   The OpenAPI `maxItems` for `nativeRecipients` and `delegatedNativeRecipients` moves to 20; `make rest-gen` changes comments only. The SDK no longer counts recipients (ava-sdk-js removes `MAX_NATIVE_RECIPIENTS`): one builder call can't see the wallet's merged grant.
3. **Self-funded gas is sized from the grant** (`grantGasShape`, `session_grant_native.go`).
   - **First operation:** `(2,550,000 + 163,000 × native rows + 624,000 × allowed-action targets) × 120%`. It is fitted to the larger of a first install with deploy and a replace of a same-shape grant.
   - **One number everywhere.** It is a pure function of the grant's permissions, so the merge, the carried merge, both coverage checks, the per-task price and the send preflight compute the same number.
     - `selfFundedGasWei`, `priceWalletNativeGas` and `foldMissingInstall` take the shape.
     - The carried merge reads its recipients from `carriedNativeRecipients`, which also builds the merged permissions.
   - **Steady send:** `nativePreflightGasUnitsSteady` is 900,000 (measured 767k). The payable steady stays 1.5M.

## Not covered

- **Leftover teardowns.** A replace that also tears down leftover grants (up to `maxOnChainTeardowns`) can sign more than the budget: four 20-row teardowns signed 8.58M against 6.97M.
- **The bundler ceiling.** Alchemy priced the largest replace at exactly 10,000,000 verification gas, likely its ceiling. Grants with many ERC-20 targets are near it whatever the native cap.
- **Base.** Not measured yet; its pre-verification gas includes L1 data cost.
- **The sponsored path.** Not re-measured. Sponsored operations are charged value only, so the budget doesn't apply.

## Verification

- `go test ./core/taskengine ./aggregator/...` and `go vet ./...`, including `-tags integration`.
- New tests:
  - `TestBatchSendSizesTokensAndEthFromSettings` and `TestBatchSendShapeStaysUnresolved`;
  - `TestNativeGasBudgetsCoverSepoliaMeasurement`, which pins every measured case under its budget;
  - `TestMaxNativeRecipientsFollowsReplaceNotInstall`, which now pins 20 and four teardowns.
- The gas tests now compute their expected caps (`gasAt1Wei`) instead of hard-coding them.
- Studio's gateway spike (`scripts/tests/established.batch-send.gateway.test.ts`) passes 12/12 against a local gateway built from this branch. Both new templates list their ETH recipients in `authorization.required.nativeRecipients`.
- The SDK live test refuses 21 recipients with `SESSION_POLICY_TOO_MANY_NATIVE_RECIPIENTS`.
