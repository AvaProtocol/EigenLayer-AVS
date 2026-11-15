# Global smart wallet identities and active limit per EOA

**Status:** Revised proposed design and implementation plan. **Date:** 2026-11-15. **Scope decision:** Option 2, confirmed by the user: one inventory and three active wallet identities per EOA across all supported networks.

Each EOA may have at most **3 active derived smart wallet identities at a time**, shared across all supported networks. Creating a wallet registers one identity available on every compatible supported network. Hiding it hides that identity everywhere and releases one global slot. Archived identities remain recoverable. There is no lifetime limit of three wallets and no restriction to salts `0`, `1`, and `2`.

Wallet identity, visibility, quota, labels, and salt allocation belong to the owner EOA. Deployment, balances, transaction nonces, permissions, budgets, and execution belong to a network. The shared inventory includes both supported mainnets and testnets; changing the selected network never creates another quota bucket.

## Why MA v2 supports a shared identity

The current implementation in `core/chainio/aa/ma_v2_account.go` calls `getAddressSemiModular(owner, salt)` at the canonical factory `0x00000000000017c61b5bEe81050EC8eFc9c6fecd`. It uses the SemiModularAccountBytecode variant. Alchemy publishes that factory and implementation at the same addresses across its supported networks, and documents deterministic MA v2 addresses from the owner and salt. See [Alchemy deployed contracts](https://www.alchemy.com/docs/wallets/smart-contracts/deployed-addresses), [MA v2 address calculation](https://www.alchemy.com/docs/wallets/smart-contracts/modular-account-v2/getting-started), and the [AccountFactory implementation](https://github.com/alchemyplatform/modular-account/blob/develop/src/factory/AccountFactory.sol).

For the same owner, salt, factory, account variant, and implementation profile, the predicted address is the same across compatible networks. The local `TestMAv2DerivationVectors` records matching factory outputs on Base and Sepolia; it checks encoded inputs, not live parity across every configured network.

"Available on all supported networks" means the identity can be resolved and used there. It does not mean the account contract is already deployed on every network. Each network deploys the account independently on first use and has independent funds and permissions. The current creation call itself registers a counterfactual address and moves no funds.

Pin the factory, account variant, implementation, and derivation version as a deployment profile. A network is compatible only after its deployments and derivation outputs have been verified against that profile. A matching factory address alone is insufficient if its implementation or initialization differs. A failing or mismatched network must be unavailable for execution without changing an existing identity's address or global visibility.

## Expected behavior

Here, active means a registered, non-hidden derived identity that is neither retired nor stale. It does not mean deployed, funded, holding a grant, or running an enabled workflow. Counterfactual identities consume one slot regardless of the number of networks on which they have been used. A designated `eoa_7702` account remains a separate, network-specific account type outside the derived-wallet quota.

| Action | Global active salts afterward | Global hidden salts afterward | Result |
| --- | --- | --- | --- |
| Start with three wallets | `0, 1, 2` | none | All slots occupied |
| Create salt `3` | `0, 1, 2` | none | Refused at the active limit |
| Hide salt `0` | `1, 2` | `0` | One slot released |
| Create salt `3` | `1, 2, 3` | `0` | Succeeds |
| Restore salt `0` | `1, 2, 3` | `0` | Refused until another wallet is hidden |
| Hide salt `2`, then restore `0` | `0, 1, 3` | `2` | Both operations succeed |

Creating an explicitly requested unused salt `4` after hiding `0` is equally valid: active identities become `1, 2, 4` across every compatible supported network. There is no requirement to create salt `3` first. An automatic Add normally chooses the next unused salt from global history.

Salt `0` follows the same global archive and restore rules as every other salt. Repeated ensure calls, including from another network, return the existing identity and visibility at capacity. Materializing that identity on another network never acquires another active slot. Reading or withdrawing from an existing identity does not acquire a slot.

Archiving continues to control inventory visibility and slot accounting. It preserves funds, ownership records, permissions, and existing workflows. Stopping automation or revoking a grant remains an explicit operation; UI copy must not imply that hiding performs either action.

## Current behavior and gaps

The review covered AVS `staging` at `f3c8261d` and the requested `../studio` checkout at `cb725d3f5` on `mock/2106-note-placement`. References below are paths within their named repository.

| Area | Current behavior | Required change |
| --- | --- | --- |
| AVS `core/taskengine/schema.go`, `engine.go` | Wallet records, salt indexes, counts, and hidden flags are scoped to a chain. Counting includes hidden and stale records, excluding retired wallets and `eoa_7702`. | Introduce an owner inventory and count distinct active global identities. Keep chain records for runtime state. |
| AVS `engine.go`, `ListWallets` and `storeDefaultWalletForListWallets` | A list can store and return a missing salt `0` even when the quota is full. | Bootstrap a default once per EOA through global admission; another network must not recreate or unhide it. |
| AVS `engine.go`, `SetWallet`; `engine_eoa_7702.go`, `SetStoredWalletHidden`; `schema.go`, `SetWalletHiddenStatus` | Visibility changes have no restore admission check. | Resolve the global identity and update one authoritative hidden flag. Restoring acquires a global slot. |
| AVS `aggregator/rest/handlers_wallets.go`, `UpdateWallet` | Initially looks up an address on the authenticated chain, but derived wallets are then updated through `SetWallet`, which uses the gateway default chain and re-derives the address. | Resolve an owned derived identity by address and update global visibility without deriving against a network. Keep designated-EOA updates network-specific. |
| Studio `prisma/schema.prisma`, `SmartWallet` and `EOAWallet` | Smart wallet rows and ownership links require `chainId`; smart wallets are unique by `(address, chainId)`. | Mirror one identity owned by an EOA address, separately from optional per-network runtime observations and chain-specific auth. |
| Studio `app/lib/wallet/smartWalletLimits.ts`, `walletsPageLayout.ts`, `app/wallets/WalletsEoaGroup.tsx` | The helper accepts an `activeCount`, but the group passes the total list length and separately hides Add at three total rows. Comments and tests explicitly count archived wallets. | Use authoritative global active usage for both creation and restoration. |
| Studio `WalletsEoaGroup.tsx` | `isArchivable={wallet.salt !== "0"}` prevents the requested hide operation. Restore has no capacity hint. | Allow salt `0` to be archived; explain and enforce restore capacity. |
| Studio `app/controllers/walletController.ts`, `addWallet` | Uses one chain's list length as the next salt. | Allocate the next salt in AVS from durable global history under the owner lock. |
| Studio `app/hooks/useLinkedEoaSmartWallets.ts`, `mergeNestedSmartWallets` | Fetches the EOA's stored `chainIds`, merges by address, and prefers the header chain, including its hidden flag. | Fetch one owner inventory independent of signed networks. A network switch changes runtime reads only. |
| Studio `WalletsEoaGroup.tsx` | Creation refreshes both the page and shared smart-wallet context; archive and restore reload only the page. | Refresh both inventories after successful visibility changes. |
| Studio `resolveRunnerInPlace` and `app/lib/mcp/ensurePreviewSmartWallet.ts` | The runner uses `wallets[0]` without excluding hidden rows. The MCP helper prefers salt `0` even if hidden. | Separate automatic runner selection from explicit ensure-by-salt behavior. |
| AVS `core/taskengine/executor.go`, `validateDerivedWallet` | Uses `MaxWalletsPerOwner` as a salt scan bound. | Treat active quota and salt discovery as separate concepts; verify higher salts through stored records. |
| SDK `packages/sdk-js/src/v4/resources/wallets.ts` | Its list documentation says hidden wallets are excluded, although the engine returns them. | Expose global identity and usage semantics, retain explicit-salt ensure, and add allocation support. |

The production infrastructure configuration in `../avs-infra/railway/configs/gateway-railway.yaml` repeats `max_wallets_per_owner: 3` in its chain blocks. Local configurations include higher values and the Go fallback is `5`. Replace the quota's network-dependent source with one explicit gateway-wide owner-inventory setting; preserve separately configurable test limits and a bounded ownership-discovery fallback.

## Global identity and network runtime records

Introduce an AVS identity record keyed by `(owner, deploymentProfile, salt)`, with an immutable `walletId`, predicted address, factory, global hidden flag, lifecycle flags, and creation order. The deployment profile pins the current semi-modular derivation inputs. Neither identity keys nor address and salt indexes contain `chainId`. Count all active derived profiles together for the owner, so selecting another factory or profile cannot create another quota bucket.

Maintain one owner metadata record containing the inventory revision, next automatic salt, and any allocation idempotency references. The active count can be computed from the canonical owner inventory; if cached, update and verify it atomically. Keep the limit in gateway configuration rather than copying it into each chain row.

Retain the existing `w:<chainID>:<owner>:<address>` records as network runtime or compatibility records linked to `walletId`. They no longer own derived-wallet visibility or quota. Resolve all inventory responses and compatibility hidden fields from the global record. Do not fan out archive mutations to every chain row and hope the writes stay synchronized.

Network records can be materialized on first use, without consuming a slot, after verifying the chain supports the pinned deployment profile. Global creation and hiding must not wait for every chain RPC or worker, and adding a supported network must not require recreating all wallet identities. On first execution on an unused network, resolve ownership and salt from the identity, persist the local runtime record needed by execution and sponsorship, then use that network's deployment and permission flow.

Audit direct consumers of `GetWallet`, `StoredWallet`, salt indexes, `ValidWalletOwner`, session-policy wallet lookup, owner execution, withdrawal, VM runner lookup, and sponsorship registration, including `aggregator/rpc_server.go`. They must resolve a valid global identity even when no historical row exists for the requested network. Existing policy and transaction records remain chain-specific. Retired and stale legacy addresses remain available through their recovery paths and must not be substituted with a newly derived address.

## Global admission and persistence

Count each registered, non-hidden, non-retired, non-stale derived identity once within the owner inventory. Normalize addresses and fail admission on storage or decoding errors. A failed prefix read must not authorize another wallet.

| Transition | Admission rule |
| --- | --- |
| Missing to new visible identity | Require global active count below the owner limit |
| Hidden to visible identity | Require global active count below the owner limit |
| Visible to hidden identity | Always permit for an owned existing identity; hide globally |
| Ensure an existing visible or hidden identity from any network | Return it unchanged regardless of quota |
| First use of an existing identity on another network | Verify compatibility and materialize runtime state; consume no slot |
| Reapply current visibility | Succeed without consuming a slot |
| Legacy retired or stale record | Preserve its system flags and exclusion from active inventory |
| Designated EOA visibility change | Preserve its separate network-specific behavior outside this quota |

Serialize creation, restore, hide, default bootstrap, and automatic salt allocation using an **owner-only** lock shared across networks. Follow the existing bounded sharded-lock pattern, but do not include chain, factory, or runner in the lock identity. Resolve derivation outside the critical section where possible, then re-read identity, usage, and revision under the owner lock before committing.

Commit identity, salt/address indexes, owner revision, automatic-salt state, and allocation replay result together using an atomic write that propagates commit errors. The current `BatchWrite` alone does not establish atomic count-and-create behavior. The lock protects the current single-writer Badger store; independent gateway writers would need a shared transactional registry.

Pin existing identities to their deployment profile. A factory change or a mismatching network derivation must not silently replace their addresses. A future profile migration requires a separate explicit migration; it must preserve archived state and recovery records.

Route gRPC `SetWallet`, REST PATCH, and derived-wallet calls through `SetStoredWalletHidden` to the same global admission service. Use the supplied owned address or known profile/salt index to resolve identity. JWT network audience and gateway default network do not determine the scope of a derived-wallet archive operation.

## Default wallet and listing

List the owner's global derived identities, including hidden ones, regardless of selected network or stored EOA sign-in chains. Keep retired and stale legacy records out of normal inventory. Optional network context can add runtime information and a designated-EOA row; it cannot filter derived identity membership or change global hidden flags.

Preserve the current first-use default bootstrap through global admission only when the owner has no derived-wallet history. Once an inventory exists, list calls do not invent a missing salt `0`; an explicit ensure may create it if capacity allows. A stored hidden `0` is always returned as hidden. Listing or previewing a newly supported network must not bootstrap another default, restore an archived one, or consume another slot.

An owner who hides every wallet keeps an archived-only inventory on every network. Studio offers creation or explicit restoration. Inventory revisions let clients discard older snapshots that arrive after a visibility change; stale Studio mirrors must never restore global flags.

## Salt allocation and ownership

Keep `POST /wallets` as an idempotent ensure for an explicit salt. Existing `chainId` input remains accepted as an optional derivation/runtime hint, with no effect on identity or quota scope. Explicit unused salts such as `4` are allowed without allocating all lower numbers.

Add an allocation operation, `POST /wallets:allocate`, exposed as `wallets.createNext`, for Studio's Add button. AVS selects the next salt from durable global history, rather than any network's list length. Require an idempotency key for allocation; commit its replay result with the identity so retries cannot create additional wallets. Two different allocation requests may create distinct salts only when both fit under the global limit.

Seed the monotonic next-salt value above all known salts during migration. Explicit ensures of a higher salt advance it. Hidden identities and retained history never free a salt for reuse. For history `0,1,2`, Add chooses `3`; for `0,2,8`, it chooses `9`. Use arbitrary-precision integers, validate uint256 bounds, and handle exhaustion explicitly. Numeric ordering must put `2` before `10` in REST and Studio; display ordinals remain separate from salts.

Salts above `2` must pass ownership, workflow execution, policy, sponsorship, and withdrawal on networks without prior runtime rows. Resolve the known owner and salt from the global registry. Do not expand a brute-force scan to match lifetime history or use the active limit as a salt bound. Keep any legacy derivation fallback independently bounded; explicit registration supplies the exact salt outside that fallback.

## Studio wallet usage

Mirror global identity separately from network observations in Prisma. Introduce an owner-by-address relation or equivalent canonical owner key independent of `EOAWallet.chainId`; keep existing account ownership/claim checks when joining the signed-in user's linked EOAs. A smart wallet's stable identity must not require an auth row or a prior connection on every network. Store global visibility and inventory revision on this mirror, with AVS as the source of truth. Keep chain-specific auth, runtime observations, and recovery data separate.

Make `useLinkedEoaSmartWallets` and `SmartWalletContext` consume one global inventory per linked EOA. Remove the loop over stored `chainIds` for identity discovery and stop resolving visibility by the header network's preferred row. Key the identity cache by owner and wallet ID, and runtime caches by wallet ID/address plus chain. Preserve `kind` through DTOs and types to distinguish designated EOAs. Do not prune global identities because a network list or worker omitted them.

Show one card per identity with global usage such as **2 of 3 active** and **Show archived (1)**. Remove total-count Add gates and the `salt !== "0"` archive restriction. Add calls the global allocator. Restore uses global capacity. Network selection changes balances, deployment status, permission rows, Deposit/Withdraw, and execution routing; card membership, labels, hidden flags, usage, and the next salt remain stable.

Use the existing chain-aware balance reader, `BalanceContext.getBalanceForAddress(address, chainId)`, in every network-dependent wallet surface. The context also retains a legacy address-only map and bulk reader; those must not supply balances across network switches. Keep `(chainId, address)` cache identity for balances and explicit network keys for permissions and budgets, so delayed reads cannot display another network's funds or quotes.

Keep Add and Restore disabled while global inventory is unknown or refreshing, or a mutation is pending. Preserve owner connection and authentication requirements without introducing a signature for every supported network. Global visibility changes can use a valid JWT proving that owner; remove Studio's current exact-chain sign-in requirement for derived-wallet hide/restore, while retaining chain-specific requirements for runtime operations. At capacity explain: **You can have up to 3 active smart wallets across all supported networks. Archive one to create or restore another.** Archive copy should say it hides the wallet on all networks, frees a slot, and restoration requires an available slot.

After successful creation, archive, or restore, invalidate global page, context, chat, and picker inventories for that owner. Refresh shared usage and preserve the newest revision. A quota refusal from another tab leaves visibility unchanged and triggers an inventory refresh; it is not an authentication retry.

`SmartWalletContext`, `resolveOwnerSmartWallets`, and chat inventory queries already filter hidden rows in their main inventory paths; migrate their inputs to global identities. `resolveRunnerInPlace` must select an eligible active global runner and resolve its runtime state on the workflow's explicit network. If every identity is archived, offer setup or restore. Preserve existing workflows' explicitly selected runners when an identity is hidden.

The MCP `create_smart_wallet` tool explicitly ensures salt `0`. It may return an existing globally hidden `0`, with visibility and a restore handoff, but must not reactivate it or silently substitute another salt. Its `chainId` describes intended use, not a new inventory. If `0` is missing at global capacity, return the admission refusal. Generic runner discovery may choose another active identity.

Keep labels consistent across page, chat, and network switches, based on global creation/salt order. Bind actions to wallet ID/address and retained derivation metadata. Never parse the displayed "Smart Wallet 3" into salt `2`.

## API and configuration

Preserve explicit-salt create and address-based PATCH inputs, while deliberately changing derived-wallet inventory and visibility semantics to global scope. Document this behavior change for SDK and legacy gRPC callers. Existing chain arguments remain network-use hints and do not partition identity operations. Keep designated-EOA operations explicitly chain-scoped.

Extend the list envelope additively with global usage, for example `{ data, usage: { scope: "owner", activeCount, activeLimit, revision } }`, and include stable identity/profile metadata on derived wallets. Hidden identities remain in `data`; usage excludes designated EOAs and archived identities. Studio uses the returned limit rather than treating its display constant as the authority. Add the allocation endpoint and SDK method, OpenAPI-generated types, and idempotency coverage.

Keep gRPC `ResourceExhausted` and HTTP `429` for quota refusals, with a stable `WALLETS_LIMIT_REACHED` code for create, allocate, and restore. The SDK already documents that code; the generic gRPC mapper alone does not establish it. Preserve it through Studio's error and action-result layers.

Configure one gateway-wide `smart_wallet_inventory.max_active_wallets_per_owner: 3`. Stop deriving this quota from per-network `MaxWalletsPerOwner`; remove its use as an ownership salt-scan bound. During configuration migration, consistent legacy limits may be mapped to the global value, but conflicting values require an explicit owner-inventory setting. Do not silently select the default network's quota. Test configurations can set an explicit different limit.

## Migration and rollout

This scope change requires an AVS inventory migration and a Studio mirror migration. Inventory history must be built from all stored networks, not only a user's current sign-in networks.

1. Group valid MA v2 records by owner, pinned profile, salt, and matching predicted address. Deduplicate identical records across networks into one identity and retain every network record and all operational references. Validate group membership rather than merging solely by salt or address.
2. Resolve legacy visibility deterministically: a migrated identity is active if **any** valid member was visible; it is hidden if all valid members were hidden. Report conflicting flags. This preserves wallets previously visible somewhere; after cutover, the first explicit hide applies globally. Exclude retired/stale records from this visibility vote and preserve them for recovery.
3. If one profile/salt tuple has conflicting addresses, preserve the records and report the conflict for resolution. Do not automatically choose an address or rewrite a workflow runner. Unresolved canonical conflicts must block activation for that owner until repaired, while retaining known recovery paths.
4. Seed allocation above all retained relevant salt history and write migration markers/revisions atomically per owner. Reruns must not overwrite newer global hidden flags from old chain rows. Import the AVS global visibility and usage into Studio; never reconstruct them independently from a stale Prisma mirror.
5. Audit the global union before rollout. Some owners may already have more than three distinct visible identities across networks. Preserve them, block new activations while at or above the limit, and allow hides and recovery. Do not auto-hide funded wallets or delete historical rows to force compliance.

Implement and release in this order:

1. **Deployment profile and registry:** verify the same semi-modular derivation on every supported network; add identity/index/owner metadata storage and global configuration, the migration, atomic owner admission, and regression coverage.
2. **AVS consumers and APIs:** route all identity operations through the registry, add allocation/global usage, and update runtime ownership, session-policy, sponsorship, VM, and withdrawal paths to support networks with no prior local row. Fence or stop old writers during migration and cutover so chain-only writes cannot bypass the registry.
3. **SDK and infrastructure configuration:** regenerate types, expose global inventory and allocation, document the new scope, and set the owner-inventory limit once in `avs-infra`.
4. **Studio:** migrate identity ownership/persistence, global inventory and cache keys, runtime network reads, archive/restore/Add controls, shared refresh, runner/MCP handling, labels, and copy. Update `PLAN_SMART_WALLET_AGENT_ACCOUNT.md` alongside implementation.
5. **Integration verification:** run the same owner through hide/create/restore from different network contexts, then use a new identity on a supported network with no prior row. Verify same address and global visibility while confirming independent balances, deployments, grants, budgets, and nonces.

Backend registry enforcement must precede the Studio controls. Rolling back to chain-only inventory writers after global mutations requires an explicit data reconciliation, not merely a binary rollback. Keep implementation and validation checklists open until completed.

## Acceptance checks

- [ ] Verify factory/implementation profile and matching predicted addresses for representative owner/salt vectors, including `0` and `4`, on every supported network using read-only calls. An unavailable or mismatched network cannot change a global address.
- [ ] With global active salts `0,1,2`, creating `3` or `4` from any network fails without registering another identity.
- [ ] Hide `0` from Base; ensure unused `4` from Ethereum; verify active `1,2,4` and hidden `0` in AVS and Studio on every network. No on-chain deployment is needed for the inventory change.
- [ ] Restore `0` from a third network at capacity fails; hide `2`, then restore `0`; all networks show the same result.
- [ ] Repeated ensure and first runtime materialization on another network preserve global visibility and consume no additional slot. An existing hidden wallet remains readable and recoverable at capacity.
- [ ] Hide all wallets; list, preview, header changes, and MCP ensure preserve archived-only inventory. A default is bootstrapped only for a genuinely new owner, never per network.
- [ ] Migration deduplicates identical multi-network records, applies the documented mixed-visibility rule, preserves all legacy recovery/runtime references, reports canonical conflicts, and safely resumes without overwriting newer global changes.
- [ ] Retired/stale records and designated EOAs do not consume derived-wallet slots. One active identity used on eleven networks consumes one slot.
- [ ] Cross-network races between different creates, restores, allocation, and first-use bootstrap with one free slot permit only one new activation. Replaying an allocation idempotency key returns the same identity.
- [ ] Count/decode/write failures leave identity, indexes, revision, salt state, and replay records consistent without granting a slot.
- [ ] Add chooses `3` after hiding `0`, `9` for history `0,2,8`, and handles salts above JS safe-integer range and uint256 exhaustion correctly. Explicit creation of `4` does not require `3`.
- [ ] A linked EOA discovers all global identities even if it has signed in on only one network. Header changes never change usage, labels, hidden flags, or identity membership.
- [ ] Network switches and delayed responses cannot leak balances, permission state, budgets, deployment state, or nonces between networks or restore an older visibility revision.
- [ ] Deposit, workflow pickers, chat, runner resolution, and shared context reflect global hide/restore. MCP exposes an archived default and does not unhide it implicitly.
- [ ] Use registered salt `4` and a higher non-contiguous salt on a network without a prior local row: ownership, policy setup, sponsorship, execution, and withdrawal work through exact registry metadata without expanding a salt scan.
- [ ] Existing inventories above the global limit remain readable, hideable, and recoverable; new activations are refused until capacity is available. Network selection cannot bypass this rule.

Extend AVS wallet, list, REST handler, ownership, and sponsorship tests; add focused registry migration, atomic-write failure, and cross-network concurrency tests. Update SDK allocation/usage contracts and Studio Prisma/controller/hook/runner/MCP coverage. Replace the page contract that asserts archived wallets count. Add a behavioral `/wallets` test for global hide, explicit salt `4` creation, network switch, blocked restore, and successful restore, rather than relying only on source-text assertions.
