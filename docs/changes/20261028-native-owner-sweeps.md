# Native ETH in owner-signed workflow batches

The owner handoff can prepare ERC-20 and native ETH transfers in one atomic MA-v2
`executeBatch`. This covers derived smart wallets and already delegated, pinned
SMA-7702 EOAs. It adds no account setup, session permission or wallet-native
EIP-5792 adapter. Existing owner/account/chain/nonce/signature checks still apply.

## Workflow contract

A Manual graph may end in one ContractWrite, one ETHTransfer, or a ContractWrite
immediately followed by a terminal ETHTransfer. Read/check nodes remain supported;
all declared writes must actually run. The combined batch has at most 32 calls.
Native value on ContractWrite remains refused; ETHTransfer encodes an empty-data
call to its destination. Zero, negative, oversized, unresolved and `MAX` owner
amounts are refused. Use the captured Balance output as a positive decimal wei cap.

The additive ETHTransfer config field `reserveGas: true` means:

- `amount` is a captured upper bound, not an instruction to read the balance again.
- Self-funded: final native value = cap minus the operation's signed maximum gas
  cost. The transfer amount never includes an existing EntryPoint deposit; gas
  may still be paid from that deposit.
- Sponsored: final native value = cap. Sponsorship is finalized against that exact
  calldata before hashing; a policy refusal never falls back to self funding.
- Any unused prefund may remain in the source account's EntryPoint gas deposit;
  the sweep does not withdraw pre-existing or refunded EntryPoint deposits.
- A cap too small for fees is refused. The user can deselect ETH or fund it.
- Ordinary session/Auto/simulate reject this owner-only field. Fixed transfers
  with the default `false` keep their ordinary behavior.

All pricing finishes before returning a hash. A self-funded reserve is first
estimated with a 1-wei native call, then the final amount is estimated with gas
limits reset to zero. One padded fee quote is reused for those estimates, so a
moving base fee does not drive the reserve loop. Gas ceilings only increase
between rounds; this covers extra
nonzero calldata bytes without reserve oscillation. At most eight adjustment
rounds are allowed; failure to converge refuses prepare. No post-sign repricing
or repair is added. Recipients whose behavior rejects estimation are refused.

Funding checks subtract all native call values from account balance, then require
remaining balance plus EntryPoint deposit to cover the maximum unsponsored fee.
A paymaster covers gas only: native call values must still fit account balance.
Sponsored zero-value/ERC-20 batches do not read native balance or EntryPoint
deposit. Native batches and all self-funded batches retain their funding checks.
If the deposit covers the full prefund, no prefund leaves the wallet: the signed
reserve remains in its native balance and the deposit pays actual gas. With a
zero deposit, the wallet pays the full prefund before the transfer, its native
balance reaches zero, and unused prefund remains in the EntryPoint deposit.

Prepare persists the captured batch, including the raw native cap. Submit resolves
that stored batch using the stored operation's fee fields, compares exact calldata,
and rechecks existing authority/nonce/funding
conditions. It never changes a transfer amount, operation or hash. Only the
signature is accepted; the durable submitting marker and uncertain-send behavior
are unchanged. Collected writes are withheld from completed simulation steps.
Workflow reads and branch conditions are evaluated only at prepare. An incoming
transfer while the owner signs does not change the reviewed operation. Old
unsigned preparations without a captured batch must be prepared again; previously
attempted submissions remain reconcilable by their original hash.

The owner-only `reserveGas` field is also refused at stored workflow creation,
including ETHTransfer loop runners, before a session task can be persisted.

## Validation

Tests cover mixed and native-only graphs, ordering and unsupported writes, raw
caps, fee convergence/refusal, sponsored and self-funded funding, pinned EOA
identity, REST/protobuf field round trips, and submit-once with unchanged calldata.
All owner tests run with the race detector; the REST, pricing and AA package suites
also run. These tests use local RPC fixtures; the live Sepolia check below also
passed against the real chain and bundler.

The opt-in live Sepolia suite has two cases:

- `TestOwnerNativeSweepWithEntryPointDeposit_Sepolia` uses the dedicated salt-26
  runner with a deposit of at least 0.001 ETH, explicitly requiring that it covers
  the entire signed prefund.
- `TestOwnerNativeSweepWithoutEntryPointDeposit_Sepolia` derives a fresh random
  salt each run and requires zero deposit before prepare. This exercises the
  account's payment of `missingAccountFunds` from its native balance. A previous
  run's gas refund cannot accidentally fund this case.

Both use real owner preparation, pricing, signature-only submit and bundler relay
without a session grant. A count-only wrapper delegates to the production backend
and asserts exactly one relay across repeated submit calls. Each case captures
the runner's entire native balance and sends cap minus the signed maximum fee
to the owner. A successful `UserOperationEvent` under the reviewed hash, the exact
reviewed recipient/value and separate wallet/deposit balance assertions prove
execution and funding. The owner's overall balance delta is diagnostic because
unrelated transfers or bundle beneficiary payments may affect it. Stored unsigned
operation bytes and the original cap must remain unchanged.

