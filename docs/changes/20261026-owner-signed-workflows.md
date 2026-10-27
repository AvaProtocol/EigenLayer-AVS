# Owner-signed Manual workflow handoff

Date: 2026-10-26
Status: Owner-signed HTTP execution verified on Sepolia; SDK/browser integration pending
Branch: `feat/1987-owner-signed-workflows`
Related: [Studio #1987](https://github.com/AvaProtocol/studio/issues/1987),
[SDK workflow/simulation draft #289](https://github.com/AvaProtocol/ava-sdk-js/pull/289)

## Behavior

A user can prepare a Manual workflow's resolved ContractWrite calls, review a
priced UserOperation, sign its hash with the account owner, and continue the
same operation through the gateway. This path needs no session grant or
server-held owner key. Existing Auto/session execution and ordinary simulation
keep their behavior.

Enable `owner_execution_enabled: true` in a local gateway config to expose the
handoff; the default is false. Disabling it prevents new preparations and first
submissions while preserving reconciliation of already attempted submissions.

## Existing execution interfaces and the added contract

The current authenticated trigger immediately runs the session-authorized
workflow. It cannot pause to return a priced operation for a transaction
signature. An Await signal belongs to an already authorized durable workflow;
it does not provide the owner's transaction authority. Ordinary simulation must
remain a preview and cannot silently become a prepare or submit operation.

The additions are therefore generic actions on the existing workflow/execution
resources, not sweep-specific endpoints, a new node or an independent service:

- `POST /api/v1/workflows:prepare` accepts `chainId`, `trigger`, `nodes`, `edges`
  and `inputVariables`. It reuses the existing graph runner for read/check work
  and captures the terminal ContractWrite's resolved calls before any session
  preflight, Tenderly write or submission. It returns the priced unsigned v0.7
  operation, hash, owner, EntryPoint, chain, decoded calls, maximum gas cost,
  final funding choice, execution ID and signing deadline.
- The execution is `waiting`. Completed steps contain the trigger and read/check
  nodes. The unexecuted write is identified by `writeNodeId` and `calls`; no
  successful write or fabricated receipt is returned. This is a transient Manual
  definition, not a deployed workflow/history record.
- `POST /api/v1/executions/{id}:submit` accepts only `{ "signature": "0x…" }`.
  Unknown fields are rejected: callers cannot replace calldata, chain, nonce,
  gas, paymaster or any other operation field. A 202 response gives the same
  hash and relay state; it is not an execution receipt.
- Poll the existing `GET /api/v1/userops/{userOpHash}?chainId=…` for pending,
  confirmed or failed status and decoded calls. No additional status service is
  introduced. A concrete user JWT is required for every action; a partner
  assertion alone is insufficient.

`api/openapi.yaml` defines the contract and generated Go handlers/types are
regenerated with `make rest-gen`. Thin SDK methods/types and extension of the
existing SDK example are the next integration step. The SDK's simulation-only
`authorizationMode` and scheduling fields are not inputs to prepare.

## Initial supported shape

- One configured chain, one registered MA v2 derived wallet, and its enabled
  undelegated EOA fallback owner. The current factory must derive the selected
  source from that owner and stored salt.
- One Manual trigger, read/check nodes (`ContractRead`, CustomCode, Branch or
  Filter), and exactly one terminal ContractWrite. At most 64 nodes, 128 edges
  and 32 calls. No write Loop, additional writes, post-write nodes or forwarded
  native value. Unsupported graph shapes fail before runners execute; resolved
  calls are also validated before pricing.
- Each method call is defined explicitly in the node. The ordinary per-call
  template/ABI encoder and pre-flight checks resolve its target and calldata.
  A runtime CustomCode array does not become a call list.
- Use `PackExecuteBatchMAv2` for the batch. This is the current-staging successor
  to the audit's `PackExecuteBatchAuto`; staging `3bffd581` removed v0.6 account
  support. The session batch's `submitSmartWalletUserOp` is not reused because it
  still requires a session grant.
- The generic gateway operation is not a token-discovery or allowlist service.
  The first Skill/example remains responsible for its Sepolia USDC/WETH catalog
  selection, exact amounts, exclusions and token-return simulation checks.
  EVM calls are atomic on revert; a token returning false without reverting
  needs token-specific handling. Native ETH sweeps, contract owners and EIP-7702
  setup/source support remain follow-up work.

## Authority, pricing and immutable continuation

Preparation checks the chain RPC's chain ID, derives through the canonical
factory, and verifies deployed proxy implementation, EntryPoint and
`getFallbackSignerData()` owner/enabled state. Both deployed and counterfactual
accounts require the verified factory implementation
`0x000000000000c5A9089039570Dd36455b5C07383`
(`alchemy.sma-bytecode.1.0.0`). Another implementation is refused even on a fresh
prepare. Counterfactual accounts attach owner/salt deployment data. Owner code,
account code, implementation code and factory identity are checked for supported
compatibility; current account/implementation/factory hashes are checked again
at first submission. A stored wallet association alone does not establish this
on-chain authority.

The owner nonce uses entity `0` plus `ValidationOptionGlobal` (`1`) and the
matching EntryPoint key/sequence. No session nonce cache or nonce repair is used.
Pricing reuses `priceOperationV07`, including the existing paymaster policy and
its refusal behavior. Source-funded execution checks native balance plus
EntryPoint deposit against the signed maximum gas exposure, including for an
ERC-20-only batch. Sponsorship fields are finalized before signing; refusal
never silently falls back to charging the source.

Owner preparation starts gas limits at zero so the bundler computes them before
signing. Nonzero historical seeds can be echoed rather than recomputed. A
read-only Sepolia prepare probe against the existing two-token fixture initially
failed `AA26 over verificationGasLimit` with the old 60,000 seed. Computed pricing
succeeded with a verification limit of 184,877 and returned an unsigned operation;
the probe invoked no send method. This is preparation evidence, not inclusion or
signature verification on-chain.

The wallet signs the **32 raw bytes** of `userOpHash` with `personal_sign`:
ethers `signMessage(getBytes(userOpHash))`, with the EIP-191 prefix applied once.
The gateway verifies recovery against the owner, requires canonical low-s ECDSA,
normalizes `v` from `0/1` to `27/28` while retaining `27/28`, then adds the MA v2
`0xFF 0x00` framing. These rules are specific to the supported fallback validator.
The deployed 1.0.0 validator applies `toEthSignedMessageHash()` to the userOp
hash. Alchemy's later 1.1.0 source removes that wrapping and is incompatible
with this recipe. The implementation guard prevents offering `personal_sign`
for an unverified build; it does not silently select another signing method.

Before first relay, the persisted definition is run again in read/check capture
mode. It must resolve to byte-identical calls; changed balances or branch
conditions cause refusal, not substitution of new amounts. Pricing is not run
again. The account identity, nonce and source funding are also rechecked. State
can still change between these checks and on-chain inclusion.

`SendUserOpV07` submits the exact priced fields with the owner signature. The
server-key `SendUserOpV07WithRetry` path is not called. Verification-gas or nonce
changes require fresh prepare/review/signing. Session validation and retries
remain unchanged.

Account validation sources: Alchemy's pinned 1.0.0
[fallback validation](https://github.com/alchemyplatform/modular-account/blob/bd75c9eca7ff5a66aaebc3cf2dab43ddc4622482/src/account/SemiModularAccountBase.sol#L120),
[account ID](https://github.com/alchemyplatform/modular-account/blob/bd75c9eca7ff5a66aaebc3cf2dab43ddc4622482/src/account/SemiModularAccountBytecode.sol#L40)
and the later [removal of EIP-191 wrapping](https://github.com/alchemyplatform/modular-account/commit/88ee1ff359a22f155c779e2e9ef4bd04d821f412).
A read-only Sepolia identity probe confirmed the factory implementation above,
its 1.0.0 account ID and runtime code hash
`0x3612f3fa5ac5e89b87b284024977e6082441e30ff05f9f8497cd280131a27e16`.
This probe signed and broadcast nothing. The gateway checks the configured
chain's actual contract reads during both prepare and the first submit; graph
simulation mode prevents the captured write from being submitted, not these
read-only RPC calls.

## Persistence and uncertain broadcasts

Preparations are owner-scoped TTL records under additive
`ownerexec:<owner>:<executionId>` keys. They contain the unsigned operation,
integrity hash, account identity, source salt and read/check definition/input.
They contain no owner private key or signature. Existing task/execution/storage
keys and persisted model fields are unchanged.

The gateway signing window is ten minutes. Abandoned records expire in storage.
This deadline is not an on-chain signature expiry or a revocation mechanism;
discarding a signing request broadcasts nothing through this gateway.

Before the first send, a durable `submitting` marker is saved under the existing
per-execution lock. Failure to save that marker prevents the broadcast. Submitted,
uncertain and rejected records are retained for 24 hours; all retained records
are capped at 64 per owner. Concurrent/repeated submissions and restart recovery
return the existing hash without rebroadcasting. A known bundler precheck
rejection requires fresh consent. Transport/internal errors and already-known
responses remain uncertain and must be reconciled by that hash. No automatic
second operation is constructed.

A crash after the marker but before the RPC can leave an operation unsent and
its preparation ID unable to broadcast again. Poll the hash returned by prepare.
Another broadcast needs a new prepare/review/signature. A storage failure after
a successful send returns an error, while the original marker remains
`submitting`; repeating submit returns that same hash without another send.
A status lookup returning 404 is still unresolved: it may not be indexed or
may never have been accepted. SDK/UI polling must retain that uncertainty
instead of claiming failure or automatically preparing a replacement.

Every submit request still needs a valid owner signature, including repeats for
`submitting`, `unknown` and `submitted` records. The gateway does not retain
that signature. Polling the existing UserOp status route requires the user's JWT
and existing wallet association, without an owner transaction signature.

## Verification

Focused race-enabled tests cover the complete local HTTP flow, real graph data
flow into two transfer calls, raw-hash signatures and framing, exact priced
fields on keyless relay, owner/account/nonce/funding changes, stale read results,
wrong signer, malformed/extra submit fields, expiration, feature rollback,
storage failure before and after send, concurrent retries, restart/uncertain
broadcasts, cached-submit signature checks, unverified implementation refusal,
gas rejection,
sponsorship refusal, bounds and existing UserOp status reuse. External chain and
bundler calls in these tests use local HTTP fixtures; no live sweep is broadcast.

Verified 55 focused cases with race detection. The new implementation-refusal
tests fail for both deployed and counterfactual accounts without the guard and
pass with it. Workspace binary build, vet,
formatting and the storage check pass; the storage change is the additive
`ownerexec:` namespace. Independent preset, account encoding, config, REST mapping
and middleware package suites also pass.

```bash
go test -race -short ./core/taskengine ./pkg/erc4337/preset ./aggregator/rest \
  -run '^TestOwner|^TestPermissionMap|^TestColonAction' -count=1
```

The broad affected-package run in this fresh worktree cannot complete existing
configuration-dependent tests without `config/test.yaml`. CI run
[40432706488](https://github.com/AvaProtocol/EigenLayer-AVS/actions/runs/40432706488)
passed the full affected package suites, including taskengine, preset and REST.

### Live owner-signed Sepolia execution

On 2026-10-26, the local stack's gateway was replaced with this branch's
`cd61c382` binary, retaining the existing configuration, database, ports, workers
and operators and enabling the feature for that launch. A separate HTTP probe
used the existing development fixture owner to sign the raw 32-byte prepared
hash with ethers `signMessage(getBytes(userOpHash))`.

- Source: `0x60aCdE1B4fC1848647eaB6F1460a33F4cA2b520E`.
- Recipient: the fixture owner,
  `0x2FB186De3d32F4EB45A58F6dD7133A2442f9830e`.
- One source-funded UserOperation:
  `0xd5970d7a3ed0f15bc17d90b380b0691ab5e92757056f569468c11d360e8404da`.
- Confirmed transaction:
  [0xd3630746…3140e661](https://sepolia.etherscan.io/tx/0xd3630746908f6366904afc6482fd6cd66771e2d31368d0a1fa3f392b3140e661),
  block `12002748`, successful UserOperation event with the prepared owner nonce
  and two matching ERC-20 Transfer events in that transaction.

| Token |        Source before | Source after | Recipient before |      Recipient after |
| ----- | -------------------: | -----------: | ---------------: | -------------------: |
| USDC  |                 13.3 |            0 |         1.500489 |            14.800489 |
| WETH  | 0.000002877930532126 |            0 |                0 | 0.000002877930532126 |

Both balance deltas matched the exact prepared base-unit amounts. Repeating
signature-only submit returned the same hash and submitted state. Session
policies were unchanged. Actual gas cost was `0.000038235905353026` Sepolia ETH,
below the signed maximum of `0.000043129212076176` ETH.

The independent chain RPC initially lagged the bundler's confirmed status;
read-only receipt verification subsequently passed without another broadcast.
No owner signature, private key or JWT was stored in the probe artifacts.
Live counterfactual deployment, sponsorship and browser-wallet signing remain
separate verification cases. The SDK example still previews/simulates only.
