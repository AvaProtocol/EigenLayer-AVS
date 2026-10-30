# Wallet budget feasibility — phase 1

Experiments for Studio #2034, from gateway `origin/staging` at
`367a11a6065b3d10c6cc1a9246c93e8e955bedc6`. Branch:
`spike/wallet-budget-feasibility`. These are tests and research, not the wallet
budget implementation. No production behavior, configuration, API, database
schema or Studio code changes.

## Decision

**Mechanism 1 is a provisional candidate for the tested Alchemy route, not a
settled portable default.** The Sepolia Alchemy bundler accepted an owner-signed
deferred reset/install batch, the USDC allowance became zero, and the new
session validation was installed. A reverting reset was rejected during gas
estimation: no transaction was sent and the on-chain approval/install state was
unchanged. Only mechanism 2's deliberately mined failure demonstrates a real
transaction rolling back the reset and install.

Before selecting mechanism 1 for production, confirm Alchemy's validation
storage policy or reproduce acceptance on a second strict bundler, and define
immediate activation in the step 2 contract. Mechanism 2 is the execution-phase
alternative if that portability or activation requirement cannot be met. It
does not put token reset storage accesses in validation, but needs its own
owner operation and fee review. No second bundler or Alchemy policy confirmation
has been obtained by this spike.

This unblocks drafting the interface contract (step 2). Gateway budget mode and
Studio implementation still wait for that contract and gateway-owner review.
The expired-permission error needs a presentation fix in the gateway build;
S4 below records the exact gap. No custom Aave module is required for the
agreed phase 1 approach of explicitly consenting to unmetered protocol actions.

## Results

| Spike | Result | Evidence |
| --- | --- | --- |
| S1 deferred resets + install | Observed working on Alchemy; strict-bundler portability remains unconfirmed | `TestWalletBudgetApprovalMechanisms_Sepolia/S1DeferredResetsAndInstall`, `S1DeferredFailingResetRefused`, `S1SessionCannotStripOwnerSignedResets` |
| S2 owner UserOperation batch | Works; both estimator refusal and mined execution rollback verified | Same test, `S2OwnerResetsAndInstall`, `S2OwnerFailingResetRefused`, `S2OwnerMinedFailureRollsBack` |
| S3 zero cap | Works on the stock deployed AllowlistModule; gateway currently rejects zero in two places | Same test, `S3ZeroCap`, including `approve1` and `transfer1`; `TestWalletBudgetZeroCapPrototypePreservesProductionGuards` |
| S4 no workflow end | Partial: HTTP create, persistence, execution and expired-permission refusal work; the renewal instruction is truncated. | `TestWalletBudgetManualWorkflowWithoutEndDateOverHTTP`, including its expired-permission subtest; existing resolver/deploy tests |
| S5 router swaps with finite WETH cap | Both directions work without direct wallet WETH deposit/withdraw permissions | `TestWalletBudgetRouterWithoutWETHWrapPermissions_Sepolia` |
| S6 Aave withdrawal recipient | Research complete; stock modules cannot impose the recipient condition | Options and estimates below; no contract deployed |

## S1: deferred resets, then install

