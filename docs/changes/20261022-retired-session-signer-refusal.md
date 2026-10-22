# Refuse grants made for a retired session signer

## Problem

#864 made `session_signer_private_key` the only key the gateway signs session operations with. A grant installs the signer address current at grant time. After that key is replaced, every earlier grant names a signer the gateway can no longer produce.

Execution already refused those grants, but two checks did not:

- **Deploy check.** `enforceSessionPolicyDeployCheck` checks expiry and permission coverage. A grant that covers the workflow passed even when its signer was retired, so create and resume enabled automations that could never execute, with `session_policy_deploy_check` on.
- **Readiness.** The simulate verdict's `authorization.status` reported `covered` for the same grant.

The execution refusal was plain text, with no machine-readable code. `IsClientUserOpFailure` did not recognise it either, so an owner-fixable condition reached Sentry as an error.

## Change

- **New code `SESSION_POLICY_SIGNER_RETIRED`** for a grant whose signer is not the configured session signer. The remedy is granting again, which installs the current signer.
- **Resolver.** `configuredSessionSigner` returns the refusal with the code as its prefix, matching `SESSION_POLICY_EXPIRED` and the chain-window codes. `IsClientUserOpFailure` classifies it as a client failure.
- **Deploy check (create and resume).** A fund-moving workflow on a retired-signer grant is `409 SESSION_POLICY_SIGNER_RETIRED`, with the REST title "Grant was made for a retired session signer". The response carries `policyId`, `affectedTaskIds` and `required` (the grant to sign) and no `missingActions`, like an expired grant.
  - The detail has no colon. Clients that show gateway text keep only what follows the last colon.
- **Precedence.** Expiry outranks a retired signer, since either way the owner grants again. An unreadable fund-moving target outranks both. A notification-only workflow is not checked.
  - The unreadable target now wins even when a third chain also has an ordinary coverage gap. Previously an ordinary gap suppressed it, so a workflow with an ordinary gap, an unreadable target and an expired grant on three chains returned `SESSION_POLICY_EXPIRED`. The owner then re-signed and still hit the unreadable target.
  - The same applies when the grant-again refusal is on the unreadable target's own chain and another chain has an ordinary gap. Previously that returned `SESSION_POLICY_NOT_COVERING`, naming one chain's `policyId` and the other chain's `required`.
  - The same condition governs both refusals, so the fix changes that expired case too. Without an unreadable target, an ordinary gap still loses to expiry or a retired signer, as before.
- **Readiness.** `fillSimulateAuthorization` reports `no_grant`, with the grant's `policyId`, the same `required`, and a "grant again" detail. `no_grant` already means "no usable grant" and sends clients to their grant step, so the status enum is unchanged.
- **No key configured.** With no session signer configured, nothing is called retired. Production always configures one (#864).
- **OpenAPI.** `api/openapi.yaml` documents the code on create, resume, `policyId` and `required`, and the `no_grant` text. The REST types are regenerated; only doc comments change.

Not changed: `scripts/spike/*` and the live-test helpers (`grantControllerAuthority*`) still sign grants with a controller key. They install their own resolver with an explicit key, so they do not use the gateway's signer selection.

## Clients

Studio maps known 409 codes to its own copy. Until it maps `SESSION_POLICY_SIGNER_RETIRED`, it falls back to showing the gateway's detail, which is written for users. Map it next to `SESSION_POLICY_EXPIRED`: same remedy, so allow the grant again from `required`.

## Verification

- `TestDeployCheckRefusesGrantForRetiredSigner`:
  - a covering grant for a retired signer is refused with the code, the coverage sentinel, `policyId`, `affectedTaskIds`, `required` and a colon-free detail, and the runner lock is released;
  - a grant for the current signer deploys;
  - expiry outranks a retired signer;
  - a notification-only workflow still deploys.
- `TestDeployCheckUnresolvedTargetOutranksGrantAgainAlongsideOrdinaryGap`: across three chains (ordinary gap, grant-again, unreadable target), the unreadable target wins with its own `policyId` and `required` and no `missingActions`, for an expired grant and a retired-signer grant. Both variants failed before the fix.
- `TestDeployCheckUnresolvedTargetOutranksSameChainGrantAgainBesideOrdinaryGap`: across two chains (an ordinary gap; a grant-again refusal and an unreadable target on the same chain), the unreadable target wins, and `policyId` and `required` both name that chain. Both variants failed before the fix.
- `TestSimulateAuthorizationReportsRetiredSignerAsNoGrant`: the verdict is `no_grant` with the policy id and `required`; the current-signer control is `covered`.
- `TestResolverRefusesRetiredSignerWithStableCode`: the resolver error is prefixed with the code; a grant naming another signer, or no signer, is retired; the configured signer is not; with no key configured, nothing is retired.
- `TestRetiredSignerDeployCheckMapsToConflict`: 409 with the stable wire code and title.
- `TestIsClientUserOpFailure`: the wrapped resolver error is a client failure.
- `go test ./aggregator/rest/ ./pkg/erc4337/preset/` and the session, deploy-check, authorization and skill tests in `./core/taskengine/` pass. `go vet -tags=integration` is clean.
