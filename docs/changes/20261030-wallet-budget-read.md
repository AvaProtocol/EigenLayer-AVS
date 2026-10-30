# Wallet budget: read, retirement health and execution refusals

Implements brief 3a against Studio's contract at `a1859828d`.

## API and storage

- `GET /wallets/{address}/budget?chainId=` uses the existing policy ownership
  check and user JWT guard, including explicit partner refusal.
- Workflow-sized permissions work immediately. Active remainders come from the
  stock Allowlist and NativeTokenLimit modules. Pending deferred permissions
  report stored totals. Used amounts never go negative; the unlimited boundary
  is `2^128 - 1`. Legacy singular caps and absent `GrantedCap` remain readable.
- A failed remainder read returns `503 BUDGET_READ_UNAVAILABLE`. Retirement
  failures appear as `unverified` in a 200 response. Missing permissions are 404;
  ambiguous authority remains a conflict rather than choosing a grant.
- `gasWei` is absent because current records have no gas breakdown. The native
  amount still contains the combined installed limit. `liveApprovals` and
  `pendingChange` are optional and omitted until their slices exist.
- Optional `sizing`, `budgetStartedAt`, `budgetStartAmounts` and
  `budgetStartNative` fields are added to the stored policy and API. Existing
  preparation/submission paths do not write them. Absent sizing means workflow.
- Classification is centralized in `wallet_budget.go`, scoped by chain, target
  and selector. `budget_contracts.json` snapshots the current protocols catalog's
  Aave Pool, SwapRouter02, wrapped-native and Lido entry points. Unknown calls
  remain disclosed as `other`; new catalog entry points require a reviewed update.
- OpenAPI is the source of the regenerated REST interfaces. Protobuf adds
  `SESSION_POLICY_EXPIRED`, reserved `BUDGET_USED_UP`/`BUDGET_ENDED` codes, and
  additive step `error_details` with the refusal's structured fields.

## Registration is separate from module cleanup

`EntityRegisteredOnChain` reads the account's `getValidationData(bytes24)`.
`VerifySupersededTeardown` now uses registration to decide whether another
account uninstall is needed. `TornDownAt` records cleared registration.

The budget read also checks module signer, allowlist/selector/cap, native-limit
and time-range storage from the stored install. It includes retired policies
even if registration was already marked cleared:

- `registered`: account registration remains, with the stored authority limits;
- `residual`: registration is gone but module storage remains;
- `unverified`: the read or module interpretation failed;
- fully cleared entities are omitted.

Unknown module storage is never assumed clean. The owner-only direct module
cleanup encoder derives payloads from the exact stored install. It does not add
a new submission route. Allocation retains revoked records and now refuses
uint32 exhaustion instead of wrapping into a used entity.

## Execution results

Expired-permission errors retain `SESSION_POLICY_EXPIRED` and their complete
renewal instruction through contract-write method results and REST step mapping.
The S4 regression now asserts that behavior. Structured budget errors have
constructors and REST field mapping; send guards will emit them in a later slice.

## Verification

- Targeted policy, grant, budget, allocation, teardown and HTTP tests with `-race`.
- Full `core/chainio/aa` and REST mapping suites with `-race`.
- `go build ./...`, targeted `go vet`, and storage comparison against staging.
  Storage keys are unchanged; the four optional policy fields are additive.
- Sepolia opt-in read probe passed in 72.88 seconds on a fresh owner-controlled
  wallet `0x44887850795f0A5C45489aCb19E46E70DaEAB586`:
  - [install](https://sepolia.etherscan.io/tx/0xe0b1798b86c49f65a277811a9b4d87e919ff7cb8ec4ac595a2ecae7f336f1118);
  - [session approval](https://sepolia.etherscan.io/tx/0x736e3fe3dfcaee7dee48334d1cac567819e7e6a860f1370383cee8dad892d15b)
    charges ten USDC base units: remaining 500 → 490, used 0 → 10;
  - [uninstall](https://sepolia.etherscan.io/tx/0xfa60c1a6835c8c6119daeec2b04df7648a0bccfb23f6fda11c8bdfe9222d0506)
    clears registration and module storage;
  - [fixture recovery](https://sepolia.etherscan.io/tx/0x7c6e3d67c0528abff4e5d9a9f83aa198edb2c9f18188e697801e6bbd9d2499d5)
    resets the approval and returns wallet assets to the existing owner. A
    2,299,602,768,612 wei EntryPoint deposit remains in that owner-controlled wallet.
- The Sepolia fork test uses the real pinned factory/implementation and stock
  modules. Fork-only replacement of the TimeRange module's cleanup makes
  `onUninstall` revert without deploying an adversarial module publicly. It proves:
  a valid session signature before removal; successful uninstall with cleared
  account registration/hooks and residual signer/limits; exact
  `ValidationFunctionMissing(bytes4)` refusal of the same signed operation;
  failure of another account uninstall using the old hook list; and successful
  owner-directed module cleanup after restoring the module code. It also passes
  when launched from the race-enabled Go suite.

Reproduce opt-in checks (both require `-tags integration`):

```sh
WALLET_BUDGET_READ_LIVE=1 go test -tags integration ./core/taskengine -run '^TestWalletBudgetReadBeforeAfterSpend_Sepolia$' -count=1 -v
WALLET_BUDGET_RETIREMENT_FORK=1 go test -race -tags integration ./core/taskengine -run '^TestWalletBudgetRetirementCleanup_SepoliaFork$' -count=1 -v
```

The read probe uses the phase-1 fixture's Sepolia-only checks, funding ceiling,
0.005 ETH operation ceiling and no paymaster. The fork probe makes no public
transactions. Preparation/signing, budget installation, holds, discovery and
Skill integration remain the subsequent slices. The shared gateway process was
not replaced by these tests.
