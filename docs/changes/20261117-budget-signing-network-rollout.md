# Budget signing by network

Budget signing now has an explicit `wallet_budget_signing_chains` deployment setting, in addition to the preparation and signing switches. An omitted or empty list preserves the existing Sepolia-only behavior. Ethereum (1) and Base (8453) require explicit opt-in; listing another chain cannot enable an unqualified deployment. Disabling preparation or signing still refuses every submission/cancel. Observation and reconciliation of submitted operations remain available.

This prepares mainnet support without enabling it merely by upgrading the binary. The canonical BGB catalog correction from #921 is included in this branch's staging base, satisfying #922's code prerequisite. The release still needs to ship before production Ethereum activation.

## Approval resets

Automatic nonzero resets are restricted to individually qualified deployments:

| Network | Qualified tokens |
|---|---|
| Sepolia | USDC (existing live bundler proof) |
| Ethereum | native USDC, USDT, WETH, canonical BGB |
| Base | native USDC, WETH |

Other nonzero approvals still require owner-directed revocation before preparation. Failed RPC calls and empty or malformed allowance responses still fail closed. A successful transaction alone does not establish that a reset succeeded; the existing pinned post-inclusion reads remain authoritative.

`TestBudgetAtomicResetAndInstall_MainnetForks` exercises every mainnet entry above. Go's production reset/install encoders supply the batch bytes. Disposable forks use the pinned factory/implementation, real token contracts, modules and EntryPoint to prove reset, rollback after a later reset/installation failure, owner-signed installation, session-signed execution and metered ERC-20 spending. USDT's no-return `approve` is included. No transaction reaches mainnet. The CI workflow runs both network proofs for relevant changes using only a read RPC credential and public fork-only keys.

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
2. Complete bounded Ethereum/Base owner-signing and bundler canaries on fresh test identities, plus the separate discovery-completeness canary. Mainnet funding/signatures need a designated test owner.
3. Complete #923's sponsored-execution check using a dedicated test policy. The local fixture intentionally disables the existing policy whose webhook points at production; the self-funded probe does not close that check.
4. Opt in production Ethereum/Base with `wallet_budget_signing_chains: [11155111, 1, 8453]` after those proofs pass.
5. Coordinate Studio #2134/#2135: update its shared availability rule and advisor/gallery consumers to match the activated networks. A gateway upgrade alone does not remove Studio's existing Sepolia-only guard.

No storage migration is required. Preparation amounts, owner review binding, nonce holds, strict allowance reads, post-inclusion reconciliation, retired-authority refusal and fee ceilings are unchanged.

## Native MAX fee quote

The live native probe exposed a preview/send difference on quiet Sepolia: the chain quote was 32 wei per gas while Alchemy required a 100,000,000 wei priority-fee floor. The sender already raised its fee to that floor; the simulation did not. Native MAX previews now use the same fee selection as the sender, including the bundler floor. The sender's selection/fallback behavior and diagnostic logs are preserved. A regression drives the VM preview with a bundler floor above the chain tip and checks the amount and fee reserve.
