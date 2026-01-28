# Telegram Transfer Notification – Code Change Plan

## Problem (from client logs)

When a single-node REST (Telegram) run is executed with `options.summarize: true` and `inputVariables.transfer_monitor` containing transfer event data:

- The request body correctly has empty `text` (server is expected to fill it).
- The server **does** inject a summary, but the message is the **generic simulation fallback** instead of a transfer-specific message.

**Example of current (wrong) message:**

```
Simulation: Workflow successfully completed
Smart wallet 0x5d814... (owner 0xc60e...) completed the 'singleNodeExecution_restApi' node.
```

**Desired message (when transfer_monitor is present):**

```
⬆️ Sent 1.5 ETH to 0x00...02 on Base
```

### Root cause

The issue is in the data flow:

```
vm_runner_rest.go:1007 → FormatForMessageChannels(s, "telegram", r.vm)
    ↓
summarizer.go:129 → ExtractTransferEventData(vm)
    ↓
summarizer_deterministic.go:2553 → Only checks vm.ExecutionLogs (misses vm.vars)
    ↓
Returns nil → Falls back to generic "Smart wallet..." message
```

- `FormatForMessageChannels` in `vm_runner_rest.go:1007` uses `ExtractTransferEventData(vm)` as a fallback when the summary has no "structured" data.
- `ExtractTransferEventData` (in `summarizer_deterministic.go:2553`) **only** looks at `vm.ExecutionLogs` for an EVENT/TRIGGER step with Transfer data.
- In a **single-node** run, there is **no** such step in ExecutionLogs; the transfer is only in **inputVariables**, which are merged into `vm.vars` via `AddVar()` (see `run_node_immediately.go:252-254`).
- So `ExtractTransferEventData` returns `nil`, and the code falls back to the deterministic single-node body.

---

## Code change plan

### 1. Extend `ExtractTransferEventData` (summarizer_deterministic.go:2553)

**Keep** the existing behavior: first look for an EVENT/TRIGGER step in `vm.ExecutionLogs` with Transfer data and use it if found.

**Add** a fallback when no such step exists: while holding `vm.mu`, check `vm.vars["transfer_monitor"]`:
- If present and it is a `map[string]interface{}` with a key `"data"` that is also a `map[string]interface{}`, and `data["eventName"] == "Transfer"`, then build a `TransferEventData` from that map.

**Implementation approach:**

```go
// Add constant at package level
const transferMonitorVarName = "transfer_monitor"

// Add helper function to avoid code duplication
func transferEventDataFromMap(data map[string]interface{}) *TransferEventData {
    eventName, _ := data["eventName"].(string)
    if eventName != "Transfer" {
        return nil
    }

    transfer := &TransferEventData{}
    if dir, ok := data["direction"].(string); ok {
        transfer.Direction = dir
    }
    if from, ok := data["fromAddress"].(string); ok {
        transfer.FromAddress = from
    }
    if to, ok := data["toAddress"].(string); ok {
        transfer.ToAddress = to
    }
    if val, ok := data["value"].(string); ok {
        transfer.Value = val
    }
    if sym, ok := data["tokenSymbol"].(string); ok {
        transfer.TokenSymbol = sym
    }
    if name, ok := data["tokenName"].(string); ok {
        transfer.TokenName = name
    }
    if ts, ok := data["blockTimestamp"].(float64); ok {
        transfer.BlockTimestamp = int64(ts)
    } else if ts, ok := data["blockTimestamp"].(int64); ok {
        transfer.BlockTimestamp = ts
    }
    return transfer
}

// In ExtractTransferEventData, after the ExecutionLogs loop:
if transfer == nil {
    // Fallback: check vm.vars["transfer_monitor"]
    if tmRaw, ok := vm.vars[transferMonitorVarName]; ok {
        if tmMap, ok := tmRaw.(map[string]interface{}); ok {
            if dataRaw, ok := tmMap["data"]; ok {
                if data, ok := dataRaw.(map[string]interface{}); ok {
                    transfer = transferEventDataFromMap(data)
                }
            }
        }
    }
}
vm.mu.Unlock()
```

**Thread-safety:** Keep a single lock over the block that reads both ExecutionLogs and vars; release before calling `resolveChainName(vm)`.

### 2. No changes elsewhere

- **vm_runner_rest.go:** No change. Already calls `FormatForMessageChannels(s, "telegram", r.vm)` at line 1007.
- **summarizer.go:** No change. `FormatForMessageChannels` already calls `ExtractTransferEventData(vm)` at line 129.
- **Client:** No change. Keep sending `inputVariables.transfer_monitor` with the same shape.

### 3. Tests

Add a test in `summarizer_ai_test.go`:

```go
func TestExtractTransferEventData_FromVars(t *testing.T) {
    vm := &VM{
        vars: map[string]any{
            "transfer_monitor": map[string]interface{}{
                "data": map[string]interface{}{
                    "eventName":      "Transfer",
                    "direction":      "sent",
                    "fromAddress":    "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
                    "toAddress":      "0x0000000000000000000000000000000000000002",
                    "value":          "1.5",
                    "tokenSymbol":    "ETH",
                    "tokenName":      "Ether",
                    "blockTimestamp": int64(1768943005000),
                },
            },
            "settings": map[string]interface{}{
                "chain": "base",
            },
        },
        ExecutionLogs: []*avsproto.Execution{}, // Empty - no trigger step
    }

    got := ExtractTransferEventData(vm)
    require.NotNil(t, got)
    assert.Equal(t, "sent", got.Direction)
    assert.Equal(t, "1.5", got.Value)
    assert.Equal(t, "ETH", got.TokenSymbol)
    assert.Equal(t, "Base", got.ChainName)

    msg := FormatTransferMessage(got)
    assert.Contains(t, msg, "1.5 ETH")
    assert.Contains(t, msg, "Sent")
}
```

Keep existing tests unchanged to ensure existing behavior remains covered.

### 4. Context-memory API

- **No change** to when or how the context-memory API is called.
- For **single-node transfer notifications**, the above change is sufficient.
- The context-memory API remains useful for **multi-step workflows** where AI-generated descriptions are desired.

### 5. Documentation

After implementing, optionally update `docs/CONTEXT_MEMORY_API_REFACTOR.md` under "Transfer Notifications" to note that for single-node notification runs, the aggregator also uses `vm.vars["transfer_monitor"].data` when present.

---

## File summary

| File | Action |
|------|--------|
| `core/taskengine/summarizer_deterministic.go` | Add `transferMonitorVarName` constant, `transferEventDataFromMap` helper, and fallback logic in `ExtractTransferEventData` |
| `core/taskengine/summarizer_ai_test.go` | Add `TestExtractTransferEventData_FromVars` |
| `core/taskengine/vm_runner_rest.go` | No change |
| `core/taskengine/summarizer.go` | No change |
| `docs/CONTEXT_MEMORY_API_REFACTOR.md` | Optional: short note after implementation |

---

## Expected outcome

For the same client payload (Telegram node, `summarize: true`, `inputVariables.transfer_monitor` with e.g. 1.5 ETH sent on Base), the Telegram message should change from:

**Before:** `Simulation: Workflow successfully completed / Smart wallet ... completed the 'singleNodeExecution_restApi' node`

**After:** `⬆️ Sent 1.5 ETH to 0x00...02 on Base`
