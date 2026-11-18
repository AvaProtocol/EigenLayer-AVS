# Budget signing by network

Budget signing now has an explicit `wallet_budget_signing_chains` deployment setting, in addition to the preparation and signing switches. An omitted or empty list preserves the existing Sepolia-only behavior. Ethereum (1) and Base (8453) require explicit opt-in; listing another chain cannot enable an unqualified deployment. Disabling preparation or signing, or removing a chain from the list, refuses new budget sends, including owner cancellation. Observation and reconciliation of submitted operations remain available.

Signing-gate refusals still use `BUDGET_HANDOFF_REQUIRED`, but their detail is now "Budget signing is not enabled on this network." for both disabled global switches and networks outside the effective signing list. Clients should use the code for availability handling. Startup warnings identify duplicate and unqualified `wallet_budget_signing_chains` entries; those entries do not enable additional networks.

Unsigned preparation checks the preparation switch without applying the signing chain list. It can return a review on an unlisted chain, but submission will be refused. The unsigned record has a TTL and creates no wallet nonce hold or installed policy. A durable hold is committed only during submission, before relay. Clients must check the activated networks before offering budget review.

The public `GET /networks` reports them: one row per configured chain with `walletBudgetSigning`, computed by the same `BudgetSigningEnabledForChain` rule submission enforces. Studio reads it (cached for a minute) for every availability decision, so activating or deactivating a chain is this config change alone; there is no second list to keep in step.

This prepares mainnet support without enabling it merely by upgrading the binary. The canonical BGB catalog correction from #921 shipped with these controls in v6.4.0, satisfying #922's prerequisite for production Ethereum activation.

## Approval resets

Automatic nonzero resets are restricted to individually qualified deployments:

| Network | Qualified tokens |
|---|---|
| Sepolia | USDC (existing live bundler proof) |
| Ethereum | the token catalog, 128 of 130: every token except BNB and GT |
| Base | the token catalog, all 37 |

The mainnet lists started at USDC, USDT, WETH and canonical BGB on Ethereum and native USDC and WETH on Base. A per-token sweep then ran the same proof over each catalog token on a pinned fork (Ethereum block 26437089, Base block 58780333); `budget_reset_deployments.go` lists every token that passed, by symbol. BNB (`0xB8c7…bDD52`) and GT (`0xE667…4759`) revert on `approve(spender, 0)`, so no batch can reset them, and an approval of either still needs another way out. A token added to the catalog is added here only with the proof passing for it; the CI proof now also runs when the catalog changes.

Other nonzero approvals still require owner-directed revocation before preparation (`BUDGET_RESET_UNSUPPORTED_TOKEN`, naming the token and spender). Studio offers that revoke from Review. Failed RPC calls and empty or malformed allowance responses still fail closed. A successful transaction alone does not establish that a reset succeeded; the existing pinned post-inclusion reads remain authoritative.

`TestBudgetAtomicResetAndInstall_MainnetForks` exercises every mainnet entry above, in batches of 10 per forge run. The cases travel as one hex environment string, and Linux caps a single string at 128 KiB; a token's cases are about 9.5 KB, and the test fails with a clear message if a batch outgrows the cap. Go's production reset/install encoders supply the batch bytes. Disposable forks use the pinned factory/implementation, real token contracts, modules and EntryPoint to prove resets, owner-signed installation, session-signed execution and metered ERC-20 spending. USDT's no-return `approve` is included. The Solidity harness constructs the signatures, nonce keys and session wrapper with public fixture keys; it does not exercise Go's `FrameOwnerSignature` or session signing path. The separate live Sepolia handoffs provide that Go signing and bundler-relay evidence.

The rollback cases call the wallet directly as its owner on the fork. One resets a real token before a later fixture-token reset reverts. The other resets a real token before truncated installation calldata fails ABI decoding. Both check that the allowance remains unchanged and no signer was installed. These establish batch atomicity; they do not qualify a module-level installation rejection such as a duplicate entity or invalid hook configuration. No transaction reaches mainnet. The CI workflow runs both network proofs for relevant changes using only a read RPC credential and public fork-only keys.

```sh
WALLET_BUDGET_MAINNET_FORK=1 go test -tags integration ./core/taskengine \
  -run '^TestBudgetAtomicResetAndInstall_MainnetForks$' -count=1 -v
```

RPCs come from `ETHEREUM_RPC_URL` / `BASE_RPC_URL`, or the existing `ALCHEMY_API_KEY`. Each proof pins and logs its block number. This proves contract behavior; it does not qualify live bundler admission or gas sponsorship.

## Live probes

The existing Sepolia owner-handoff probe remains available. A new opt-in variant allocates through Base Sepolia, checks real factory derivation on both networks, and uses that identity for a signed Sepolia budget/session operation and owner recovery. It uses an isolated database, fresh high salts, the existing test owner, a limit of three and self-funded Sepolia gas. It verifies capacity, hide/create/restore, identity/visibility, and per-network policy, deployment and balance isolation. It explicitly avoids the SDK fixture's deterministic address/nonce readers.

```sh
WALLET_INVENTORY_LIVE=1 go test -tags integration ./core/taskengine \
  -run '^TestWalletInventoryBudgetHandoff_CrossNetwork$' -count=1 -v
NATIVE_MAX_LIVE=1 go test -tags integration ./core/taskengine \
  -run '^TestSelfFundedNativeMax_Sepolia$' -count=1 -v
```

