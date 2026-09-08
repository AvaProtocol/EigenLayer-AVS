# Backup Start/Stop mutex, fail-closed periodic dir, reserved secret names

- **Date**: 2026-09-08
- **Status**: Implemented
- **Branch**: fix/783-backup-mutex-reserved-secrets
- **Related**: #783, #782 review, `docs/changes/20260907-platform-secrets-and-backup-dir.md`

## Problem

Claude's #782 review left three non-blocking follow-ups: periodic backup Start/Stop had no mutex (now that production actually starts the ticker); a bad `backup_dir` with `backup_interval_hours > 0` logged and continued; a user secret named `moralis_api_key` (or the GoPlus names) wrote successfully then vanished from configVars.

## Decision

- Mutex around `backupEnabled` / `stop`. `backupLoop` takes the stop channel as an argument so Start after Stop recreates the channel instead of closing twice.
- `validatePeriodicBackup`: interval > 0 requires a non-empty absolute dir at config load. `StartPeriodicBackup` failure is fatal to aggregator Start (no more silent degrade to "no backups").
- `CreateSecret` / `UpdateSecret` reject platform secret names with InvalidArgument, same family as the existing `ap_` prefix rule.
