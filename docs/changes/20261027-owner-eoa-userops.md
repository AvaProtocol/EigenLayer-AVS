# Owner-signed UserOperations from an EOA

Date: 2026-10-27
Status: Implementation and local verification; live delegation/inclusion pending
Branch: `feat/1987-owner-eoa-userops`
Related: [Studio #1987](https://github.com/AvaProtocol/studio/issues/1987),
[SDK #289](https://github.com/AvaProtocol/ava-sdk-js/pull/289), gateway #871

## Result and scope

The generic owner-signed Manual workflow handoff accepts an EOA that already
delegates to the canonical SMA-7702 implementation. The selected runner is the
EOA, not its derived smart wallet. Multiple static ERC-20 transfers fit in one
`executeBatch`, one owner-signed UserOperation, and one keyless gateway relay.

The EOA must already have its explicit, chain-bound EIP-7702 authorization
applied and a stored `kind=eoa_7702` wallet row. The existing delegation API
performs that separate action; owner prepare and submit do not modify account
code or install a session grant. An ordinary EOA without account code still
cannot execute a UserOperation. Initial delegation is not bundled into the
UserOperation and native-value calls remain unsupported.

Both `owner_execution_enabled` and `smart_wallet.eoa_7702_execute` are required
for an EOA source. Both keep their existing false defaults. Only Sepolia and
Base are supported, with the existing canonical delegate and runtime hash.
Disabling the flag blocks new preparation and first relay; already attempted
operations remain reconcilable by the same hash.

## Account checks and signing

- The stored source must be owned by the JWT user, have address equal to owner,
  no CREATE2 salt or nonzero factory, and name the canonical delegate. Retired,
  stale, malformed and unknown wallet kinds are refused.
- The chain RPC must match the selected chain. The EOA must have exactly
  `0xef0100 || 0x69007702764179f14F51cdce752f4f775d74E139`. The delegate's
  deployed runtime must hash to
  `0xecfc0328ce4d953a4d452e11f54b578fa2d7f2f8126246d1120ca24d11845081`.
- `getFallbackSignerData()` on the EOA must return the JWT owner and enabled
  signing. The implementation permits changing this signer in storage, so the
  default `address(this)` signer alone is insufficient evidence. `entryPoint()`
  must be the v0.7 EntryPoint.
- The EOA source never calls the CREATE2 factory and never attaches factory
  deployment data. Its nonce is still entity 0 with global option 1.
- The owner signs `personal_sign` of the 32 raw UserOperation hash bytes. The
  gateway checks canonical ECDSA, normalizes `v`, and supplies `0xFF 0x00`.
  Pricing and paymaster fields are finalized before this signature. A refused
  sponsor remains an error; it does not charge the EOA through a fallback.
- Submit accepts only the signature. It rechecks the stored association,
  supported account identity, nonce and gas funding, and follows the existing
  capture check for byte-identical workflow calldata. It never reprices or
  changes amounts. A sweep compiled from a single balance snapshot contains
  static amounts; this change adds no token balance rechecks.

The signing recipe is verified against Alchemy's v2.0.0 source, commit
`bd75c9eca7ff5a66aaebc3cf2dab43ddc4622482`:
[SMA-7702 fallback signer](https://github.com/alchemyplatform/modular-account/blob/bd75c9eca7ff5a66aaebc3cf2dab43ddc4622482/src/account/SemiModularAccount7702.sol)
and [fallback UserOp validation](https://github.com/alchemyplatform/modular-account/blob/bd75c9eca7ff5a66aaebc3cf2dab43ddc4622482/src/account/SemiModularAccountBase.sol).
The later raw-hash validator is not accepted by the implementation pin.

## The same owner's derived wallet

A canonical delegated EOA retains its ECDSA key, so it can also sign the
derived-wallet fallback signature with signature type 0. The gateway now
recognizes that verified designation instead of requiring empty owner code.
This preserves the return leg after the EOA has delegated. The EOA execution
flag does not gate a derived-wallet source. Unverified contract/delegation
owners are still refused.

The preparation identity now includes the signing EOA's code hash. A delegation
change between preparation and first relay requires a new preparation/signature.
Old ten-minute preparations without that identity field fail closed and must
be prepared again after this deployment. The additive private TTL record field
does not change wallet/task storage keys or public request/response shapes.

## Verification

Local tests cover account/config guards, a nil-salt continuation across restart,
unchanged priced fields, one relay despite repeated submit, and the real HTTP
prepare/sign/submit/status handlers for both account kinds. An offline EVM test
executes the exact pinned runtime through an EIP-7702 designation, including
owner signing, nonce selection, changed fallback and disabled fallback cases.
The public runtime fixture was fetched read-only from Sepolia block 12007129;
tests assert its hash rather than trusting the source label.

Local checks passed:

- `go test -race ./pkg/erc4337/preset ./core/taskengine ./aggregator/rest -run '^TestOwner' -count=1 -v`
  — 27 top-level tests, including the real HTTP round trips and pinned-runtime
  signature checks.
- `make build`.
- `make rest-gen` — description-only OpenAPI changes produce no generated Go diff.
- `go run scripts/compare_storage_structure.go origin/staging` — no breaking
  storage changes; `git diff --check` is clean.

The local-EVM tests use go-ethereum's existing version; `go mod tidy` adds the
indirect dependencies needed to execute its in-memory state/runtime packages.

No real EOA has been delegated or swept through this new path yet. The earlier
SDK round trip used an ordinary EOA transaction out and a derived-wallet UserOp
back. That result does not establish EOA UserOp inclusion. A live check must
separately review/apply delegation, prepare the EOA transfer batch, sign/submit
it once, and verify the receipt and token deltas. A live test must also check
the same owner's derived-wallet return leg.
