# N14.a — Typed unpack of our UserOps

- **Date**: 2026-08-24
- **Status**: Implemented
- **Branch**: `feat/userop-unwrap`
- **Related**: studio `PLAN_USEROP_UNWRAP.md` (SoT), `PLAN_AGENT_ONDEMAND_ACTIONS.md` G2/G3, `UnpackExecuteCalldata`

## Problem

`nodes:run` already returned `userOpHash` and `executionStatus` (pending ≠ failed).
It did not return the inner `execute` / `executeBatch` calls we packed, so Studio
Troubleshoot still had to guess from EntryPoint events. There was no JWT-gated
way to re-poll a pending UserOp by hash.

## Decision

1. Stamp `receipt.calls: [{ to, value, selector, data }]` from
   `aa.UnpackExecuteCalldata` on pending and mined contract-write receipts
   (single-call and atomic batch). Same full array on each batch method result.
2. On AA23 / bundler reject after we have packed calldata, attach those calls
   on a failed receipt. On inner revert with a single call, also set
   `failedCall`.
3. `GET /api/v1/userops/{userOpHash}` (user JWT, same gate as `nodes:run`).
   Sender must be one of the caller's smart wallets; unknown and foreign hashes
   both 404. Not a public AA explorer.

Studio Troubleshoot consumes `receipt.calls` from execution detail and the
status GET; it does not port `UnpackExecuteCalldata` to TypeScript.

## Verification

- `go test ./core/taskengine -count=1 -run 'InnerCalls|UserOp|CreateRealTransactionResult|AtomicBatchOnDemand'`
- `go test ./aggregator/rest -count=1 -run 'PermissionMap|SimulateRequiresUserJWT'`
