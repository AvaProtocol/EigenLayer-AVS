# Carry Base GHO approval into dependent simulations

A funded Studio Aave repay simulation failed in Base GHO's `transferFrom`
with panic `0x11` (arithmetic underflow), even though its approve node succeeded
and the runner held enough GHO. `UpgradeableGhoToken` stores allowances at
mapping slot 5; automatic approval propagation tried only slots 1, 2, 3 and 10.
The dependent repayment therefore still read zero allowance.

Include slot 5 in the existing allowance candidates. Injection still requires
a successful approve simulation; no allowance is created after a rejected
approval. This is a targeted addition, not general storage-layout discovery;
ERC-7201 namespaced token layouts remain outside this candidate strategy.

Verification:

- A read-only Base `eth_call` with the old candidates reproduced panic `0x11`;
  adding slot 5 simulated repayment of exactly 1 GHO to an actual debt position.
- The source-backed Studio funded suite passed two supply cases and the GHO
  repay case through the local gateway and production record-only report handler.
  Real allowances began at zero; wallet balances and scaled position principal
  remained unchanged. No live transaction was broadcast.
- The full selected Studio harness passed 26 tests, including failure reporting
  and the producer/SDK contract suite.
- A network-free VM regression checks GHO's actual allowance slot after a
  successful mocked approval and verifies that a rejected approval injects none.
  Focused gateway checks passed with the race detector.

The local test gateway was rebuilt with this change and the separately tested
write-confirmation fix; its existing configuration and data were preserved.
