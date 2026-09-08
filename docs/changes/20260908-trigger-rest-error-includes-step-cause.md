# Trigger REST envelope includes step cause

- **Date**: 2026-09-08
- **Status**: Implemented
- **Branch**: fix/trigger-error-includes-step-cause
- **Related**: ava-sdk-js #263, `docs/changes/20260413-execution-status-redesign.md`

## Problem

Blocking `POST /workflows/{id}:trigger` returned HTTP 200 with

```json
{ "status": "failed", "error": "1 of 2 steps failed: w" }
```

The bundler reason (`eth_sendUserOperation: replacement underpriced`) was on `execution.steps[].error`. gRPC `TriggerTaskResp` already had `steps`; the REST handler copied only `executionId` / `status` / timestamps / `error` and dropped `steps`. `AnalyzeExecutionResult` built `error` from **node names only**.

The SDK pass-through was faithful. Clients that only read `:trigger` could not see why the step failed.

## Decision

1. `failedStepLabel` — `execution.error` is `N of M steps failed: <name>: <step.error>, …`.
2. `mapping.ProtoToOpenAPITriggerWorkflow` — REST trigger copies proto `error` **and** `steps` (OpenAPI `TriggerWorkflowResponse.steps`). There was no conversion unit test; the handler did the copy inline.

No retry. The gateway still does not wait-and-resend on `replacement underpriced`. It just stops hiding the cause.

## Alternatives

- Envelope-only string, still omit `steps`. Typed `error` would be enough for the SDK today, but REST would stay a lossy projection of gRPC.
- Retry inside `SendUserOp`. Hides the class of failure; wrong if the pending op is a real replacement.

## Verification

- `TestAnalyzeExecutionResult_IncludesBundlerCause`
- `TestAnalyzeExecutionResult_SomeStepsFailed` / `_AllFailure` now assert the step cause is in the summary
- `TestProtoToOpenAPITriggerWorkflow_CopiesErrorAndSteps` (`status: failed`)
- `TestProtoToOpenAPITriggerWorkflow_ErrorCopiesVmCauseAndSteps` (`status: error` — `VM execution error: …` plus steps)
- `TestProtoToOpenAPITriggerWorkflow_WaitingNormalizesAwaitStepType` (`status: waiting` → `steps[].type` is `await`, not `NODE_TYPE_AWAIT`)