The native MAX probe uses actual USDC sends followed by VM `RunNodeWithInputs` simulation/execution, then owner recovery. Its short-limit cases use a real owner call to the deployed native module to set the exact remaining limit after the token step; that is deliberate fixture setup. Each wallet receives at most 0.02 Sepolia ETH and 100 test-USDC base units; the deposit case additionally funds 0.001 Sepolia ETH to its owner-controlled EntryPoint deposit. It checks reported value against the recipient's balance, the remaining wallet balance, metadata and the simulation estimate. EntryPoint refunds remain explicitly reported and owner-controlled.

Addresses, salts, receipts and recovery details belong in private avs-infra incident evidence, not this public repository. These probes are opt-in and never run fund-moving operations in regular CI.

For #923's separate sponsorship check, run the cross-network probe with an explicitly provided `WALLET_INVENTORY_SPONSOR_POLICY_ID` for a dedicated Sepolia test policy (and `WALLET_INVENTORY_SPONSOR_WEBHOOK_DATA` if that policy requires it). Only the zero-value session approval is sponsored; budget installation remains self-funded. The probe requires a real nonzero paymaster and unchanged native wallet balance/permission for that session operation. A policy refusal never falls back to paying from the wallet. Without that variable, the probe is explicitly self-funded and does not qualify sponsorship.

## Activation sequence

1. Merge and release the gateway candidate containing #921 and these controls, initially retaining Sepolia-only configuration.
2. Complete bounded Ethereum/Base owner-signing and bundler canaries on fresh test identities, plus the separate discovery-completeness canary. Compare the prepared gas/fee fields and maximum fee with the operation actually signed and relayed, and confirm `wallet_budget_max_fee_wei` admits the intended bounded operation. Budget preparation already uses the bundler-aware quote; the ceiling applies to `MaxOwnerGasCost` of that exact operation. Mainnet funding/signatures need a designated test owner.
3. Complete #923's sponsored-execution check using a dedicated test policy. The local fixture intentionally disables the existing policy whose webhook points at production; the self-funded probe does not close that check.
4. Opt in production Ethereum/Base with `wallet_budget_signing_chains: [11155111, 1, 8453]` after those proofs pass.
5. Studio follows on its own: it reads `GET /networks`, within a minute of the change. Confirm that Configure, Review and chat search stop showing the unavailable notice on the activated chains (Studio #2134/#2135).

No storage migration is required. Preparation amounts, owner review binding, nonce holds, strict allowance reads, post-inclusion reconciliation, retired-authority refusal and fee ceilings are unchanged.

## Deactivation and outstanding changes

Removing a chain stops new budget sends through the gateway. It does not cancel an operation already relayed, invalidate an owner signature, remove installed authority or release a durable hold. A held wallet's affected runs remain paused until reconciliation proves settlement. `PrepareBudgetCancel` uses the same signing gate, so there is no cancellation-only mode while the chain is disabled.

1. Before a planned deactivation, record the effective signing list and each unsettled change's preparation IDs, attempts, userOp hashes and owner nonce in private incident evidence. Where possible, settle or owner-cancel those changes while signing is still enabled. An incident can require immediate deactivation before settlement.
2. Studio's availability follows `GET /networks` within a minute; nothing to change there. Configure the remaining networks explicitly: an empty list restores the Sepolia default. To disable every network, turn off `wallet_budget_signing_enabled` (or preparation), rather than relying on an empty list.
3. Keep the compatible gateway, database and chain readers available for observation/reconciliation. Retain submitted records, attempts and holds through timeouts, RPC failures, bundler rejections and restarts. Disabling a switch or expiring a preparation is not settlement evidence.
4. If a submitted change remains stuck, choose an owner recovery path based on the incident. Deliberately re-enabling a safe chain permits owner-signed replacement/cancellation, but also re-enables new submissions. If the chain must stay disabled, the owner can consume the held EntryPoint owner nonce outside Ava using a separately reviewed recovery operation. Reconciliation must verify canonical nonce/inclusion evidence and authority cleanup before releasing the hold; an unexpected authority or failed read keeps it in place. Never clear the hold directly in storage to resume runs.
5. Resume signing and Studio availability only after the incident is resolved and the activation checks above pass. Record the resulting settlement and effective chain list privately.

## Native MAX fee quote

The live native probe exposed a preview/send difference on quiet Sepolia: the chain quote was 32 wei per gas while Alchemy required a 100,000,000 wei priority-fee floor. The sender already raised its fee to that floor; the simulation did not. Native MAX previews now use the same fee selection as the sender, including the bundler floor. A regression drives the VM preview with a bundler floor above the chain tip and checks the amount and fee reserve.

Fee selection retains the existing fallback for bundlers that do not implement `rundler_maxPriorityFeePerGas` or cannot return a floor: both sends and previews use the chain tip and now emit a warning. The VM supplies its logger to the preview quote. If the entire quote is unavailable, the preview warns before using its existing simulation fallback. These remain estimates; the signed send prices and validates the actual operation again. Raw provider errors remain at Debug so warning logs do not expose credential-bearing RPC URLs.