The pinned [ModularAccountBase v2.0.1 source](https://github.com/alchemyplatform/modular-account/blob/v2.0.1/src/account/ModularAccountBase.sol)
does not limit the deferred call to `installValidation`. `_handleDeferredAction`
checks the owner's authority and signature, then self-calls the signed bytes.
`executeBatch` can call USDC `approve(router, 0)` and then self-call
`installValidation`. A revert bubbles through validation and rolls back the
whole deferred batch. The fallback owner has no validation hooks, satisfying
the deferred-signature restriction.

The execution call carried by the session operation is separately
`executeUserOp`-wrapped so the new validation's execution hooks run. This matters
even for the first operation that installs it.

Fixture: `0xa0F7dD863b2Dd9C82B47728c83eE07D75E867C35`, owned by the existing
Sepolia test owner. A preliminary owner operation seeds a real USDC allowance
of 123 base units to SwapRouter02. The signed deferred call resets it, then
installs entity 101 with Allowlist validation/execution hooks and TimeRange.
The first session execution only approves zero, so it cannot conceal a failed
reset by setting a nonzero allowance later.

- Successful reset/install UserOp:
  `0xbfd256ebc147fd79d34ae0b22d3e7257e7351f843444796de98087a4243c8763`.
  [Sepolia transaction](https://sepolia.etherscan.io/tx/0x9d23db8896f5a533687c8437577aa893f16af495f598325768d6cb6b09109729).
  Readbacks assert allowance zero and entity signer installed.
- Failing case: reset the live router approval, then call USDC
  `approve(address(0), 0)`, then install entity 102. The bundler refuses it with
  `AA23 reverted` / `ERC20: approve to the zero address`. There is no transaction
  for this refusal. Readbacks assert allowance still 123 and entity 102 absent.

The result demonstrates acceptance by this bundler for this deployed wallet
and USDC proxy, not universal acceptance for every token or bundler. The
[ERC-7562 storage rules](https://ercs.ethereum.org/ERCS/erc-7562) allow associated
storage of an existing sender, but this does **not** establish that the whole
USDC approval call satisfies strict validation rules. Circle's
[approval implementation](https://github.com/circlefin/stablecoin-evm/blob/master/contracts/v1/FiatTokenV1.sol)
also checks the paused flag and the spender's blacklist state. Those reads are
not tied to the sender. The fixture checks that the fresh sender is unstaked;
the operation is deployed and self-funded, with no factory/paymaster frame to
justify arbitrary external reads under STO-033.

Alchemy's acceptance may rely on provider-specific exemptions, policy, or
validation tracing behavior. We have not traced the deployed implementation's
storage accesses or obtained provider confirmation, so this is a portability
risk, not a confirmed Alchemy defect. A wallet-associated allowance slot alone
does not resolve it. Before using mechanism 1 beyond this tested route, obtain
Alchemy confirmation or run the same signed operation shape against an
independent strict bundler and record the result. This experiment does not test
counterfactual deployment and resets in one op.

`S1SessionCannotStripOwnerSignedResets` signs the reset/install batch with the
owner key, then reuses that signature with an install-only deferred batch and
a session-key operation. It prices the valid original batch as a control, then
signs the altered operation with the session key and submits it through
`eth_sendUserOperation`. It requires the deployed account's
`DeferredActionSignatureInvalid()` refusal, with the allowance unchanged and
entity 107 absent. This exercises EVM validation through the real bundler,
rather than relying only on the Go digest helper or an unsigned estimation
attempt. The rejected submission has no transaction. The signed-submission
rerun used wallet `0xB0BC272fC090455cd928D20478161753276bA899` and returned
`AA23 reverted` with `revertData: 0x01334f77`, the expected signature-error
selector. Both its approval and entity readbacks passed.

The deferred batch is in validation. If the later Skill execution fails, a
successful reset/install still persists. A failure in a reset inside the
deferred batch prevents installation. Do not move resets into the later Skill
execution and claim that it has the same atomicity.

## S2: owner-signed execution batch

The existing `preset.PrepareOwnerUserOp` successfully prices
`executeBatch([USDC.approve(router, 0), wallet.installValidation(...)])`.
The account permits that self-call under owner authority. The same allowance
and signer readbacks pass for entity 103.

- UserOp: `0xb317adf6ea4e015fd7cf2e5ec033e080f0e4e42c6f1af2fcf5cb5631ab121016`.
  [Sepolia transaction](https://sepolia.etherscan.io/tx/0x6283510fe7294675b61b709136301e06d230098b3f1647cd29cf7d5c277a7728).
- The reset-to-zero-spender variant is refused during gas estimation, with the
  live allowance unchanged and entity 104 absent.
- Mined negative case: `S2OwnerMinedFailureRollsBack` deliberately puts an
  install of entity 106 before the failing reset, signs the final larger batch
  using the valid control's gas estimate plus 500k call-gas headroom and 100k
  pre-verification headroom, and sends it once. This rerun used wallet
  `0x5512eFa59932113F942f7b1C08Efe989118691e1`. UserOp
  `0xd76ec2aeaeda1b17350ce09708dc3d2eabf5bb830a0f9a96056f02c3c08e8b4d` was
  [included](https://sepolia.etherscan.io/tx/0x206e0592eb41ef8300d1261aa2de975208442d9641123dee60cea439258974e4)
  with UserOp success **false**. The allowance remains 123 and entity 106 is
  absent. The test decodes the matching EntryPoint `UserOperationRevertReason`
  event and requires `ERC20: approve to the zero address`, excluding an
  out-of-gas failure before the install. This proves execution rollback after
  installation had begun. The outer
  EntryPoint transaction succeeds while this inner UserOp fails; these are
  distinct statuses. This case also passed with Go's race detector.

**Correction to the original report:** the previously cited
[failure transaction](https://sepolia.etherscan.io/tx/0xe12196b88bf7eab45c585b9431b9b22f65bf1cdeae1700c1eb970d013678ff43)
does not prove reset-triggered rollback. A `debug_traceTransaction` call trace
shows out of gas inside `installValidation`; no `UserOperationRevertReason`
event was emitted. Its fixed 900k execution limit was below what installation
needed. The earlier rollback claim is withdrawn and replaced by the
reason-checked transaction above. The test no longer reduces the successful
control's gas estimate to a fixed value.

The wallet signs `personal_sign` of the **32 raw bytes** of the EntryPoint v0.7
UserOp hash, prefixed once with EIP-191 and framed by the gateway. The wallet
prompt may show a hash rather than a readable reset/install description.
Studio must show the exact token/spender resets, new caps, expiry, account,
chain and maximum fee before asking for that signature, and bind that review
to the prepared bytes. This experiment uses the existing development test key;
it does not test a browser wallet's prompt.

## S3: zero-cap cleanup row

There are **two** positive-only guards to change in the later implementation:
`parseCapAmount` in `core/taskengine/session_permissions.go` and
`PackAllowlistInstallData` in `core/chainio/aa/ma_v2_hooks.go`. The test-only ABI
encoder bypasses both without changing either production guard. Its nonzero
encoding is compared byte-for-byte with the production encoder; reducing the
amount to zero changes only the cap word, preserving the metering flag.

The [stock AllowlistModule](https://github.com/alchemyplatform/modular-account/blob/v2.0.1/src/modules/permissions/AllowlistModule.sol)
meters the amount of `approve` and `transfer`. A metered cap of zero permits
zero-amount calls and refuses positive amounts. It is not an unlimited row.

Entity 105 is installed with cap zero and both selectors permitted:

- Install UserOp `0xd86c04aef0897981b8bbfa9e482f585d614afaf31a1ba7a2c6d681a31f79785a`;
  [transaction](https://sepolia.etherscan.io/tx/0x9733c0f1bc20b018dba0c180f323768cce840bc3a5d3063cfe3339381d6ad27c).
- `approve(router, 0)` succeeds: UserOp
  `0xabc6fabe90b59fe87b8bc1abb12cc80e128ea8cb26d4451207e32c6fe81c08aa`;
  [transaction](https://sepolia.etherscan.io/tx/0xfa9f6efb438346dbeba6e49dfd756c11da79ac606c09160691e5aa018a9dba68).
- Nonzero approve and transfer are refused by the real bundler. It hides the
  inner execution error, so the tests additionally call the deployed execution
  hook against the same wallet/entity and assert the exact
  `ExceededTokenLimit()` selector. That rules out a transfer failing merely
  because this isolated wallet has no USDC balance. Refused calls have no tx.

The hook also allows a zero-value transfer. An approve-only cleanup row should
therefore expose only `approve`, as the plan proposes. Keeping exhausted rows
at zero also requires auditing merge/carry code that currently drops them;
this spike does not implement that migration.

## S4: a Manual Skill without an end date

The test mounts the real REST create/trigger handlers with the existing test
authentication fixture and a real engine/database. It POSTs a Manual workflow
without `expiredAt`, reloads it, asserts `ExpiredAt == 0`, runs a local CustomCode
node through the blocking workflow executor, and reloads it again. It succeeds
without inventing an expiry. The server still applies `DefaultMaxExecution`:
no calendar end does not imply unlimited execution quota.

A second no-end Manual workflow has a real ContractWrite definition and an
expired stored wallet permission. Deployment checks are disabled explicitly to
reach the run-time guard. The real execution fails before sending a UserOp and
returns `SESSION_POLICY_EXPIRED`. This test uses an isolated synthetic wallet
record, not a live grant waiting for a wall-clock expiry. Existing
`TestSessionResolverRefusesExpiredGrant` and
`TestDeployCheckRefusesExpiredGrantWithNoEnd` also pass.

**Gap:** the resolver's full message contains an expiry timestamp and
`grant again to keep executing`, but the contract-write path truncates it to
200 characters. In this request it ends just after the wallet address, losing
the renewal instruction. The step's `errorCode` is also the generic
`INVALID_REQUEST`; `SESSION_POLICY_EXPIRED` survives only inside the truncated
message. Keep the structured expiry code and a concise, actionable reason
through the execution result when building the budget verdict. No production
error handling is changed here.

The HTTP characterization test now explicitly asserts the generic error code,
the truncated suffix and the absent renewal instruction. A future fix must
update those expectations rather than silently changing this spike's finding.

## S5: keep a finite WETH row, wrap/unwrap in the router

Fixture: `0x423eAc6626f7F153f85144D8A8e562e4DBdC4C64`. The permission includes a
finite 0.1 WETH cap and **only** WETH approve/transfer selectors, capped USDC,
SwapRouter02 `multicall(bytes[])`, a 0.01 ETH NativeTokenLimit including gas,
and TimeRange. The WETH row contains neither deposit nor withdraw.

1. Send exactly 0.00001 Sepolia ETH to router multicall: `exactInputSingle`
   with WETH input and USDC output to the wallet, then `refundETH`.
   It receives 380010 USDC base units.
   UserOp `0x0087363ce4d58d543e48d0dfd8ce51860804542b75eaa815965d76fc1ea0310f`;
   [transaction](https://sepolia.etherscan.io/tx/0xfd38161a7a22af38f65dc4520e1d23b3b51078ad83d3d1e84fe8cbdfd9d30012).
2. Approve exactly the purchased amount and call router multicall:
   `exactInputSingle` with USDC input, WETH output to the router, then
   `unwrapWETH9` to the wallet.
   UserOp `0x2f573cbc26f6f35bcb3356c1fa98758bf05f99fce7a98c3558f10047e2347d0f`;
   [transaction](https://sepolia.etherscan.io/tx/0x0e7977ab54108db17771648614504991a1fbc38716f438a459cac598f1dbefa0).

The test asserts all purchased USDC is sold, no WETH remains in the wallet,
the WETH allowance-module remainder is still 0.1 WETH, and the separate native
budget charges gas. After the first swap, its decrease must exceed the ETH
input; after the second, zero-value batch, it must decrease again. A decrease
from the ETH input alone is insufficient. Router [payment helpers](https://github.com/Uniswap/v3-periphery/blob/main/contracts/base/PeripheryPayments.sol)
do wrapping/unwrapping in the router's own context. The finite WETH cap does
not meter that native input; native value and gas need their own limit.

These are tiny feasibility swaps with a one-base-unit minimum, not a production
slippage policy. A shipped workflow must quote and enforce an appropriate floor.
Only `multicall` is visible to the account's target/selector check; this is not
a claim that the stock module restricts recipients inside arbitrary multicalls.

Existing direct-wrap paths found by source search:

- `core/taskengine/session_grant_native_live_test.go`: `runWETHDeposit` and its
  Uniswap-only L4 fixture explicitly permit and call deposit.
- `core/taskengine/tenderly_client_sepolia_test.go`: payable WETH deposit
  simulation fixture.
- `scripts/spike/native_eth_hooks/main.go`: direct deposit spike.
- Production `vm_runner_contract_write.go` is a generic authored-call runner;
  its Uniswap helpers validate swaps but do not rewrite deposit/withdraw into
  router calls. Templates that currently author direct WETH calls must change
  their graph when opting into a finite WETH budget. They cannot just drop the
  selectors and keep the old graph.

## S6: restrict Aave withdraw's recipient

[Aave Pool withdraw](https://aave.com/docs/aave-v3/smart-contracts/pool#withdraw)
takes `(asset, amount, to)`, with `to` independent of the caller. A target and
selector allowlist alone cannot constrain it. A cap on the underlying token
does not meter a Pool withdrawal. Recipient restriction and amount metering
are separate features.

| Option | Fit and work required | Cost / audit implications |
| --- | --- | --- |
| Custom ERC-6900 execution hook | Best fit for a future on-chain guarantee. Parse the account's execute/batch/executeUserOp paths; for the pinned Pool and withdraw selector require a canonical `to == account`, even for `amount == maxUint256`. Keep the existing target/selector and cap hooks. | New Solidity code, deployment, install encoding and lifecycle tests; external security review required. Planning estimate: several engineer-days for a narrow prototype, roughly 1–2 engineer-weeks for hardening before audit, not a delivery commitment. |
| Existing Alchemy stock module | No matching recipient-argument restriction found in the published permission modules: Allowlist, NativeTokenLimit, TimeRange and PaymasterGuard. | No supported drop-in identified. Do not advertise an on-chain guarantee based on these modules. |
| Custom execution module / adapter | Possible, but an ordinary external wrapper changes `msg.sender` at Aave and cannot withdraw the wallet's aTokens. An account-integrated module would need to execute as the account and close every alternate Pool route. | More authorization and composition surface than a narrow hook; substantial additional audit scope. Not the phase 1 recommendation. |
| Gateway-only recipient check | Cheap defense for the normal workflow compiler; keep it even if a hook is added later. | Not an on-chain restriction on a compromised session signer. Continue explicit unmetered-protocol consent in phase 1. |

The [ERC-6900 hook model](https://ercs.ethereum.org/ERCS/erc-6900) supports this
kind of enforcement. A future hook must reject malformed/short/noncanonical
calldata, apply to every batch element and entry path, and prevent bypasses
through self-administration, alternate permitted Pool entry points or adapters.
Keep Pool addresses chain-specific; proxy implementation upgrades affect the
review assumptions. A `to == wallet` check still allows unlimited withdrawal
back to the wallet and does not protect health factor or cap protocol amounts.

Gas estimate is deliberately rough and **unmeasured**: an extra external hook
plus calldata decoding and a few configuration reads is plausibly on the order
of 10k–30k gas for a simple call; batches scale with decoding/number of calls.
[EIP-2929](https://eips.ethereum.org/EIPS/eip-2929) alone charges 2600 for a cold
account access and 2100 for a cold storage read, so zero overhead is not credible.
Measure the actual module on forks and Sepolia before setting a gas reserve.
Deployment/audit dollars cannot be estimated reliably without code and quotes.

## Boundaries for the interface contract

- Define activation separately from the Skill schedule. A deferred signature
  changes nothing on chain until a session operation carrying it is included.
  Existing approvals and the previous permission stay live until then. The
  fixture activates immediately and signs only a ten-minute deadline; it does
  not prove that a first scheduled run hours or days later can activate safely.
- Step 2 must distinguish signed/pending activation from on-chain active, bind
  an explicit activation operation to the reviewed reset/install, define a
  short activation deadline, confirmation/failure recovery and who pays gas.
  Do not label a new budget active or a scheduled Skill enabled before the
  activation is confirmed. Prove a safe activation call for every permission
  shape; this spike does not specify or test a generic no-op activator.
- Do not silently extend the owner signature to cover a far-future first run.
  If delayed activation is deliberately supported, disclose the live old
  approvals and expiry window, keep the state pending, and obtain a fresh
  signature after expiration. Choose mechanism 2 if immediate deferred
  activation or the required bundler storage policy cannot be supported.
- Sign the exact ordered resets and install/old-entity retirements, not only
  the displayed budget totals. These spikes prove reset + new install; full
  migration/replacement of old entities remains feature integration work.
- Test token behavior before supporting its reset in validation. Account
  `executeBatch` bubbles EVM reverts but does not interpret an ERC-20 `false`
  return or prove a no-op token set allowance to zero. This result covers the
  tested reverting USDC implementation. Supporting false/no-op tokens would
  need an on-chain zero-allowance assertion, not merely a read after install.
- For mechanism 2, owner prepare prices first, then the owner signature binds
  the final UserOperation bytes. No mutation or signing-key relay repair after
  that signature. Mechanism 1 instead binds `DeferredAction(nonce, deadline,
  call)`: it does not sign the outer session operation's execution or gas
  fields. Its installed permission hooks constrain that operation. The step 2
  contract must describe that difference and the activation fee policy rather
  than claiming that a deferred signature freezes the entire UserOperation.
- First installs on undeployed accounts, multiple reset tokens/spenders,
  existing unlimited approvals and real prior-grant migration need their own
  end-to-end feature tests. The live reset fixture here used a nonzero finite
  approval, not an unlimited approval.

## Running and verification

Use the normal private `.env` and `config/test.yaml` worktree links. Never
commit them or publish raw config logs (the config loader logs RPC URLs).
`TEST_PRIVATE_KEY` is the owner; the configured test controller signs session
operations. The tests fail closed unless chain ID is Sepolia and require
`WALLET_BUDGET_FEASIBILITY=1`. They never use a Gas Manager policy.

```sh
WALLET_BUDGET_FEASIBILITY=1 make test/package TAGS=integration \
  PKG='./core/taskengine -run TestWalletBudget -count=1'

go test -race ./aggregator/rest ./core/taskengine \
  -run 'TestWalletBudgetManualWorkflowWithoutEndDateOverHTTP|TestWalletBudgetZeroCapPrototype|TestWalletBudgetCleanup|TestSessionResolverRefusesExpiredGrant|TestDeployCheckRefusesExpiredGrantWithNoEnd' \
  -count=1
```

The Make target cleans both Go caches and builds the repository before running
the selected package. Iteration used the equivalent targeted `go test` commands
to avoid repeating cache cleanup. HTTP/offline checks pass with the race
detector. Initial live iterations exposed stale `latest` reads even after an
Alchemy inclusion receipt: fixture assertions now read the exact mined block,
and the send helper waits for nonce consumption before the next preparation.
This changes only test synchronization, not execution logic. No production
server was restarted.

Live verification can be reproduced with:

```sh
WALLET_BUDGET_FEASIBILITY=1 go test -race -tags=integration ./core/taskengine \
  -run 'TestWalletBudget(ApprovalMechanisms|RouterWithoutWETHWrapPermissions)_Sepolia' \
  -count=1 -v
```

Post-review verification: the HTTP/offline race checks passed. Both complete
live test groups and all their subtests were then rerun together on the
unchanged code at `bf5822a0` with the race detector and passed in 259.455 seconds.
That combined run includes the fully signed stripping attack, reason-checked
mined rollback, both swaps, gas-budget assertions and fixture cleanup. CI also
passed on `bf5822a0`.
Static checks passed with
`go vet -tags=integration ./aggregator/rest ./core/taskengine`, Go formatting
and whitespace checks. The repository build also passed through the Make
target's build prerequisite; its earlier test run exposed the stale-read issue
that the final run fixes. This is targeted verification, not the full live suite.

Each live fixture uses a fresh salt, deploys before funding and receives at
most 0.02 ETH initial wallet funding. Cleanup returns remaining wallet ETH and
test tokens to the same owner and clears the USDC router approval. Unused
EntryPoint prefund refunds remain owner-controlled and are logged separately.
If ETH is zero while tokens or an approval remain, cleanup fails with the exact
residual amounts and fixture address/salt; it cannot log successful recovery.
After sending cleanup, token and allowance readbacks must be zero. Unit tests
cover the zero-ETH residual cases.
`TestWalletBudgetRecoverFixture_Sepolia` accepts an explicitly supplied
`WALLET_BUDGET_SALT` to recover an interrupted fixture without funding it.

This change is the phase 1 evidence for review. Step 3 waits for the step 2 API
contract.
