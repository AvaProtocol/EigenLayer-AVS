# Native recipient refusals name the recipient and say what kind they are

Date: 2026-10-27
Status: PR against `staging`
Branch: `feat/native-recipient-refusal-fields`
Related: gateway #872 and #873, [Studio #1988](https://github.com/AvaProtocol/studio/issues/1988),
[Studio #2007](https://github.com/AvaProtocol/studio/pull/2007)

## Problem

Since #873, prepare and submit check every native recipient on the merged
grant. That includes other enabled tasks' recipients, so the refused recipient
need not be one the request added. Two things were only in the problem's
`detail` text:

- **Which recipient.** A client has to tell its own recipient, which it can
  re-check and acknowledge, from another task's, which it can't.
- **What kind of refusal.** `SESSION_POLICY_RECIPIENT_NOT_EOA` covered both a
  plain contract and a 7702-delegated EOA with no acknowledgement. The fix for
  each is different. The grant-time contract sentence also named no address.

Studio parses the detail today. Any change to the wording would break it
without an error.

## Change

1. **New code `SESSION_POLICY_RECIPIENT_NOT_ACKNOWLEDGED`.** A 7702-delegated
   recipient that `delegatedNativeRecipients` does not acknowledge, at prepare
   and submit, and in the run-time check before a grant is applied. It was
   `SESSION_POLICY_RECIPIENT_NOT_EOA`. `NOT_EOA` now means a contract, or a
   pre-apply code read that failed.
   - Counted as a native miss in a Skill Test (`isNativeMessage`), and as a
     client failure in bundler logging (`IsClientUserOpFailure`), like `NOT_EOA`.
   - The message text is unchanged apart from its code.
2. **Prepare and submit return a typed refusal** (`NativeRecipientRefusal`)
   with the code, the recipient, the delegate its designation names now, and
   the acknowledged delegate for a changed one. `Error()` is the same sentence.
   The grant-time contract sentence now names the recipient.
3. **The problem carries the addresses.** `mapPolicyError` copies them onto
   new `Problem` fields, documented in `api/openapi.yaml`:
   - `recipient` on all three refusals;
   - `delegate` on `NOT_ACKNOWLEDGED` and `DELEGATE_CHANGED`;
   - `acknowledgedDelegate` on `DELEGATE_CHANGED`.
4. **Withdraw** keeps `NOT_ACKNOWLEDGED` and `DELEGATE_CHANGED` as their own
   codes. Before this, `DELEGATE_CHANGED` reached the client as a 400 with an
   empty `code`. The mapping moved into `mapWithdrawError`, which is now
   tested. Withdraw does not set the address fields: its refusal is a gRPC
   status, not the typed error.

## Clients

A client that switched on `NOT_EOA` for both cases has to handle
`SESSION_POLICY_RECIPIENT_NOT_ACKNOWLEDGED` before this deploys. Studio
reads the new code and fields, and falls back to the detail for an older
gateway. The SDK picks up the fields when its types are regenerated from this
spec.

## Verification

- `TestNativeRecipientDelegation`: the unacknowledged cases now expect the new
  code and forbid `NOT_EOA`.
- New tests:
  - `TestNativeRecipientRefusalNamesTheRecipient`: the typed refusal for a
    contract, an unacknowledged delegation and a changed delegate.
  - `TestMapPolicyErrorNamesTheRefusedRecipient`: the problem fields from a
    real `Validate` refusal, and the string fallback.
  - `TestProblemCopiesNativeRecipientRefusalFields`: the middleware writes them
    to the body.
  - `TestMapWithdrawErrorKeepsRecipientCodes`.
- Updated: the `IsClientUserOpFailure` table.
- About 20 package tests need the CI-only `config/test.yaml` and were not run
  locally.
