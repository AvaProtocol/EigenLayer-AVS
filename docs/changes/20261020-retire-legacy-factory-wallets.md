# Retire legacy-factory wallets

## Problem

Wallet records accumulate per chain and per owner (`w:<chainID>:<owner>:<address>`). Records whose factory is not the MA v2 factory are left over from before the EntryPoint v0.7 cutover. The gateway can no longer create or execute such accounts:

- `account_provider` can only be `modular_account_v2` (`config.ValidateAccountProvider`).
- The v0.6 send path was removed.

Those records still appear in `ListWallets` and still count toward `MaxWalletsPerOwner`. Accounts used heavily during testing show stale wallets, and some hit the cap without a usable wallet over it. Clients that mirror the list, such as Studio, show them too.

## Change

- `model.SmartWallet.Retired` (bool, `omitempty`): a system-set flag. Retired records stay on disk, since the on-chain account may still hold assets and workflows, policies and fee records refer to wallets by address.
- `ListWallets` skips retired records, as it already skips `StaleDerivation` ones.
- `walletCountsTowardOwnerCap` ignores retired records, so they no longer use up the cap.
- `GetWallet` refuses to create a new record under any factory other than MA v2 (`InvalidArgument`, `LegacyFactoryWalletError`). Without this an explicit legacy factory could mint a fresh, listed, cap-consuming record after the one-off migration has run. A wallet that already has a record is still returned, and its hidden flag can still change; unhiding never clears `Retired`.
- One-off startup migration `20261020-retire-legacy-factory-wallets` (`migrations.RetireLegacyFactoryWallets`):
  - Marks every derived wallet record whose factory is not MA v2, or has no recorded factory, as `Retired` and `IsHidden`, on every chain and for every owner.
  - Skips `eoa_7702` records.
  - Rewrites only the primary record; the `wsalt:` index is untouched.
  - Writes one log line per wallet (`retire-legacy-wallets: retired chain=… owner=… wallet=… factory=… salt=… workflows=…`).
  - Re-running finds nothing to change. The migrator takes a DB backup first.
- `scripts/retired_wallet_funds`: a read-only report over a copy of the gateway DB. It lists retired or to-be-retired wallets that still hold the chain's native token or any whitelisted token, for manual follow-up. Run before a release it previews what the migration will retire. Each balance read is retried once; a read that still fails, or returns too few bytes to decode, marks the wallet as an error row (with anything already found) rather than a zero, so a funded wallet is never silently dropped.

No new API, endpoint or admin surface.

## Operating it

Before releasing, preview against a production backup. `--backup-file` takes the gateway's own `full-backup.db` (on the volume under `/data/gateway_backup/<YY-MM-DD-HH-MM>/`) and loads it into a temporary directory; `--gateway-path` takes a copied DB directory instead.

```
go run ./scripts/retired_wallet_funds --backup-file /path/to/full-backup.db \
  --rpc 1="$ETH_RPC_URL" --rpc 8453="$BASE_RPC_URL" \
  --rpc 11155111="$SEPOLIA_RPC_URL" --rpc 84532="$BASE_SEPOLIA_RPC_URL"
```

After the release, the gateway log has one `retire-legacy-wallets:` line per retired wallet, and the same report over a fresh copy lists the funded ones with `STATE=retired`. Chains without an `--rpc` are reported as unchecked.

Undoing it would take another migration that clears `Retired` on chosen records.

## Verification

- `migrations/retire_legacy_factory_wallets_test.go` covers:
  - MA v2 and `eoa_7702` records kept; SimpleAccount, unknown and missing factories retired.
  - Other fields and the salt index preserved, workflow counts in the log, a second run changing nothing.
  - Key formats checked against `taskengine` helpers.
- `TestListWalletsSkipsRetiredWalletsAndCapIgnoresThem`: retired wallets are not listed and leave room under the cap. It fails without the engine change.
- `TestGetWalletRefusesNewLegacyFactoryWallets` (replaces `TestSetWalletHiddenStatusWithCustomFactory`, which created and listed a v0.6 wallet): a new legacy record is refused and nothing is stored; an existing one is still returned and unhidden, stays retired and is not listed.
- `scripts/retired_wallet_funds/main_test.go`: an empty `balanceOf` reply is an error, a zero reply is a checked zero.
- `go run scripts/compare_storage_structure.go main`: additive, backward-compatible.
- End to end on a scratch DB holding a real funded v0.6 wallet:
  - The report listed it as pending, with the testnet balances matching the chain.
  - The migration retired its three chain records.
  - The report then showed them as retired, and a second migration run retired nothing.
