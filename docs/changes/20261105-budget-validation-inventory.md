# Bound wallet-budget validation inventory reads

Budget submit could refuse a valid owner signature with 503
`BUDGET_READ_UNAVAILABLE` and `submissionStarted: false`. `CheckAuthorities`
queried `ValidationInstalled` from genesis through the pinned block in one
`eth_getLogs` request. Sepolia had more than 12 million blocks, while the local
RPC accepted at most 10,000 blocks per request. The same check protects budget
session runs, so the problem was not limited to first installation.

## Changes

- Read account code at the pinned block hash first. An undeployed account needs
  no validation-event scan. The snapshot must still pass `VerifyCanonical`.
- For ordinary deployed accounts, binary-search historical code to find a safe
  creation boundary, then include that block's constructor events in the scan.
  An RPC failure during this search is not treated as an empty inventory.
- Keep the full history for an EIP-7702 source. Removing its delegation can
  clear code without clearing storage, so the latest delegation is not proof
  that earlier installed authorities disappeared.
- Read logs in consecutive, inclusive ranges. The default maximum is 10,000
  blocks; override `smart_wallet.budget_log_block_range` for a single-chain
  gateway or `chains[].smart_wallet.budget_log_block_range` per gateway chain.
  Zero uses the default. Negative YAML values are rejected.
- Use the same bounded reads when reconciling owner-nonce consumption through
  EntryPoint events. A hold can outlive the provider's maximum log window.
- Continue checking each unknown candidate's current registration at the pinned
  block. Duplicate events do not repeat registration reads. Missing pages,
  unavailable state, cancellation and reorgs remain failures; they never release
  a hold or authorize a send from a partial inventory.

No API, SDK, stored-record or signing changes are required. This change does not
enable budget flags, alter prepared bytes or restart a gateway.

## Read-only provider checks

On 2026-11-05, authenticated read-only RPC probes confirmed that **local
Sepolia and production Sepolia, Ethereum and Base** each reject a 10,001-block
query with RPC code `-32005` and accept a 10,000-block query. Both Sepolia
endpoints support the historical-code probe. The reported salt-1 smart wallet
`0x6CAfAbB2Cf490fA83dc839266632C575665c0F93` has no code at the observed snapshot.
The Railway project currently has only its production environment; these
checks do not claim a separate hosted staging endpoint was tested.

Ordinary-account boundary discovery needs historical `eth_getCode`; an RPC
without that state is refused rather than guessing a lower bound. Delegated
EOAs retain a complete paged scan and can require more requests. No cache or
persisted scan cursor is introduced in this fix.

## Regression coverage

- Undeployed wallet at a 12-million-block snapshot makes no `eth_getLogs` call.
- A provider rejects ranges larger than its configured limit; a registered
  unknown authority in a later page is still refused.
- Constructor events, the final partial page, duplicate candidates, genesis,
  exact-limit ranges and the uint64 boundary remain covered.
- An earlier EOA install remains visible after a later delegation.
- Code, history, page and registration failures, malformed events, cancellation
  and reorgs remain closed, including the undeployed fast path.
- Stored budget submission reaches the offline fake relay for an undeployed
  account; an inventory failure reports `submissionStarted: false`, creates no
  hold and never calls that relay.
- Owner-operation reconciliation finds an inclusion in a later page.

The original implementation fails the undeployed-wallet, later-page-authority
and later-page-owner-operation regression tests against the capped RPC fixture.
The fixed budget/config tests pass with the race detector: 113 top-level tests,
229 including subtests. The full affected config, task-engine (including
subpackages) and REST packages also pass with the race detector. The REST
lifecycle fixture now models the wallet's creation in block 1 and permits only
the wallet-code boundary lookup at genesis; owner identity, nonce and fee reads
retain their final-snapshot assertions. Vet, lint and formatting checks pass.

Live probes are read-only; no budget was signed, broadcast or installed by this
verification. The shared local gateway was not restarted.
