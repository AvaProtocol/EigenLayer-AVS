# Unified JSON Formatting for Execution Logs

## Overview

This document describes the changes made to unify the formatting of structured data in execution logs across all node types. Previously, execution logs displayed structured data inconsistently:

- **Request bodies**: JSON with HTML-escaped characters (`\u003c`, `\u003e`, `\u0026`)
- **Response headers**: Go map format (`map[Key:[Value]]`)
- **Filter data**: Go's default `%v` formatting

This made logs harder to read for developers, especially when dealing with HTML content, comparisons, and special characters.

## Changes

### New Utility Function: `FormatAsJSON`

Added a unified utility function in `core/taskengine/utils.go`:

```go
func FormatAsJSON(data interface{}) string
```

**Features:**
- Converts any data structure to clean JSON string
- **Disables HTML escaping** for readable output
- Handles all types: strings, numbers, booleans, maps, slices, structs
- Returns compact JSON (no pretty-printing) to keep logs concise
- Falls back gracefully for non-JSON-serializable types

**Key difference from standard `json.Marshal`:**
- Standard: `{"html":"\u003cdiv\u003eContent \u0026 \"quotes\"\u003c/div\u003e"}`
- FormatAsJSON: `{"html":"<div>Content & \"quotes\"</div>"}`

### Updated Files

1. **`core/taskengine/utils.go`**
   - Added `FormatAsJSON()` utility function

2. **`core/taskengine/vm_runner_rest.go`**
   - Line 780: When injecting summaries into SendGrid/Telegram payloads, use `FormatAsJSON(bodyObj)` instead of `json.Marshal()`
   - Line 828: Request body logging now uses `FormatAsJSON(body)`
   - Line 865: Response headers now uses `FormatAsJSON(response.Header())`

3. **`core/taskengine/vm_runner_filter.go`**
   - Line 88: Removed redundant timestamp from log message
   - Line 188: Filter data content now uses `FormatAsJSON(actualDataToFilter)`

4. **`core/taskengine/vm_runner_balance.go`**
   - Line 141: Removed redundant timestamp from log message

5. **`core/taskengine/vm_runner_branch.go`**
   - Line 100: Removed redundant timestamp from log message

6. **`core/taskengine/vm_runner_loop.go`**
   - Lines 50, 422: Removed redundant timestamps from log messages

7. **`core/taskengine/vm_runner_graphql_query.go`**
   - Line 98: Removed redundant timestamp from log message

### Before and After Examples

#### Request Body

**Before:**
```
Request body: {"content":[{"type":"text/plain","value":"Summary"}],"personalizations":[{"dynamic_template_data":{"analysisHtml":"\u003cdiv style=\"font-weight:600\"\u003eWorkflow Summary\u003c/div\u003e\n\u003cp\u003eThe workflow did not fully execute.\u003c/p\u003e"}}]}
```

**After:**
```
Request body: {"content":[{"type":"text/plain","value":"Summary"}],"personalizations":[{"dynamic_template_data":{"analysisHtml":"<div style=\"font-weight:600\">Workflow Summary</div>\n<p>The workflow did not fully execute.</p>"}}]}
```

#### Response Headers

**Before:**
```
Response headers: map[Access-Control-Allow-Headers:[Authorization, Content-Type] Content-Length:[0] Date:[Mon, 03 Nov 2025 17:37:01 GMT] Server:[nginx] X-Message-Id:[DPluXOE9SqqUuFqEee5B4g]]
```

**After:**
```
Response headers: {"Access-Control-Allow-Headers":["Authorization, Content-Type"],"Content-Length":["0"],"Date":["Mon, 03 Nov 2025 17:37:01 GMT"],"Server":["nginx"],"X-Message-Id":["DPluXOE9SqqUuFqEee5B4g"]}
```

#### Filter Data

**Before:**
```
Data to filter type: []interface {}, content: [map[amount:1000 token:0x1234]]
```

**After:**
```
Data to filter type: []interface {}, content: [{"amount":1000,"token":"0x1234"}]
```

### Timestamps Removed from Logs

The `Execution_Step` protobuf already contains `StartAt` and `EndAt` fields (timestamps in milliseconds) that track when each step started and ended. Including timestamps in the log messages themselves was redundant and added visual clutter without providing additional value.

**Removed timestamp patterns:**
- ❌ `"Executing REST API Node ID: %s at %s"` 
- ✅ `"Executing REST API Node ID: %s"`

This makes logs cleaner while preserving precise timing information in the structured `Execution_Step` fields where it can be programmatically accessed.

### Centralized Log Header Format

Created `formatNodeExecutionLogHeader()` function in `node_utils.go` to standardize the first line of execution logs across all node types. This function:

- Uses **node name** instead of node ID for better readability
- Falls back to node ID if name is not available
- Shows both name and ID when both are available for reference
- Provides consistent formatting across all node types

**New format:**
```
Executing NODE_TYPE 'node_name' (node_id)
```

**Examples:**
- `Executing NODE_TYPE_REST_API 'fetch_data' (01k70sm85e929fqhdbh64nnm28)`
- `Executing NODE_TYPE_BRANCH 'check_balance' (01k70b5qkaqecrw4dc44pz8p48)`
- `Executing NODE_TYPE_LOOP 'process_items' (01k70c1xyz123456789abcdef)`

