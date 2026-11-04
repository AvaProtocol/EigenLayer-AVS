# Wallet budget slice 3d: owner submit, durable pause and activation

Budget signing remains **off by default**, and the signing path accepts only
Sepolia. No deployed configuration, service key, Studio integration or SDK
release is changed here.

## Contract and decisions

- `wallet_budget_preparation_enabled` permits unsigned reviews.
  `wallet_budget_signing_enabled` separately permits a first submit.
- `wallet_budget_max_fee_wei` is configurable. Its default is
  `5000000000000000` (**0.005 ETH**), as agreed. Prepare and first submit refuse
  a higher quote with `BUDGET_FEE_CEILING_EXCEEDED`. Every operation is
  self-funded, including cancellation. A configured paymaster is never used;
  insufficient ETH returns `BUDGET_FEE_INSUFFICIENT_ETH`.
- Migration still refuses outstanding legacy bearer signatures until their
  deadlines. Consuming an owner nonce does not invalidate a session nonce.
- Sepolia signing requires the accepted discovery policy: a complete Moralis
  inventory plus catalog pairs, a passing live discovery canary, and explicit
  acceptance of indexer omissions. A fresh service signature does not prove
  that dormant or unknown-token approvals were indexed. The tests here use
  throwaway service keys and known fixture pairs; they do not qualify that
  canary or provision the real discovery signer.
- Unsupported Permit2, NFT/operator, and unaudited callable routes are refused
  with `BUDGET_ROUTE_UNSUPPORTED`. The current route table covers chain catalog
  ERC-20 transfer/approve and the pinned router, Pool and wrapped-native routes.
  It does not claim protection from arbitrary indirect spending authority.
  The automatic reset matrix remains **Sepolia USDC only**.

## API and signing

Prepare stays `POST /wallets/{address}/policies:prepare`. The budget handoff is:

1. `GET /wallets/{address}/policies/preparations/{id}?chainId=` recovers the
   exact unsigned review, its state and reason, hash, fees and inclusion data.
2. `POST /wallets/{address}/policies/preparations/{id}:submit?chainId=` accepts
   **only `{ "signature": "0x…" }`**. It requires the authenticated wallet owner.
3. `POST /wallets/{address}/policies/preparations/{id}:cancel?chainId=` prepares
   a priced owner no-op on the held nonce. It does not send it. The owner signs
   that new preparation and submits through the same signature-only endpoint.
4. A normal new budget prepare on the same nonce is a replacement. Pending
   entities are reserved in storage, so earlier unsigned reviews cannot collide.

A successful cancellation reports `active` with `isCancel: true`. That means
the cancellation was included; no new permission is activated and the Skill
must not be enabled from that cancellation result.

The wallet still `personal_sign`s the **32 raw bytes** of `userOpHash`. The
gateway checks recovery, normalizes the recovery byte and frames `0xFF 0x00`.
Neither the priced bytes nor their gas, nonce, factory or paymaster fields can
be replaced by the client. Relay remains keyless, without gas/nonce repair.
Legacy policy submit and generic owner-execution submit do not accept budget
preparation IDs or budget input.

Before the first relay, one canonical snapshot checks the old permission,
remaining token/native caps, task set, legacy signatures, account identity,
owner nonce, reset allowances, entity reservation and retirement calls. Owner
factory/identity/nonce/funding reads use that block number; allowance and module
reads use its hash, and the hash is checked again before the marker. A changed
base or derivation returns `SESSION_POLICY_BASE_CHANGED`; failed reads are not
interpreted as zero. There is no reprice or automatic second operation.

## Durable state and execution pause

The preparation starts with a ten-minute TTL. Before any network send, a single
Badger transaction rewrites it **without TTL**, stores the pending permission,
and acquires the owner/wallet/chain/nonce hold. Atomic storage is required and
commit errors propagate. The pending permission is never usable. Activation
does not need a deferred bearer signature, and subsequent session operations
retain the execution-hook wrapper.

The states are `prepared`, `submitting`, `submitted`, `active`, `reverted`,
`rejected`, `unsent`, and `superseded`. A marker never becomes `unsent`.
`rejected` is unsettled: a fee rejection, timeout, lost reply or missing hash
does not invalidate an already signed operation. Repeated submit verifies the
owner signature but **never rebroadcasts**, even after expiry, restart, or a
post-send storage failure. Recovery works when signing is later disabled.

All attempts on one nonce share the hold. Only observed nonce consumption and
its canonical EntryPoint event can choose the winner. A rejected replacement
does not release the original's hold. A successful receipt stays `submitted`
until its reset proof passes; `active`, `reverted` and `superseded` settle it.

Every session broadcast records its exact hash/nonce before RPC. The send gate
waits for a session send already in progress, and final budget reads refuse an
unresolved prior flight or a snapshot older than a confirmed session receipt.
This prevents a spend immediately before submit from being restored by stale
remaining amounts. An unresolved session flight must settle before that submit
can proceed; it is never silently forgotten because of a timeout.

The hold controls Ava's sends. A stolen session key can still spend outside
the gateway during the change window, bounded by its old on-chain remainder.
This is the residual risk accepted in the contract, not an on-chain freeze.

On-demand runs return `BUDGET_CHANGING` before creating an execution when a hold
is open. Scheduled jobs remain durable and deferred, without a failed execution
or retry/execution-count increment. They rejoin the queue when checked again;
recovery restores all deferred wakeups, including backlogs over the buffer size.
A runner gate covers the whole workflow, and a separate send gate covers raw
session sends and all their nonce/gas retry broadcasts. Startup installs the
guard; every send reconciles that wallet's stored holds before proceeding.

