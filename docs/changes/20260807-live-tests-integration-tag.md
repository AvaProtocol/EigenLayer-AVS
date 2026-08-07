# Live-chain tests behind `//go:build integration` (#690)

## Thesis

`Unit Test (core/taskengine)` was non-deterministic because live Sepolia UserOp /
Uniswap / sequential-write tests shipped in the default test set. CI now stays
deterministic; local `make test` still exercises the full suite.

## What changed

Tagged the live-chain suite with `//go:build integration`:

- `userops_withdraw_test.go`, `userops_batch_swap_test.go`
- `execute_uniswap_approval_test.go`, `simulate_uniswap_workflow_test.go`
- `execute_sequential_contract_writes_test.go`, `simulate_sequential_contract_writes_test.go`
- `eth_transfer_integration_test.go`, `uniswap_sepolia_constants_test.go`
- `tenderly_client_sepolia_test.go` (extracted WETH Sepolia case from the mixed Tenderly unit file)

`userops_withdraw_all_test.go` stays `//go:build manual` (explicit opt-in only).

Makefile:

- `make test` → `-tags=integration` (all tests, including live)
- `make test/unit` → unit only (CI-shaped)
- `make test/integration` → integration packages only
- `make test/package TAGS=integration PKG=...` supported

CI (`go test . -v -short` without the tag) no longer compiles or runs the live files.

No scheduled workflow — local make is enough when you want the live suite.
