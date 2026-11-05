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
- Persist a hash-anchored deployment boundary and a finalized inventory cursor
  per chain and wallet. Every complete finalized page stores its cursor and all
  candidates together, so a timeout or restart resumes from the last complete
  page instead of rescanning the wallet's lifetime.
- Warm that inventory during unsigned budget preparation, after validating the
  owner operation and before returning a signable review. This takes no wallet
  run/send gate. A failed warmup returns no review or hold; submit still checks
  current authority under its final send gate.
- On subsequent checks, verify the cached checkpoint's canonical hash, scan only
  new finalized blocks and re-read the unfinalized tail through the pinned head.
  A reorged or malformed checkpoint is rebuilt. An older pinned view cannot
  overwrite a newer cursor. Concurrent checks share the backfill work.
- Cache candidates, including ones currently known to Ava or unregistered;
  never cache the authority decision. Every check reloads the known policies
  and reads unknown candidates' registration at the current pinned block. A
  later reinstallation or policy change cannot hide behind a cache hit.
- Use the same bounded reads when reconciling owner-nonce consumption through
  EntryPoint events. A hold can outlive the provider's maximum log window.
- Continue checking each unknown candidate's current registration at the pinned
  block. Duplicate events do not repeat registration reads. Missing pages,
  unavailable state, cancellation and reorgs remain failures; they never release
  a hold or authorize a send from a partial inventory.
- Preserve the authored unknown-authority refusal without adding an internal
  inventory prefix to the run-path message.

The derived inventory uses additive `budgetauthority:v1:{chain}:{wallet}`
records. Existing permission, hold and preparation records are unchanged; older
versions ignore this cache. No API, SDK or signing changes are required. This
change does not enable budget flags, alter prepared bytes or restart a gateway.

## Read-only provider checks

On 2026-11-05, authenticated read-only RPC probes confirmed that **local
Sepolia and production Sepolia, Ethereum and Base** each reject a 10,001-block
query with RPC code `-32005` and accept a 10,000-block query. Both Sepolia
endpoints support the historical-code probe. The reported salt-1 smart wallet
`0x6CAfAbB2Cf490fA83dc839266632C575665c0F93` has no code at the observed snapshot.
The Railway project currently has only its production environment; these
checks do not claim a separate hosted staging endpoint was tested.

Production Ethereum and Base were also checked with complete historical-code
binary searches over the deployed MA v2 factory. Ethereum served all 25 reads
and located block 21,426,430; Base served all 26 and located block 23,849,125.
Both endpoints returned code at the discovered block and no code immediately
before it, and both served the `finalized` block tag used by the cache. Local
and production Sepolia were separately verified to serve that tag too.

A cold ordinary-account lookup still needs historical `eth_getCode`; an RPC
without that state is refused rather than guessing a lower bound. Delegated
EOAs start their first inventory at genesis. Initial backfills can be expensive,
but completed finalized pages survive failures and restarts. Warm checks do no
deployment search and query only blocks since the saved cursor plus the
unfinalized tail; cost scales with new blocks and candidates, not wallet age.

The per-page canonical confirmation is intentional. A finalized range is saved
before the scan's final check. If a reorg changes the prefix between two pages,
dropping that confirmation can save old-fork candidates under a new-fork
checkpoint; the final refusal would not undo that saved record. A regression
test reproduces this failure when the check is removed. The extra request is
retained for resumable backfills; first setup now does that work before the
owner signs. A later cold repair can still delay a budget run until its safety
check completes.

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
- Warm checks and a new Engine reuse the saved prefix without historical code
  reads or lifetime log queries, including for delegated EOAs.
- An interrupted backfill resumes at the first missing page and retains earlier
  candidates. Cache hits still detect a newly live authority or changed policy.
- An unfinalized reorg is detected by re-reading the tail; a changed finalized
  checkpoint forces a full rebuild. Malformed/cross-wallet/cross-chain records,
  checkpoint read failures and storage failures never authorize a send.
- Older snapshots cannot regress the stored cursor; concurrent checks backfill
  once and independently recheck current registration.
- Preparation warms before returning a review, without taking wallet run/send
  gates; submit still refuses a newly live authority after a successful warmup.
- A mid-scan reorg cannot promote a mixed-fork prefix to a canonical checkpoint,
  even if the final check would have refused the send.

The original implementation fails the undeployed-wallet, later-page-authority
and later-page-owner-operation regression tests against the capped RPC fixture.
The fixed inventory/config tests pass with the race detector. The REST
lifecycle fixture now models the wallet's creation in block 1 and permits only
the wallet-code boundary lookup at genesis; owner identity, nonce and fee reads
retain their final-snapshot assertions.

The complete affected config, task-engine (including subpackages), and REST
suites pass with the race detector: 1,327 top-level tests, 2,812 including
subtests. Vet, lint, formatting, diff checks and the storage compatibility
check against `origin/main` pass.

Live probes are read-only; no budget was signed, broadcast or installed by this
verification. The shared local gateway was not restarted.
