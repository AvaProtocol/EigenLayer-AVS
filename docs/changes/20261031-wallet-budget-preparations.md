# Wallet budget slice 3c: unsigned preparation and trusted discovery

Budget signatures remain disabled. `wallet_budget_preparation_enabled` defaults
false; enabling it permits read-only preparation and review recovery, never
submission. Existing non-budget deferred grants and Auto/session execution keep
their existing path.

## Contract

`POST /wallets/{address}/policies:prepare` accepts budget intent plus a fresh
trusted `approvalDiscovery`, performs the additive composition from #883, reads
remaining caps and allowances, builds reset → registered-entity retirement →
install as one `executeBatch`, and prices it through the pinned MA v2 owner
path. It explicitly disables sponsorship without mutating chain configuration.
It never stores a usable session policy or acquires a wallet execution hold.

The separate `budgetprep:<owner>:<id>` record retains the exact priced unsigned
operation, hash, maximum fee, account identity, original intent, base policy,
remainder, enabled tasks and derived rows. It lasts ten minutes, with at most
64 unexpired unsigned records per owner. Authenticated
`GET /wallets/{address}/policies/preparations/{id}?chainId=` returns that same
review, without repricing or reading balances again. Corrupt and cross-owner,
wallet, chain or expired records cannot return an operation.
Recovery rebuilds the exact reset, retirement and install calls from the
displayed review and checks the carrier nonce against the operation nonce,
as well as recomputing its hash and maximum fee. Preparation uses independent
per-owner, context-aware locks rather than blocking execution-resume shards.
An unreadable record fails closed until its TTL expires; its key is logged
without its contents so the blockage can be diagnosed.

The response uses `signing.kind: owner_operation` and advertises the existing
`personal_sign` of the **32 raw bytes** of `userOpHash`. It omits legacy digest
and typed data. The reserved signature-only budget submit route always refuses
with `BUDGET_HANDOFF_REQUIRED`. Neither it, the legacy policy submit, nor the
generic owner-execution submit can relay a budget preparation.

Known, still-valid stored legacy deferred signatures refuse migration even if
revoked off chain. Their session nonce differs from the owner nonce, so an
owner operation cannot silently invalidate them. The refusal includes the
latest outstanding deadline; today's default signing window can last 30 days.
Off-chain revocation does not shorten it, and there is no nonce-invalidation
remedy in this slice. Historical registered entities are included in retirement, without
truncating an oversized batch or silently marking anything cleared.

## Discovery and reset scope

Studio already has full paged, unfiltered approval discovery. Its companion
3c change signs that inventory at the trusted server boundary using a **new**
secp256k1 service key: `GATEWAY_STUDIO_BUDGET_DISCOVERY_PRIVATE_KEY`. The gateway
stores only its public Ethereum address in `wallet_budget_discovery_signer`.
This key is unrelated to Studio's Ed25519 partner key, the session signer and
any owner's wallet. No key or deployment configuration is changed here.

The EIP-712 domain is `Ava Wallet Budget Approval Discovery`, version `1`, the
chain id and `verifyingContract = wallet`. The primary type is:

```
ApprovalDiscovery(address owner,address wallet,bytes32 candidatesHash,bytes32 allowedContractsHash,string source,uint256 fetchedAt,bool complete)
```

`candidatesHash` is keccak256 of deduplicated pairs sorted by token then spender
address bytes, each address padded to 32 bytes, concatenated without an ABI
array-length prefix. Source is `moralis`, timestamp is Unix milliseconds from
the first inventory read, completeness must be true, and the low-S 65-byte
signature is hex on the wire. `allowedContractsHash` hashes the sorted unique
callable targets as padded address words. Every resulting action/native recipient
must be in that signed scope, so a caller cannot replay a narrower discovery
for a new contract and leave its old approval out. Recovery bytes 0/1 or 27/28 are accepted. The
gateway requires a matching owner/wallet/chain and age at most two minutes,
and rechecks freshness after pricing. Independently computed ethers/Go golden
vectors agree. A signed partial inventory is unavailable, never an empty list.

The gateway reads actual `allowance(wallet, spender)` for reset-scope candidates,
expanding known token pairs across newly allowed targets (or all targets for
migration and new totals). Native-recipient targets are included: a contract
fallback or delegated account can spend an old approval without calldata.
Pairs for unchanged targets needing no reset do not make redundant RPC reads;
the full signed inventory remains in the intent record.
Missing/replayed/forged/stale attestations and failed/malformed reads fail
closed. No allowance amount from discovery is trusted.

Attestation freshness does not bound Moralis's indexer lag or prove inventory
completeness. Missing approvals for unknown tokens remain a residual risk;
adding currently held tokens would help but would not close dormant-token
gaps. The discovery-completeness policy and live chain coverage must be settled
before signing is enabled. Direct ERC-20 resets do not inspect Permit2's
internal/transitive permissions, NFT operator approvals or other mechanisms.
The mainnet gate must classify allowed spending routes and refuse unsupported
authority or expose it as explicitly unmetered with owner consent. This slice
does not claim arbitrary indirect spending is constrained.

