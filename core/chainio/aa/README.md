# Account Abstraction (AA) Package

Helpers for Alchemy Modular Account v2 accounts on the ERC-4337 v0.7 EntryPoint.

## Files

- `ma_v2.go`: `execute` / `executeBatch` calldata packing (`PackExecuteMAv2`, `PackExecuteBatchMAv2`).
- `aa.go`: `UnpackExecuteCalldata`, the inverse of the packers.
- `ma_v2_account.go`: counterfactual address and init code from the MA v2 factory (`GetSenderAddressMAv2`, `GetInitCodeMAv2`).
- `derive.go`: `DeriveSenderAddress` / `DeriveInitCode` for a factory taken from a wallet record or request. Any factory but MA v2 is refused with `ErrLegacyFactory`.
- `ma_v2_*.go`: session-grant module install, uninstall, hooks and on-chain entity state.
- `entrypoint_events.go`: the EntryPoint `UserOperationRevertReason` event.

## ABIs

Every ABI is a small inline JSON constant next to the code that uses it, covering only the methods and events the gateway calls. There are no generated bindings. When a contract changes, edit the constant and the tests that pin its selectors.
