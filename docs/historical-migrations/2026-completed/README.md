# Historical migrations — 2026 completed

Migrations that have been successfully applied in production and archived out
of the active `migrations.Migrations` list to reduce startup overhead. The Go
files here carry a `//go:build historical_migrations` tag, so they are excluded
from normal builds; they are kept for reference and audit only.

Migration completion is recorded per-database under `migration:<name>` keys, so
these never re-run on an environment that already applied them.

| Migration | Applied | Records | What it did |
|---|---|---|---|
| `20260618-delete-invalid-failed-tasks` | v3.10.3 (2026-06-21) | 33 | Deleted workflow rows that were `Failed` **and** still failed `ValidateWithError` — the legacy cohort the boot-time scan retired (invalid node names like "send eth", trigger configs orphaned by an older proto migration). Removed the Failed status row, the user index, and any stale Enabled orphan. |
| `20260621-delete-auto-disabled-invalid-tasks` | v3.10.3 (2026-06-21) | 12 | Deleted workflow rows the executor flipped to `Disabled` after the consecutive-permanent-validation-failure threshold — overwhelmingly "task smart wallet address does not belong to owner" (Sentry EIGENLAYER-AVS-1X..28). These passed `ValidateWithError`, so the Failed-cohort migration above deliberately left them. Matched on persisted `LastValidationError` + `ConsecutiveValidationFailures >= 10`; removed the Disabled status row, the user index, and any Enabled orphan. |

Both took a full DB backup first (handled by the migrator) and are idempotent.

## Running an archived migration (rare)

If you ever need to re-run one against a fresh database that legitimately
contains the legacy data (e.g. a restore from a pre-migration backup), build
with the tag and re-register it temporarily:

```bash
go build -tags historical_migrations ./...
```
