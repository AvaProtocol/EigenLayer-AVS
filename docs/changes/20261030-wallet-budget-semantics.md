# Wallet budget semantics (gateway slice 3b)

This follows the budget read and retirement verification in #882. It implements
budget composition, simulation verdicts and pre-run remainder checks. It does
not enable budget installation or submission.

## Amounts and owner edits

`MergeBudgetPolicy` is a pure compositor. It takes a trusted on-chain remainder
snapshot and returns an unsigned draft; it does not persist, sign, retire or
activate a permission. The future stored owner-operation preparation must call
it again with the final read under the wallet's execution hold.

For a total A and remainder R, repeating A installs R, raising to N installs
R + (N - A), and an additive amount X installs R + X while recording A + X.
An explicit owner total takes precedence over a same-token addition. Enabled
Skills contribute actions, recipients and end dates, never run-count-sized caps.
Unchanged rows, used amounts and the original budget start time carry forward.

Native `amount` is the combined total, including earlier gas additions.
`GasHeadroom` is an explicit increment of newly priced wei, not a target reserve
and not a request to add the old allowance again. Repeating prepare against the
same base is idempotent. After activation, a later explicit gas increment grows
both the total and the remaining amount by only that increment, preserving used.
The future review must display that combined result, and an editor must prefill
the combined total returned by the budget read. With no gas increment, repeating
that total does not grow it. The two-merge regression pins those cases.

Change/Renew (`startNewTotals`) requires explicit totals for **every existing
capped token and ETH**. An omitted total is rejected rather than silently reset.
This includes zero totals. The new period's timestamp is assigned at submit and
becomes visible only after successful activation; composition leaves it unset.
Lowering a total, shortening the end, or turning an existing uncapped spending
right into a cap requires that explicit owner change.
Plain raises/extensions may omit token and ETH totals; omitted rows carry their
existing total and live remainder unchanged. This is separate from Change/Renew.

Disable (`removeActions` without new totals) only removes the named selectors.
It cannot add enabled workflows' rights, gas headroom, recipients or time. All
other rows and totals remain. Removed-token and exhausted-token limits remain
installed, including zero; a removed token gets an empty selector allowlist.
Disabling the last unmetered capability leaves a deny-all allowlist rather than
retaining that capability or discarding the budget's accounting.
An exhausted native limit also remains a zero-limit hook. Dropping these hooks
would remove limits, so the budget hook encoders support explicit zero limits
while the legacy encoders retain their positive-limit guards.

Migration preserves workflow-grant rows, real remainders, unlimited limits and
the end unless the owner explicitly starts new totals. Finite WETH totals are
not compatible with direct deposit/withdraw rights; those selectors must be
removed as part of the owner's change, with dependent workflows migrated first.

## Coverage and execution

Budget-mode simulation compares a **single run** with the actual remainder.
Unknown amounts use the chosen budget ceiling; unknown targets remain refused.
Missing token rows are returned as `missingTokens`; a recurring end beyond the
budget is returned as `needsUntil`. A Manual trigger gets no artificial window
from the workflow-grant sizing rules. Existing workflow-mode verdicts are
unchanged. A failed remainder read never falls back to stored installed limits.

Before a budget-backed contract write or ETH transfer, the engine reads the
relevant token/native remainder and checks the actual operation's calls. Both
approve and transfer charge their full amounts, summed within one atomic batch.
Native checks count value and self-funded gas; sponsored operations count value.
Exhausted and ended budgets report `BUDGET_USED_UP` (token, needed, remaining)
and `BUDGET_ENDED` (validUntil) through method results and the execution step.
An RPC failure refuses execution. Budget refusals stop later sequential methods;
unrelated future error codes do not change the legacy sequential behavior.
Input validation retains its legacy priority before the single-call permission
preflight. Nil and typed-nil RPC dependencies return a budget read refusal.

## Signing remains disabled

The REST schema accepts a `budget` input so its contract is explicit, but prepare
and stateless submit return `409 BUDGET_HANDOFF_REQUIRED`. They never return a
deferred-install digest or reinterpret budget intent as a legacy permission.
Legacy full replacement of an existing budget returns
`409 BUDGET_REPLACE_REFUSED`, including when `basePolicyId` was omitted. Both
prepare and submit check this under the wallet's authority lock. Non-budget
wallets keep their existing behavior.

The production call-site trace found only the two policy HTTP handlers calling
`PrepareSessionPolicy`/`SubmitSessionPolicy`, including the colon-action shims.
Their `add` input is the shared create-or-update path for Skill, Auto and MCP
callers; it is refused on a budget by the same guard. The low-level
`PrepareSessionGrant` builder has only the two guarded Engine call sites, and
`SubmitSessionGrant` has only the guarded Engine submit caller. No alternate
production builder or signing route bypasses those checks. The HTTP regression
covers a full request with no base, an additive request, and an old signed submit
after the wallet is marked as a budget, including an expired budget.

Slices 3c/3d must add stored priced preparations, signature-only submission,
approval discovery and atomic resets, durable holds and reconciliation, owner
replacement/cancel, and activation/retirement reporting before this becomes
usable. No feature flag in this slice enables that incomplete path.

## Verification

Offline tests cover additive arithmetic, complete owner edits, migration,
unlimited limits, capability removal, exhausted zero limits, scope/expiry,
one-run simulation, fail-closed reads and REST refusal. Single-node API tests
exercise step finalization for budget refusals before simulation or broadcast.
Live budget installation and signing are outside this slice.

The full offline `-race -short` run passed for `core/taskengine`, REST (including
mapping and middleware), `core/chainio/aa` and `pkg/erc4337/preset`. The final
metadata and Disable refinements also passed targeted race tests. `make build`,
`go vet` for those packages, formatting and the storage comparison against
`origin/staging` passed. No server was restarted and no transaction was signed
or broadcast.