## Reset evidence and external changes

Zero allowances are proven at the inclusion block. An unavailable read retains
`submitted` and the hold. A nonzero allowance raises an alarm and names its
token/spender in `reason`. The owner can revoke it through the approval scan;
with the hold still blocking Ava's approvals, a later current-state recheck can
complete activation. Activation, superseding the old permission, settling all
attempts and releasing the hold commit atomically.

Direct owner broadcasts are found through the canonical EntryPoint event, not
bundler receipt absence. An unknown consuming operation never activates a
different budget. Validation-install events supply candidates, and account
registration reads identify unknown live entities/modules. Those keep runs
paused with `BUDGET_AUTHORITY_UNKNOWN` until the owner removes them and checks
again. Known retired entities remain separately reported as registered,
residual or unverified; retirement read health does not block amounts or a
successful budget activation. Entity IDs remain reserved permanently.

The chain RPC must provide account validation-install history and historical
inclusion-block allowance reads. If either is unavailable, signing/activation
fails closed. Unsigned reviews are still ephemeral; broadcasting an operation
independently after its unsigned preparation was deleted is not an activation
API. Unknown authority is refused, never reconstructed as an approved budget.

## Verification and remaining release gates

Offline coverage includes mounted HTTP prepare/sign/submit/recovery, deployed
and counterfactual accounts, strict request/chain parsing, final-block pinning,
storage failures before and after relay, TTL removal, restart, old-signature and
fee guards, pending entity collision, stale spend/allowances, replacement/cancel,
reset read failure/repair, unknown consuming nonce/authority, queue deferral and
single spend after resume. The live tests skip in ordinary CI.

Opt-in Sepolia command:

```sh
WALLET_BUDGET_SIGNING=1 go test -tags integration ./core/taskengine \
  -run '^TestBudgetOwnerHandoff_Sepolia$' -count=1 -timeout 20m -v
```

It uses fresh salts, the existing test owner, throwaway session/discovery keys,
no sponsorship, 0.02 ETH temporary funding per wallet, and a 0.005 ETH cap per
operation. It checks an undeployed wallet's first install, insufficient ETH,
an actual legacy migration with two USDC approval resets (one unlimited), and
an ordinary wrapped session operation consuming native budget after activation.
It returns fixture tokens/native funds and clears both router/Pool approvals;
any EntryPoint remainder stays controlled by the same owner and is reported.

The complete Phase 1 release checklist remains required before enabling
signing. In particular, qualify real Moralis later-page discovery, a scheduled
run/in-flight spend through a live change, every settled state, and the failing
hook's owner-directed module cleanup. The existing `wallet_budget_retirement`
Sepolia fork is the proposed deterministic proof for forced cleanup failure;
forced reset failures and deterministic same-nonce races should also use forks,
with normal canaries against the actual bundler. No mainnet signing is enabled.

The following SDK release needs **methods and types**, wrapping budget read,
preparation read, signature-only submit, and cancel prepare with explicit
chain IDs. Studio then waits for `active`, presents pending pauses and recovery,
and replaces its interim expiry read. Neither a types-only publish nor this
gateway change completes that integration.

## Recorded validation (2026-11-03)

- The affected-package race suite passed 2,883 test cases/subtests, with ten
  existing skips. The final budget/HTTP/storage/queue tests also pass after the
  RPC-hash fix. All packages compile with the integration build tag.
- Changed-line lint, the build, formatting and storage comparison against
  `origin/staging` pass; storage changes are additive.
- `TestWalletBudgetRetirementCleanup_SepoliaFork` passes, including the actual
  Go-encoded owner-directed module cleanup after failed uninstall cleanup.
- The full live handoff test passes together (139.59 seconds):

| Probe | Budget install / migration | Subsequent session |
| --- | --- | --- |
| Counterfactual wallet | [transaction](https://sepolia.etherscan.io/tx/0x00b10eae4c361e3b82124c159239af8103b43fa4dc1c244c56b6f076bcaeb4b9) | [transaction](https://sepolia.etherscan.io/tx/0xdcf59675238aa922fafe3d192d9b0dc043e449c451e76c601f9f245511dee23b) |
| Legacy migration, two USDC resets | [transaction](https://sepolia.etherscan.io/tx/0xe0ffee38060538c4fe551b00f226f6d5cb85a0b1ad68eed6cc92bb2dfc61ceae) | [transaction](https://sepolia.etherscan.io/tx/0xcfeae73db57296d0174cfa03f97db718a05359ff40adfad5a5b3dbc72caeca0c) |

Both session probes reduce the native remainder from 0.006 ETH, proving that
the activated permission resolves, wraps its execution hooks and meters gas.
Fixture assets and both approvals were recovered/cleared. Each successful
wallet retains about 0.00002971 ETH of owner-controlled EntryPoint prefund,
which the recovery log reports explicitly.

Initial live attempts were not passes: two fixture assumptions needed fixing
(native-funding gas estimation and a mismatched migration total), and hash-pinned
reads exposed a real integration error. The gateway now retains the RPC's
reported block hash rather than reconstructing it from a decoded header. A
regression test covers that difference and a canonical hash changing. These
fixes were included in the single passing full live run above.
