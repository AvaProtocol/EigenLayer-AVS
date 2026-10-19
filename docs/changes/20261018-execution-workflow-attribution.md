# Preserve each listed execution's workflow

The execution list derives each item's workflow ID from its history storage key
and carries it in the additive protobuf `Execution.workflow_id` field. Both
pagination directions use that identity. Historical rows need no backfill, and
a stale embedded workflow ID cannot override the storage key.

The flat REST list serializes each item's own workflow ID. A single-workflow
request may supply a fallback; a multi-workflow list never substitutes its first
query ID. A missing identity fails the response instead of mislabeling a run.
The malformed-payload fallback also keeps the correct workflow ID.

Existing REST clients need no schema change: `Execution.workflowId` was already
required. Studio can restore one multi-workflow request after this gateway fix is
deployed, while retaining its per-instance attribution rules.
