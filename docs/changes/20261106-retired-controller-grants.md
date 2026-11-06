# Remove retired Ava grants before installing a budget

An older Ava grant may still be registered on chain even when this gateway has
no policy record for it. Budget authority checks previously reported every such
registration as `BUDGET_AUTHORITY_UNKNOWN`, with no removal payload.

## Recognition and owner removal

`wallet_budget_retired_session_signers` is an explicit map from chain ID to
public retired Ava signer addresses. It defaults to empty. Operators must pin
the historical controller or rotated session signer on each affected chain;
there is no implicit cross-chain fallback and no private key is needed. Do not
add a foreign signer merely to suppress a refusal.

The pre-#864 controller was `0x82F2Dd9a552a69f2CED7ff2D05c43aB8430158fb`
on mainnets as well as testnets. Pin it on all eleven chains served then:
Ethereum (1), Base (8453), Sepolia (11155111), Base Sepolia (84532), BNB (56),
Arbitrum (42161), Optimism (10), Unichain (130), Robinhood (4663), Polygon (137)
and Hyperliquid (999). Applying this configuration must follow deployment of
the gateway code in this change.

For an unrecognized, currently registered SingleSigner entity greater than
zero, the gateway reads its signer at the pinned block. If that signer is in
the chain's retired list, prepare and submit return **409
`BUDGET_RETIRED_GRANT_INSTALLED`**, with `onChainCleanup` in the existing revoke
shape: `entityId`, `target`, `callData`, `chainId`. The target is the owned wallet.
Submit preserves `submissionStarted: false` when this check refuses before send.

The grant remains a blocker. Returning a payload never installs a budget,
trusts the retired key, or sends a transaction. The owner reviews and sends the
removal, waits for confirmation, then prepares again. Multiple retired grants
are removed one at a time. A foreign registered authority takes precedence and
still returns `BUDGET_AUTHORITY_UNKNOWN`, without a cleanup. Entity zero is
never offered for removal. Budget runs stay paused with the existing
`BUDGET_CHANGING` contract and authored, code-free wording.

## Historical teardown

New inventory entries retain the install event's transaction, block hash and
log position. Recovery checks the matching receipt/event, wallet, module,
entity and live signer. Recovered hook data must use Ava's known module formats
and name that same entity. It supports a direct self-install or a canonical
EntryPoint v0.7 `handleOps` install, including a deferred action carried in the
UserOperation signature. Nested batches follow only zero-value self-calls.
Ambiguous or mismatched history is not used. A recovered install is passed to
`SessionSignerUninstallFromInstall` so the original hook data and teardown
order are retained.

Pruned history, unsupported transaction wrappers and missing provenance use
`PackSessionSignerUninstall(entity, nil)`. This intentionally
skips hook-module cleanup while removing account registration and clearing the
SingleSigner slot. Any remaining hook storage is inert after deregistration;
it must not be described as fully cleaned. Before returning any retired cleanup,
the gateway durably reserves that entity and all earlier IDs from future
allocation. This also protects direct installs whose deferred nonce was never
used. The per-chain/wallet high-water mark only increases and is independent of
the rebuildable inventory cache. Recognition, registration and signer
read failures still fail closed. An interrupted request returns an error rather
than a fallback derived after cancellation. No synthetic policy records are
created. The reservation key is additive; no existing records are rewritten.

## First-scan latency

The derived inventory namespace and record version are now v2. v1 records are
left untouched and ignored, so previously scanned wallets rebuild once and
capture install provenance. Permanent entity reservations use a separate key
and survive this rebuild. A complete v2 checkpoint is reused on the next read
and after restart; interrupted v2 backfills retain their complete prefix.

Long finalized inventory backfills overlap at most four log-range requests.
Results are consumed and checkpointed in ascending range order, with the
existing per-page canonical check. A failed or cancelled page never advances
the cursor past a gap; retries keep the complete prefix. Short/warm scans,
unfinalized tails and owner-operation reconciliation retain sequential reads.
This reduces provider round trips on a cold scan but does not guarantee a
prepare fits a particular client timeout. There are no background scans.

## Verification and rollout

Offline tests exercise the real authority check and both preparation stages,
foreign/retired signer separation, exact historical hook recovery, missing or
ambiguous history fallback, owner-removal readback, REST serialization, and
parallel scan cancellation/checkpoint recovery. All fixtures use synthetic
wallets; no user transaction or private key is recorded here.

After gateway merge, sync the SDK's vendored OpenAPI from that immutable commit
and ship the additive optional `Problem.onChainCleanup` type. Studio can then
map the new refusal to its owner-sent removal flow. Configure the retired
signer pins before expecting older grants to receive the new refusal. Live
owner-signed removal of a fresh fixture is a separate check; this change does
not restart the local stack, alter deployed configuration or broadcast funds.