Automatic approve-zero resets currently cover **only Sepolia USDC**, address
`0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238`, as proven in #880. A positive
allowance requiring reset on another deployment returns
`BUDGET_RESET_UNSUPPORTED_TOKEN` with token/spender; the card should ask the
owner to revoke it through the existing approval scan and retry. A zero
allowance requires no reset. WETH/USDT or another chain must get deployment-
specific zeroing and failure-semantics tests before automatic reset support.

Before 3d enables mainnet signing, Studio's API contract §6.4.1 requires
pinned fork reset proofs for each configured Ethereum/Base USDC, USDT and WETH
deployment, plus one live gateway/bundler canary per chain. Forks count as
token-semantics evidence; Base's bridged USDT is a separate deployment. No
mainnet evidence or automatic-reset allowlist expansion is claimed here.

At Review integration, Studio may reuse successful fresh signed discovery for
an unchanged owner/wallet/chain/allowed-target scope, signer and catalog. Keep
the original first-read timestamp, leave pricing headroom inside two minutes,
and deduplicate concurrent scans. Never refresh a timestamp without reading
again, cache failures, or reuse on-chain allowance decisions/priced operations.
The current 3c Studio module reads its service key through validated `env` and
still performs discovery on each call; the bounded cache belongs to 3d wiring.
The scan and signing now share a 45-second bound, leaving 15 seconds of margin
after a full 60-second gateway prepare inside the two-minute freshness limit.
Studio skips malformed token data only when the spender is provably unrelated;
ambiguous or relevant rows still fail closed. Its 100-page limit counts raw
pages, while the 10,000-pair limit counts matching/catalog pairs; exceeding
either produces an availability diagnostic, never a partial attestation.

Self-funding shortfalls return `BUDGET_FEE_INSUFFICIENT_ETH` with needed and
available wei when quoted. Some bundlers reject prefund during estimation;
that still returns the Add ETH code but never invents an unquoted maximum fee.
No budget path retries with a paymaster. A capped token retained as an inert
row also cannot become an unrestricted native-recipient target.

## Verification and remaining gates

Offline tests exercise trusted signatures and tampering, complete discovery,
live allowance decisions, unsupported resets, migration, exact priced storage,
concurrent record limits, corrupt/cross-owner reads, counterfactual factory data,
self-funded fee refusal, and every submit boundary. Mounted HTTP tests use local
chain/bundler fixtures; no real signature or transaction is sent.

Slice 3d must implement final base/remainder/task/approval/identity/nonce reads,
the durable wallet/chain hold, signature-only relay, replacement/cancel and
restart reconciliation, activation and retirement/module-health reporting.
Final checks must cover newly pending legacy signatures and changed enabled
tasks as well as the base policy. Reserve the entity with the durable hold,
use and record a canonical block for allowance reads, verify post-install
allowances, and reconcile known operation hashes/nonces and unknown on-chain
entities after direct owner broadcasts. Ten minutes limits signing and first
send, not on-chain validity; unsettled signed operations need durable records.
A chain-appropriate configurable fee ceiling and a proven owner remedy for
legacy pending signatures remain 3d decisions, not arbitrary new limits here.
Preparing a review is not evidence of a mined installation. The full live gate
in Studio's `PLAN_WALLET_BUDGET_PERMISSION.md` Phase 1 and
`docs/WALLET_BUDGET_API.md` §6.6 remains required, including the second Skill
without signing, cross-surface remainders, approval attacks and narrowing,
counterfactual installation, failed-uninstall **and actual owner module cleanup**,
rejected replacement while the original remains submitted, restart holds, and
all settled states. Untested token resets need their own proof first.

Studio's follow-on must wait for `active` before Allow and Enable continues;
use one `readWalletBudgetAction` for Skill/Auto/MCP/Wallets; bind its card to
stored structured review data; migrate nullable manual-Skill expiry and hide
manual run/date controls; and update the UI/security design docs. Its current
legacy permission-expiry read is interim.

Legacy percentage splits are unchanged: a positive cap is an owner-selected
shared ceiling and can report `covered` even when the actual balance-sized
payment is unknown. #883's remainder preflight is budget-only. An over-cap
legacy payment still uses the existing account/bundler failure path; it does
not acquire the new budget refusal code.

The legacy over-cap send path has no cap-specific error code for Studio to
match. Method failures can be text-only and the step becomes `INVALID_REQUEST`;
AA23 is generic and can hide the original token-limit reason. Review can show
the existing cap and offer to raise it now. Run history must not call every
such failure an exhausted cap; definitive wording needs decoded evidence or a
future narrow diagnostic. No legacy gateway behavior changes in 3c.
