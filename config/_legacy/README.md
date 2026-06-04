# config/_legacy/ — pre-restructure snapshot

Verbatim copy of every tracked `config/*.yaml` as of commit `eae37b9`, taken before any planned restructure of the config layout. Kept in the repo as a safety net so:

1. We can diff "post-restructure" configs against the snapshot at any time to confirm semantic equivalence.
2. If a Railway service's `--config` path is ever broken by the restructure, the original file is still here to recover from.

## Why a snapshot, not a `git tag`?

A tag is fine for recovery via `git show <tag>:config/file.yaml`. The snapshot folder is for *humans*: it lets you open the original alongside the new layout in your editor without git gymnastics.

## When to delete this folder

After:
- The env-driven worker.yaml consolidation has landed,
- Every Railway worker/operator/gateway service has been updated to point at the new config path,
- Each service has redeployed cleanly and stayed green for at least one production day.

Delete in a single `chore: drop config/_legacy snapshot` commit referencing the restructure PR that justifies it.
