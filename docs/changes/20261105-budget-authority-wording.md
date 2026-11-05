# Context-neutral unknown-authority refusals

`CheckAuthorities` is shared by unsigned budget preparation, signed submission,
and budget runs. Its `BUDGET_AUTHORITY_UNKNOWN` detail now identifies the
unrecognized module and entity and asks the owner to remove it and check again.
It makes no claim that runs already exist or are paused.

The workflow run guard alone adds that Ava's runs for this wallet are paused.
It extracts `BudgetPolicyError.Detail` through `errors.As`, including wrapped
refusals, instead of placing the machine-readable error code or wrapper text in
the run's prose. Its `BUDGET_CHANGING` code and five-second retry stay unchanged.

OpenAPI explicitly documents 409 `BUDGET_AUTHORITY_UNKNOWN` at both prepare and
submit, including the submit operation's own description. Existing HTTP statuses,
error codes and submission-boundary metadata are
unchanged. SDK and Studio changes are outside this PR.

Regression coverage exercises the actual authority check through prepare and
submit, the workflow run guard with direct and wrapped refusals, and serialized
REST problems for both policy routes. The three engine regression tests fail
on the previous implementation for the wording defects they cover.

Validation: budget engine tests and all REST packages pass with the race
detector. `go vet` passes for those packages, and `make rest-gen` leaves the
generated Go files unchanged.

The local stack is managed separately; this work does not rebuild or restart
the running gateway, Studio, or ngrok.
