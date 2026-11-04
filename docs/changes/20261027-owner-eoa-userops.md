# Owner-signed UserOperations from an EOA

Native support was added later; see [Native owner sweeps](20261028-native-owner-sweeps.md). The original limits below describe the initial implementation.

Date: 2026-10-27
Status: Owner-signed EOA round trip verified live on Sepolia
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

## Live Sepolia verification

The source at `b4ff8a9677c03f4a389fff24b1590c8f7d8e5f2f` passed the real SDK CLI
round trip against a source-built local gateway on 2026-10-27. Unlike the earlier
SDK test's ordinary EOA transaction, both sweep directions here used the
owner-signed prepare/sign/submit handoff. The gateway and Sepolia worker run in
the local `studio` tmux session. Only the gateway was built from this branch;
the worker uses the existing local stack binary/config.

Accounts:

- EOA: `0x2FB186De3d32F4EB45A58F6dD7133A2442f9830e` (`TEST_PRIVATE_KEY`).
- Its derived smart wallet: `0x60aCdE1B4fC1848647eaB6F1460a33F4cA2b520E`.

The test completed these steps, with source-funded UserOperations:

1. Apply the separately signed, chain-bound SMA-7702 authorization at EOA nonce
   15 using the existing delegation API. Assert the actual designation,
   implementation hash, enabled fallback owner and v0.7 EntryPoint.
   [Delegation transaction](https://sepolia.etherscan.io/tx/0xf844063bc39d8f3362605e5aae74f18bb6415af956e183a92c894b52b13383f5).
2. Move the smart wallet's `0.000002877930532126 WETH` to its owner through one
   owner-signed UserOperation, to exercise both tokens in the EOA sweep.
   [WETH setup](https://sepolia.etherscan.io/tx/0x165b808802a7ae29ed33c3d3ec28a847f9cfab81c1550500098038e5662d744b).
3. Run the actual SDK sweep CLI with the EOA as source. One owner-signed
   UserOperation transfers `1.560489 USDC` and `0.000002877930532126 WETH` to
   its smart wallet. Verify both exact token deltas and EOA owner nonce +1.
   [EOA sweep](https://sepolia.etherscan.io/tx/0x8107f5d1ecf1ea76a31a5c0c4efecb03ff58b06a5b9df0993e5db24decaa00cd).
   UserOp: `0xe1ee076737414ddc98e439137f9d98fc78fb8dbdd2e4a398a5e7737e92294af8`.
4. Restart only the gateway with `eoa_7702_execute: false`. An EOA preparation
   is refused with HTTP 412, `OWNER_EXECUTION_PREPARE_REQUIRED`, and detail
   `eoa_7702_execute is false`.
5. With that flag still false, run the actual CLI from the derived wallet back
   to the EOA. One UserOperation transfers `14.780489 USDC` and the WETH balance
   successfully. This also returns the preexisting smart-wallet test funds.
   [Derived-wallet return](https://sepolia.etherscan.io/tx/0xbaa8d87aa756eaf5c07a4995dddf04fa49060f54f966427979c94bbdb05c2005).
   UserOp: `0x64340d3eeac14099a7026b7ecb7634f6517db2e05b18807da60b2a2f765c7ae6`.
6. Re-enable EOA execution locally and return the preexisting `13.22 USDC` and
   WETH to the shared smart-wallet fixture with one fixed-amount owner batch.
   Both accounts' ERC-20 balances exactly match their starting values.
   [Fixture restoration](https://sepolia.etherscan.io/tx/0x8cdbdff8c7db4b617809a580b5e10d65f984e3c88cc9b612c539e9a49347513e).
7. Clear the EOA's delegation with a self-funded type-4 transaction. Its outer
   transaction nonce is 16 and the self-sponsored authorization nonce is 17.
   Assert `eth_getCode(EOA) == 0x` and delegation status `missing` afterward.
   [Account-code restoration](https://sepolia.etherscan.io/tx/0xcfca3519247f819642e5231fc8408415bd1f08451101702fc7f16e480e040e31).

No session grant was installed: the EOA's policy list stayed empty and the
derived wallet's policies were unchanged. Including setup and cleanup, each
account's owner UserOp nonce increased by exactly two. Gateway logs contain
one relay outcome per prepared operation and no unexpected warnings/errors
in the test window. The worker stayed healthy; it emitted no additional logs.

Final balances are again `1.560489 USDC` and zero WETH at the EOA, and
`13.22 USDC` plus `0.000002877930532126 WETH` at its smart wallet. Native ETH
paid gas. The new EOA wallet association remains stored but is hidden after
clearing the designation, preserving the previous default wallet list. No new
private key was needed. The unsigned operation reviews, transaction hashes and
balance assertions are retained in the local SDK's ignored
`logs/20261027-eoa-userop-roundtrip-proof.json`.

The local gateway remains on this feature binary with both owner/EOA flags
enabled for Sepolia. Live evidence covers source-funded Sepolia execution;
Base and sponsored execution still need their own live checks.
