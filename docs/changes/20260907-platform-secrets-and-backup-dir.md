# Platform secrets out of configVars + honor backup_dir

- **Date**: 2026-09-07
- **Status**: Implemented
- **Branch**: fix/platform-secrets-and-backup-dir
- **Related**: restApi `options.auth` (`docs/changes/20260717-workflow-state-and-rest-auth.md`); avs-infra `RAILWAY_OPERATIONS.md` volume layout

## Problem

1. Every `macros.secrets` entry was copied into `apContext.configVars`. A restApi/customCode node could send `X-API-Key: {{apContext.configVars.moralis_api_key}}` at any URL and spend or exfiltrate the platform Moralis/GoPlus credentials. The in-repo rest-moralis tests did that live HTTP call.
2. yaml `backup_dir` was dead: `ConfigRaw` had no field, and `NewConfig` always set `BackupDir` to `{db_path}_backup`. Production yaml said `/data/gateway-backup`; the volume has `/data/gateway_backup`. App backups only ran on migration (`StartPeriodicBackup` was never called), so the newest `full-backup.db` was 2026-06-27.

## Decision

- Denylist `moralis_api_key`, `goplus_app_key`, `goplus_app_secret` from configVars. BalanceNode and GoPlus mint still read `macros.secrets` / `GetMacroSecret`. Notify tokens stay interpolable.
- First-party Moralis restApi uses `options.auth.provider: moralis`. The gateway attaches `X-API-Key` only when the URL host is `https://deep-index.moralis.io`.
- Honor yaml `backup_dir` (fallback `{db_path}_backup`). yaml `backup_interval_hours` > 0 starts the existing periodic ticker (retention 3). Production yaml (avs-infra) is `/data/gateway_backup` + 24h; that field is ignored until this binary is deployed.

## Verification

- `go test ./core/config/` — `resolveBackupDir` / `resolveBackupInterval`
- `go test ./core/taskengine/ -run 'TestPlatformSecretsOmittedFromConfigVars|TestRestRequestMoralis|TestRestMoralis|TestRestAuthProvider|TestRestGoPlusAuthInjection'`