Local tests load `.env.local`, then `.env`, through `testutil.LoadDotEnv`; existing
process variables take precedence. `requireOwnerKey` reads `TEST_PRIVATE_KEY`
from that environment and removes its optional `0x` prefix. CI's
`setup-test-config` action exports the `TEST_PRIVATE_KEY` secret to `GITHUB_ENV`;
the owner key is not read from `config/test.yaml` or the controller key. If
`OWNER_EOA` is supplied, it must match that key.

With `config/test.yaml` configured for Sepolia and a funded test owner:

```sh
OWNER_NATIVE_SWEEP_E2E=1 go test -tags=integration ./core/taskengine \
  -run '^TestOwnerNativeSweep(With|Without)EntryPointDeposit_Sepolia$' -v -count=1 -timeout=12m
```

Setup uses the owner key to deploy each missing fixture before funding it,
estimates setup transaction gas, and tops each runner up to 0.02 ETH if needed.
The deposit-present case may also top up its EntryPoint deposit by at most
0.001 ETH. The spendable native value returns to the owner; unused prefund remains
in the owner-controlled runner/deposit. The test logs the fresh salt and address.
It does not withdraw deposits, install grants or delegate the owner's EOA.

A worktree's `config/test.yaml` symlink does not load the main checkout's dotenv
files; export their values into the process or provide dotenv files in the
worktree. The separate `scripts/fixture_wallet` CLI does not load dotenv files.
When using it manually, deploy before funding: its fixed 120,000-gas fund
transaction can run out of gas while creating an empty recipient account.
Run without concurrent use of the fixture or owner. The integration tag plus
explicit `OWNER_NATIVE_SWEEP_E2E=1` opt-in are required, and `-short` always skips it.

Both live cases passed on 2026-10-29 UTC, capturing `20,000,000,000,000,000` wei
each. The deposit-covered case took 76.31 seconds; the zero-deposit case took
70.88 seconds. All amounts below are wei.

| Case | Prefund paid from wallet | Wallet after | EntryPoint deposit after | Transaction |
| --- | ---: | ---: | ---: | --- |
| Deposit covers gas | 0 | 14,062,104,218,630 | 988,982,198,347,330 | [Sepolia transaction](https://sepolia.etherscan.io/tx/0x754b65feab8411e471368e2b1b8cf75633bf755b3f1cd58de0f64d7f4edffab7) |
| Zero deposit | 34,718,410,415,520 | 0 | 3,014,605,659,950 | [Sepolia transaction](https://sepolia.etherscan.io/tx/0x49f4ab2e0dc5e278ff57f34c731ed302d086e9b461aa9d2fd6a9587adee778d9) |

- Deposit-covered runner (salt 26): `0x6e4Ee980350e251FB19bdCacBD5E07B01Aa4f856`.
  Its initial deposit was `1,000,000,000,000,000`; the reviewed transfer was
  `19,985,937,895,781,370`, reserve `14,062,104,218,630`, and actual gas cost
  `11,017,801,652,670`.
- Zero-deposit runner: `0xC4eb2D5495615C8a9948eA3973CeABd7D81a7893`, fresh salt
  `239944039285481479654117800643082486269`. Its reviewed transfer was
  `19,965,281,589,584,480`, reserve `34,718,410,415,520`, and actual gas cost
  `31,703,804,755,570`.

Both cases matched the exact wallet/deposit split and counted one real relay
after repeated submit, with unchanged stored unsigned bytes and original hash.
The owner's observed balance delta also matched the transfer in both runs.
These runs cover derived MA-v2 wallets, not a sweep directly from a delegated EOA.

Balance discovery requests the provider's 100-row single-response limit. A
nonempty cursor fails the node; a response at/above the limit without an explicit
final-page marker also fails rather than being accepted as a complete portfolio.
No cursor is followed and no pagination is added. A failed lookup of requested
tokens propagates an error instead of synthesizing zero balances. Successful
responses retain the same array shape and exact raw amounts. These completeness
and lookup-error rules apply to every Balance node: owner preparation, ordinary
simulation, and stored Auto/session workflows, even when owner execution is
disabled. This deliberately changes ordinary workflows that previously accepted
partial results or treated a failed requested-token lookup as a zero balance.
Bare arrays and wrappers without a cursor still pass below 100 rows; at the cap,
an explicit final cursor of `null` or `""` proves completion. A cursor-less wrapper
with 100 or more rows fails. A provider that silently caps an array below 100
rows cannot be detected without an explicit completion marker or pagination.
Tests cover capped responses, final-page markers, failed lookups and a complete 1,024-row fixture
whose rows are neither truncated nor rounded.
