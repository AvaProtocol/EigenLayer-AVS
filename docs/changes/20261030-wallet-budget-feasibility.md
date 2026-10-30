# Wallet budget feasibility — phase 1

Experiments for Studio #2034, from gateway `origin/staging` at
`367a11a6065b3d10c6cc1a9246c93e8e955bedc6`. Branch:
`spike/wallet-budget-feasibility`. These are tests and research, not the wallet
budget implementation. No production behavior, configuration, API, database
schema or Studio code changes.

## Decision

**Use mechanism 1: put the approval resets and installation in the owner's
signed deferred batch.** The Sepolia Alchemy bundler accepted it, the USDC
allowance became zero, and the new session validation was installed. A reverting
reset was rejected during validation, with the earlier reset rolled back and
the new entity absent. Mechanism 2 also works as an alternative owner-signed
UserOperation, but requires a separate operation and fee review.

This unblocks drafting the interface contract (step 2). Gateway budget mode and
Studio implementation still wait for that contract and gateway-owner review.
The expired-permission error needs a presentation fix in the gateway build;
S4 below records the exact gap. No custom Aave module is required for the
agreed phase 1 approach of explicitly consenting to unmetered protocol actions.

## Results

| Spike | Result | Evidence |
| --- | --- | --- |
| S1 deferred resets + install | Works on a deployed MA v2 wallet, through the real Alchemy bundler | `TestWalletBudgetApprovalMechanisms_Sepolia/S1DeferredResetsAndInstall`, `S1DeferredFailingResetRollsBack` |
| S2 owner UserOperation batch | Works; both estimator refusal and mined execution rollback verified | Same test, `S2OwnerResetsAndInstall`, `S2OwnerFailingResetRollsBack`, `S2OwnerMinedFailureRollsBack` |
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

Fixture: `0x59755510CB6783AA6506f9E55241B3d924658bFF`, owned by the existing
Sepolia test owner. A preliminary owner operation seeds a real USDC allowance
of 123 base units to SwapRouter02. The signed deferred call resets it, then
installs entity 101 with Allowlist validation/execution hooks and TimeRange.
The first session execution only approves zero, so it cannot conceal a failed
reset by setting a nonzero allowance later.