**Applied to all node runners:**
- REST API, Filter, Balance, Branch, Loop, GraphQL Query
- Contract Write, Contract Read, ETH Transfer, Custom Code

### Centralized Config Validation

Created `validateNodeConfig()` function in `node_utils.go` to standardize the nil config check pattern used across all node runners. This function:

- Provides consistent error messages: `"{NodeTypeName} Config is nil"`
- Reduces code duplication (previously ~10 different implementations)
- Simplifies error handling logic in each node runner
- Makes maintenance easier - single place to update validation logic

**Usage:**
```go
if err := validateNodeConfig(node.Config, "NodeTypeName"); err != nil {
    logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
    finalizeExecutionStep(executionLogStep, false, err.Error(), logBuilder.String())
    return executionLogStep, err
}
```

**Before:**
```go
if node.Config == nil {
    err := fmt.Errorf("FilterNode Config is nil")
    logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
    finalizeExecutionStep(executionLogStep, false, err.Error(), logBuilder.String())
    return executionLogStep, err
}
```

**After:**
```go
if err := validateNodeConfig(node.Config, "FilterNode"); err != nil {
    logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
    finalizeExecutionStep(executionLogStep, false, err.Error(), logBuilder.String())
    return executionLogStep, err
}
```

**Applied to all 10 node types:**
- BalanceNode, FilterNode, BranchNode, LoopNode, GraphQLQueryNode
- CustomCodeNode, ETHTransferNode, ContractReadNode, ContractWriteNode, RestAPINode (if applicable)

### Lowercase IDs for Better Readability

All ULID-based IDs (task IDs, execution IDs, node IDs) are now generated in **lowercase** instead of uppercase:

**Before:** `01K70SM85E929FQHDBH64NNM28`  
**After:** `01k70sm85e929fqhdbh64nnm28`

**Why?** Lowercase text is significantly easier to read than all-caps text, especially for long alphanumeric identifiers. This improves developer experience when reading logs, debugging, and copying IDs.

**Implementation:**

Created centralized `model.GenerateID()` function that all ID generation uses:
```go
// model/task.go
func GenerateID() string {
    return strings.ToLower(ulid.Make().String())
}
```

**Where applied:**
- Task IDs - `model.GenerateTaskID()` (uses `GenerateID()`)
- Execution IDs - 4 locations in `engine.go` (all use `model.GenerateID()`)
- Node IDs - `vm.go:CreateNodeFromType()` (uses `model.GenerateID()`)
- Log display - `formatNodeExecutionLogHeader()` converts existing IDs to lowercase

**Benefits:**
- Single source of truth for ID generation
- Easier to maintain and update
- Guaranteed consistency across all ID types
- No duplicate lowercase conversion logic

**Example logs:**
```
Executing NODE_TYPE_REST_API 'fetch_user_data' (01k70sm85e929fqhdbh64nnm28)
```

Much easier to read than:
```
Executing NODE_TYPE_REST_API 'fetch_user_data' (01K70SM85E929FQHDBH64NNM28)
```

## Rationale

### Why No HTML Escaping?

HTML escaping (converting `<`, `>`, `&` to Unicode escape sequences) is only needed when embedding JSON inside HTML documents to prevent XSS attacks. Since our execution logs are:
- Displayed as plain text to developers
- Not embedded in HTML
- Used for debugging and monitoring

...the HTML escaping actually **reduces readability** without providing any security benefit.

### When HTML Escaping IS Needed

HTML escaping should only be done at the **presentation layer** when:
- Embedding JSON in HTML `<script>` tags
- Displaying user-generated content in web pages
- Sending data that will be interpreted by a browser

For server-to-server communication, API responses, and logs, clean JSON is preferred.

## Testing

Added comprehensive tests in:
- `core/taskengine/utils_format_test.go` - Unit tests for `FormatAsJSON()`
- `core/taskengine/format_json_example_test.go` - Before/after comparison examples

Run tests:
```bash
cd /Users/mikasa/Code/EigenLayer-AVS
go test -v ./core/taskengine -run TestFormatAsJSON
```

## Impact

### Benefits
- ✅ More readable logs for developers
- ✅ Consistent JSON formatting across all node types
- ✅ Easier to copy/paste JSON from logs for debugging
- ✅ Better for log analysis tools that parse JSON

### Breaking Changes
- ⚠️ Log format changed - any scripts parsing logs may need updates
- ⚠️ Tests asserting exact log strings need to expect JSON format

### No Impact On
- ✅ API responses to clients (unchanged)
- ✅ Database storage (unchanged)
- ✅ Actual execution behavior (unchanged)
- ✅ Node functionality (unchanged)

## Future Enhancements

Consider applying `FormatAsJSON()` to other areas:
- Contract read/write node logs
- Custom code node logs
- Loop node iteration data
- Any other structured data in execution logs

## See Also

- [Go json.Encoder.SetEscapeHTML documentation](https://pkg.go.dev/encoding/json#Encoder.SetEscapeHTML)
- [Why JSON escapes < and > by default](https://stackoverflow.com/questions/19176024/how-to-escape-special-characters-in-building-a-json-string)

