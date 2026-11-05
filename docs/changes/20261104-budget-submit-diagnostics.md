# Budget submit failures and delegated-owner refusals

## Send boundary

Budget signature-only submit can fail before any relay, or after relay while
saving its result. HTTP 503 alone cannot distinguish those outcomes. The optional
Problem field `submissionStarted` now states what is known for that request:

| Value | Meaning | Client recovery |
| --- | --- | --- |
| `false` | A loaded preparation was refused under the wallet locks before attempting its first-send marker. This request sent nothing. | End this press's wait, keep its reason visible, and let the owner review/sign again. |
| `true` | Sending started or may have started. Includes a failed marker write, an already marked attempt, and a post-relay save failure. | Follow the original preparation/hash; do not start a second operation. |
| absent | No send-boundary claim, including a failed record load or an older gateway. | Treat as uncertain. |

A lost HTTP response is always uncertain. A later `prepared` read alone is not
proof that no other in-flight request can send. Do not infer unsent from status
codes, error text or elapsed time. The field is additive; no existing hold,
nonce, expiry or retry rules change. A failed first marker write is deliberately
conservative even when the local storage fake proves it made no write. A repeat
of a settled `unsent` preparation still reports false and never relays it.

Submit's required-read response is now titled **Budget couldn't be submitted**.
The detail remains generic. The REST error retains its wrapped cause only for
server logging/Sentry, with the existing request id. The final snapshot, account,
nonce, authority inventory, approval, session-flight and canonical-block reads
retain their available RPC causes instead of dropping them. Neither signatures
nor operation bodies are added to logging.

Budget preparation preserves the difference between a failed check and a
successful check that proved an unsupported account:

- Owner-operation RPC/pricing failures return an authored **503
  BUDGET_READ_UNAVAILABLE**, with no instruction to blindly retry.
- A missing/invalid SMA-7702 pin, missing chain reader, implementation-hash
  mismatch or RPC-chain configuration mismatch returns **503
  OWNER_ACCOUNT_UNAVAILABLE**. Its copy never asks the owner to change wallets.
- A confirmed unsupported implementation, fallback owner or EntryPoint returns
  **409 OWNER_ACCOUNT_UNSUPPORTED**; preparing again cannot fix that account.
- The EOA execution gate stays **403 OWNER_EXECUTION_DISABLED**. Funding and
  confirmed incompatible delegation keep their structured 409 refusals.

The Sepolia/Base support check runs before pin validation: a delegated owner
on any other chain gets a permanent 409 `EOA_DELEGATION_MISSING` with its
observed `ownerDelegate`, even when that chain has no pin. After a supported
chain passes that gate, pin validation happens before the designation
comparison. The same distinction
applies to policy checks of an `eoa_7702` runner, including preserved RPC causes.
Delegation wording asks the owner to check the required setup or choose a
compatible source, rather than telling every owner to remove delegation: an
undelegated EOA needs delegation enabled to be the sender.

A regression runs the real account inspector against a local JSON-RPC error
server, and the REST test checks that the 503 body contains none of that
server's private error text. Retirement-read errors also preserve their wrapped
RPC cause. Explicitly marked `PolicyValidationError` values retain their safe,
authored 400 `POLICIES_REJECTED` reason, including an expired signing window,
invalid expiry range, policy ID and structural permission inputs. Native
recipient RPC checks are kept outside that public validation wrapper. An
unexpected failure instead returns 503 `POLICIES_UNAVAILABLE` with fixed copy
and its private cause logged; a storage or signer-key fault is not a client
validation reason. Wallet-hold storage reads also retain their original cause.

An authored 4xx with an explicitly attached private cause is logged at info
level, with its code, route and request id. Ordinary 4xx responses without such
a cause do not gain a log entry, and expected owner refusals are not raised as
server-error alerts. The diagnostic cause never appears in the Problem body.

## Unsupported owner account code

The shared SMA-7702 designation refusal now wraps the same
`EOA_DELEGATION_MISSING` sentinel through configuration, preset and engine code.
Budget prepare/submit, policy checks of an `eoa_7702` runner and owner-workflow
prepare/submit return HTTP **409** with that code, rather than a prose-only 400 or 412. `ownerDelegate` carries the
observed address only when the owner code is a strict EIP-7702 designation. An
ordinary contract's code does not invent a delegate address.

The REST detail is authored for the owner, not the internal runtime comparison.
Studio can recognize a wallet such as MetaMask from `ownerDelegate` and link to
its own account-switching instructions. Gateway operations still never install
another delegation and still refuse every unsupported owner implementation.

## Validation and deployment

Regression tests exercise pre-send RPC failures, both marker and post-send
storage failures, retained causes versus generic wire messages, explicit false
JSON serialization, observed delegates, and the typed refusal through both REST
mappers. Review regressions additionally cover missing/invalid pins, wrong RPC
chain, permanent account refusals, both undelegated-EOA paths, policy-reader
failures, `unsent` repeats and `HTTPError.Error`/`Unwrap`. The follow-up
unsupported-chain test fails on the prior head for Ethereum and Base Sepolia,
with the execute flag on and off, and passes with the reordered gate. Validation
regressions exercise real engine paths for expiry/range/input failures and
confirm RPC and signer/storage failures never become public validation text. `make rest-gen` regenerates the additive Problem fields and 409 response
types. An SDK patch must sync these fields before depending on their static types.

The earlier broad test failures were compared with clean `e9afe6ff`:

- A fresh baseline worktree without the ignored `.env` reproduces the four
  `manual_trigger_loop_test.go` failures requiring `OWNER_EOA` or
  `TEST_PRIVATE_KEY`. These are existing fixture requirements, not introduced
  code failures. No key values are added to tracked files.
- With the standard local fixtures, the full `go test -short ./core/taskengine
  -count=1 -timeout=6m` package passes on both the clean baseline and the original
  branch (about 186 seconds each). The reviewed fixes also pass the complete
  taskengine, REST subtree, config and preset packages; taskengine takes about
  201 seconds. The final REST subtree is re-run after the fallback-copy edit.
  The chain-order/validation follow-up passes the same full package set
  (taskengine about 179 seconds) and the expanded five-package focused race
  suite, including the new policy-validation and wallet-hold error tests.
- The initial workflow-admission timeout did not reproduce in those baseline
  and full-package runs. A later race run concurrent with broader tests/lint
  hit the same admission-test deadline. Re-running the complete focused race
  suite in isolation passes all five packages (including that test); no test
  was skipped to get the passing run. This is not asserted to be a confirmed
  pre-existing code defect.
- The budget, owner-operation and Problem regression suites pass with the race
  detector, including the four previously missing-fixture cases. Vet and lint
  for changed code also pass.

This work does not replace the running local gateway or alter deployed flags.
The original live failure cannot be diagnosed retrospectively from the old
title-only log. After installing this binary, repeat the owner's existing Review
press and correlate its Problem `instance` with the server error. Before-send
503s will include `submissionStarted: false`; uncertain storage failures remain
on the original attempt. No live signature or transaction is needed for the
offline regression suite.
