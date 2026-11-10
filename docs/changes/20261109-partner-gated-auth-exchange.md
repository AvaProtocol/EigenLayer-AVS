# Partner-gated wallet JWT issuance

`POST /api/v1/auth:exchange` now requires a registered active partner's
`X-Partner-Assertion` with scope `auth:exchange`, in addition to the existing
EIP-191 wallet proof. Both the registry and assertion must grant that scope.
The assertion's `sub` must match the non-zero `ownerAddress`; the wallet's
signature, signed message and 48-hour lifetime checks remain required.
The issued JWT includes the verified `partner_id` for attribution.

This reuses the Ed25519 partner registry, signature verification, expiry and
gateway audience checks. An omitted assertion, a plain partner ID, a user
Bearer token alone, or a `read` assertion cannot authorize issuance. Health
remains anonymous. Subsequent workflow and fund-authority calls retain their
existing user-JWT requirements; a partner assertion is not wallet authority.

JWT issuance additionally refuses an unset or blank `partner_assertion_audience`
with `503 PARTNER_AUTH_AUDIENCE_REQUIRED`, preventing an accidental unbound
environment from accepting assertions issued for another gateway. Delegated
read routes retain their existing optional-audience behavior.

## Client and configuration rollout

Before any gateway config apply, restart or image deployment, verify sufficient
volume headroom for a startup migration backup as well as normal DB writes.
Do not rely only on the periodic timer when planning storage operations.

1. Grant the existing Studio partner `auth:exchange` in its gateway registry.
   No new key pair is needed. Local/test gateways need a registered partner too.
2. Deploy Studio's server-side exchange adapter. It must authenticate its own
   user before producing an assertion whose subject is the wallet being signed.
   Preserve verified wallet link/recovery flows. Never expose the partner private
   key or its assertion to the browser. The SDK's existing server-side assertion
   minter accepts arbitrary scopes, and its client supports custom headers.
3. Deploy the enforcing gateway. Client-first ordering works because the old
   gateway ignores the extra assertion on wallet exchange.
4. Verify real Studio sign-in, rejection of bare wallet-signature exchange,
   mismatched subjects and read-only partner assertions, and normal JWT use.

Existing wallet JWTs are not revoked by gating issuance and can remain valid
for approximately 48 hours. An immediate cutover requires a separately planned
JWT signing-key rotation and replacement of operator-managed `create-api-key`
credentials, which are signed with the same secret. This change does not rotate
keys, invalidate cached Studio credentials, or stop already deployed workflows.
The CLI minting path remains privileged: it requires access to the gateway's
signing-secret configuration and is not a public HTTP endpoint.
Unapproved direct SDK/integrator JWT issuance is intentionally disabled; an
approved partner backend must mediate it. Operator Node gRPC authentication
is separate and unchanged. Privileged CLI JWTs may have longer lifetimes than
the 48-hour wallet sign-in window; do not assume they expire within two days.

## Verification

Gateway tests exercise the existing crypto verifier through `AuthExchange`,
including missing/raw-ID/forged/expired/wrong-audience/read-only assertions,
registry scope and status, subject mismatch, an invalid wallet signature with a
valid assertion, successful issuance with partner attribution, and the existing
wallet-sign-in expiration bounds. Permission-map tests and generated OpenAPI
security metadata reflect partner-gated issuance.