- Successful reset/install UserOp:
  `0x6cd51161b649e683370dc4e03ef23e4c9d9bf350727a4a125fe3a0a2a957c52d`.
  [Sepolia transaction](https://sepolia.etherscan.io/tx/0xcdf93cec1958117aea5973aea8196b75b538ea7223c2fea7d569c5ef54127eb8).
  Readbacks assert allowance zero and entity signer installed.
- Failing case: reset the live router approval, then call USDC
  `approve(address(0), 0)`, then install entity 102. The bundler refuses it with
  `AA23 reverted` / `ERC20: approve to the zero address`. There is no transaction
  for this refusal. Readbacks assert allowance still 123 and entity 102 absent.

The result demonstrates acceptance by this bundler for this deployed wallet
and USDC proxy, not universal acceptance for every token or bundler. The
[ERC-7562 storage rules](https://ercs.ethereum.org/ERCS/erc-7562) permit associated
account storage for an existing sender. Tokens with extra unrelated storage
access still need a provider test. This experiment deploys the wallet before
installing; it does not test counterfactual deployment and resets in one op.

The deferred batch is in validation. If the later Skill execution fails, a
successful reset/install still persists. A failure in a reset inside the
deferred batch prevents installation. Do not move resets into the later Skill
execution and claim that it has the same atomicity.

## S2: owner-signed execution batch

The existing `preset.PrepareOwnerUserOp` successfully prices
`executeBatch([USDC.approve(router, 0), wallet.installValidation(...)])`.
The account permits that self-call under owner authority. The same allowance
and signer readbacks pass for entity 103.

- UserOp: `0x6b9cb0bc2c0a2518b1f50017702d3deb77ec36c57bf9bdef408ac0a7d6ee6c46`.
  [Sepolia transaction](https://sepolia.etherscan.io/tx/0x8adfe8868fd5488cf8e928c30bd14a5f165f05652fcac084a093db08f49cd2cd).
- The reset-to-zero-spender variant is refused during gas estimation, with the
  live allowance unchanged and entity 104 absent.
- Mined negative case: `S2OwnerMinedFailureRollsBack` deliberately puts an
  install of entity 106 before the failing reset, signs the final larger batch
  with sufficient gas headroom, and sends it once. UserOp
  `0x4fcb0f549b48a7069822bca1e4b2c0cb88a6b4f69273e32677841348bf363eb4` was
  [included](https://sepolia.etherscan.io/tx/0xe12196b88bf7eab45c585b9431b9b22f65bf1cdeae1700c1eb970d013678ff43)
  with UserOp success **false**. The allowance remains 123 and entity 106 is
  absent, proving execution rollback after installation had begun. The outer
  EntryPoint transaction succeeds while this inner UserOp fails; these are
  distinct statuses. This case also passed with Go's race detector.

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

- Install UserOp `0xdaba50d60e86d92a04d1f6fa046358cc3badc3b190d29e2a3dac0e8ff3507b67`;
  [transaction](https://sepolia.etherscan.io/tx/0x0b3594e46006995b9520a15c5629b946f9a931dd97ccb9ae8d745062fbaed1b2).
- `approve(router, 0)` succeeds: UserOp
  `0x523e2e04f0f7530b2a79ac1e5acbbc16752236be0d3c1c68f9cba4c31fbd9e4d`;
  [transaction](https://sepolia.etherscan.io/tx/0x6ba416e4d5b87976c685a701636ac8a70d9d41a2bc2c68fb56412a60b8e67263).
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

## S5: keep a finite WETH row, wrap/unwrap in the router

Fixture: `0x6D8a181fc88FAcB6F119c37c9A9f79dF035f8FcA`. The permission includes a
finite 0.1 WETH cap and **only** WETH approve/transfer selectors, capped USDC,
SwapRouter02 `multicall(bytes[])`, a 0.01 ETH NativeTokenLimit including gas,
and TimeRange. The WETH row contains neither deposit nor withdraw.

1. Send exactly 0.00001 Sepolia ETH to router multicall: `exactInputSingle`
   with WETH input and USDC output to the wallet, then `refundETH`.
   It receives 382267 USDC base units.
   UserOp `0xd8e8612666fc1c420e13683abe5f0d203746833fd6017c6fd96ed28a75635a04`;
   [transaction](https://sepolia.etherscan.io/tx/0x93800c4d494aa41532acc9dca96e1d1e87f61f07791f47ce3d31b24358eb9ce1).
2. Approve exactly the purchased amount and call router multicall:
   `exactInputSingle` with USDC input, WETH output to the router, then
   `unwrapWETH9` to the wallet.
   UserOp `0xc2b822edbf36e4708a00797b7f8c7183e356575948b84f3b465bada24ff71bd2`;
   [transaction](https://sepolia.etherscan.io/tx/0xe288bb4c0c4443b842589fbfd378c8b3cc3e05d02b9894d6affea570f0b3bf9c).

The test asserts all purchased USDC is sold, no WETH remains in the wallet,
the WETH allowance-module remainder is still 0.1 WETH, and the separate native
remainder decreased. Router [payment helpers](https://github.com/Uniswap/v3-periphery/blob/main/contracts/base/PeripheryPayments.sol)
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

- Sign the exact ordered resets and install/old-entity retirements, not only
  the displayed budget totals. These spikes prove reset + new install; full
  migration/replacement of old entities remains feature integration work.
- Test token behavior before supporting its reset in validation. Account
  `executeBatch` bubbles EVM reverts but does not interpret an ERC-20 `false`
  return or prove a no-op token set allowance to zero. This result covers the
  tested reverting USDC implementation. Supporting false/no-op tokens would
  need an on-chain zero-allowance assertion, not merely a read after install.
- Owner prepare still prices first, then one owner signature binds the final
  bytes. No mutation or signing-key relay repair after an owner signature.
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
  -run 'TestWalletBudgetManualWorkflowWithoutEndDateOverHTTP|TestWalletBudgetZeroCapPrototype|TestSessionResolverRefusesExpiredGrant|TestDeployCheckRefusesExpiredGrantWithNoEnd' \
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

Final verification: both live parents and all their subtests passed together
with the race detector:

```sh
WALLET_BUDGET_FEASIBILITY=1 go test -race -tags=integration ./core/taskengine \
  -run 'TestWalletBudget(ApprovalMechanisms|RouterWithoutWETHWrapPermissions)_Sepolia' \
  -count=1 -v
```

The HTTP/offline race command above passed. Static checks passed with
`go vet -tags=integration ./aggregator/rest ./core/taskengine`, Go formatting
and whitespace checks. The repository build also passed through the Make
target's build prerequisite; its earlier test run exposed the stale-read issue
that the final run fixes. This is targeted verification, not the full live suite.

Each live fixture uses a fresh salt, deploys before funding and receives at
most 0.02 ETH initial wallet funding. Cleanup returns remaining wallet ETH and
test tokens to the same owner and clears the USDC router approval. Unused
EntryPoint prefund refunds remain owner-controlled and are logged separately.
`TestWalletBudgetRecoverFixture_Sepolia` accepts an explicitly supplied
`WALLET_BUDGET_SALT` to recover an interrupted fixture without funding it.

This change is the phase 1 evidence for review. Step 3 waits for the step 2 API
contract.
