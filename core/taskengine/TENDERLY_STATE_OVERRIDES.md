# Workflow simulation state

Execution mode belongs to the complete workflow run:

- `SimulateTask` / `POST /api/v1/workflows:simulate` simulates every action.
- Deployed workflows execute every action through the real transaction path.
- The legacy `ContractWriteNode.Config.is_simulated` field is accepted for wire
  compatibility and ignored. It cannot override either workflow mode.
- The `RunNodeWithInputs` RPC / `POST /api/v1/nodes:run` rejects contract writes and
  native transfers, including loops with those runners. Non-transaction nodes
  remain available for isolated tests. Single-node ERC20 overrides are rejected.

## Approval dependencies

Include approval and its dependent transfer or swap in the same workflow.
During simulation, `SimulationStateMap` carries raw storage diffs between
Tenderly calls. A successful simulated ERC20 `approve(spender, amount)` also
injects allowance state for the following simulated write. No on-chain approval
is submitted during workflow simulation. Real workflow execution performs the
actual approval before the dependent action.

Overrides describe effects of preceding simulated steps. The engine must not
seed an allowance merely to make an action with a missing approval pass.

This policy does not generalize storage-layout discovery or state-aware reads.
Automatic allowance injection currently uses candidate mapping slots; contract
reads still use chain RPC. Those limitations are separate from execution mode.
