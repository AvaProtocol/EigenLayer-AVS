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
  cost. The conservative reserve does not spend an existing EntryPoint deposit.
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

`TestOwnerNativeSweepWithEntryPointDeposit_Sepolia` is the opt-in live E2E check.
It uses the real owner preparation, pricing, signature-only submit and bundler
relay paths on a dedicated MA-v2 runner at salt 26, without a session grant. It
ensures the runner has a real EntryPoint deposit of at least 0.001 Sepolia ETH,
captures its entire native balance as the cap, and sends cap minus the signed
maximum fee to the owner. It waits for a successful on-chain `UserOperationEvent`
for the reviewed hash, checks the exact recipient amount and runner/deposit gas
accounting, and verifies a repeated submit retains the same unsigned operation.

Local tests load `.env.local`, then `.env`, through `testutil.LoadDotEnv`; existing
process variables take precedence. `requireOwnerKey` reads `TEST_PRIVATE_KEY`
from that environment and removes its optional `0x` prefix. CI's
`setup-test-config` action exports the `TEST_PRIVATE_KEY` secret to `GITHUB_ENV`;
the owner key is not read from `config/test.yaml` or the controller key. If
`OWNER_EOA` is supplied, it must match that key.

With `config/test.yaml` configured for Sepolia and a funded test owner:

```sh
go run ./scripts/fixture_wallet -salt 26 -deploy
go run ./scripts/fixture_wallet -salt 26 -fund 0.02
OWNER_NATIVE_SWEEP_E2E=1 go test -tags=integration ./core/taskengine \
  -run '^TestOwnerNativeSweepWithEntryPointDeposit_Sepolia$' -v -count=1 -timeout=6m
```

The fixture command needs the key in its process environment; unlike the test
binary, it does not load dotenv files. A worktree's `config/test.yaml` symlink
does not load the main checkout's dotenv files either; export their values into
the process or provide dotenv files in the worktree. Deploy before funding: the
helper funds first when both flags are supplied, and its fixed 120,000-gas fund
transaction can run out of gas while creating an empty recipient account.
The test is self-funded and transfers
the dedicated runner's spendable ETH back to its owner. It may top up the
EntryPoint deposit from the owner by at most 0.001 ETH plus transaction gas;
it does not withdraw that deposit or delegate the owner's EOA. Run it without
concurrent use of the fixture or owner. The integration tag plus explicit
`OWNER_NATIVE_SWEEP_E2E=1` opt-in are required, and `-short` always skips it.

The live run on 2026-10-29 UTC passed in 43.14 seconds. It exercised the derived
MA-v2 wallet path, including owner signing and successful on-chain execution;
it did not exercise a sweep directly from a delegated EOA.

- Runner (salt 26): `0x6e4Ee980350e251FB19bdCacBD5E07B01Aa4f856`.
- UserOp: `0xb0fab1030383a42229238aaa33a1ef6ad6adb4d3882c5e5127df6f8e6505dea6`.
- Transaction: [0x2595d8554e1166d78ee3f054f02ee94cb1c037d09e493d7bf68a92c41b25ab59](https://sepolia.etherscan.io/tx/0x2595d8554e1166d78ee3f054f02ee94cb1c037d09e493d7bf68a92c41b25ab59).
- Captured balance: `20,000,000,000,000,000` wei; recipient received exactly
  `19,975,074,392,522,320` wei (99.875% of that balance).
- Signed maximum gas reserve: `24,925,607,477,680` wei; actual gas cost:
  `20,808,603,121,290` wei.
- EntryPoint deposit before/after: `1,000,000,000,000,000` /
  `979,191,396,878,710` wei. Runner plus deposit reconciled exactly to the
  transfer and actual gas cost, and repeated submit preserved the unsigned
  operation and original hash.

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
an explicit final cursor of `null` or `""` proves completion. Tests cover capped
responses, final-page markers, failed lookups and a complete 1,024-row fixture
whose rows are neither truncated nor rounded.
