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
also run. These tests use local RPC fixtures. No live signature or broadcast is
part of this change's validation yet.

Balance discovery requests the provider's 100-row single-response limit. A
nonempty cursor fails the node; a response at/above the limit without an explicit
final-page marker also fails rather than being accepted as a complete portfolio.
No cursor is followed and no pagination is added. A failed lookup of requested
tokens propagates an error instead of synthesizing zero balances. Successful
responses retain the same array shape and exact raw amounts. Tests cover capped
responses, final-page markers, failed lookups and a complete 1,024-row fixture
whose rows are neither truncated nor rounded.
