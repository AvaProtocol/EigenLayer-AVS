# Input Validation Implementation Guide

This document consolidates all validation implementation details for PR #408.

## Table of Contents
1. [Overview](#overview)
2. [Architecture](#architecture)
3. [Language-Specific Validation](#language-specific-validation)
4. [Size Limits](#size-limits)
5. [Node Coverage](#node-coverage)
6. [Implementation Examples](#implementation-examples)

## Overview

### Goals
- **DoS Prevention** - Size limits on all user inputs
- **Format Validation** - JSON, JavaScript, GraphQL, Handlebars
- **Centralized Logic** - Single validation function for all nodes
- **Consistent Errors** - Structured error messages

### Key Principle: Hardcoded Languages

**No `lang` field in node configs** (except ManualTrigger, CustomCodeNode).  
Each node type knows its language internally and calls the centralized validator.

```go
// FilterNode - JavaScript (hardcoded)
ValidateInputByLanguage(expression, avsproto.Lang_LANG_JAVASCRIPT)

// GraphQLQueryNode - GraphQL (hardcoded)
ValidateInputByLanguage(query, avsproto.Lang_LANG_GRAPHQL)
```

## Architecture

### Centralized Validation Function

```
validation_constants.go
    ↓
ValidateInputByLanguage(data, lang)
    ↓
├── Lang_LANG_JSON       → ValidateJSONFormat()       (size + format)
├── Lang_LANG_JAVASCRIPT → ValidateJavaScriptFormat() (size: 100KB)
├── Lang_LANG_GRAPHQL    → ValidateGraphQLFormat()    (size: 100KB)
└── Lang_LANG_HANDLEBARS → ValidateHandlebarsFormat() (size: 100KB)
```

### Benefits
- ✅ **DRY** - No duplicate validation code
- ✅ **Extensible** - Add new languages in ONE place
- ✅ **Consistent** - Same limits everywhere
- ✅ **Testable** - Test once, applies to all nodes

## Language-Specific Validation

### 1. JSON (`Lang_LANG_JSON`)

**Validation**: Size limit + JSON format parsing  
**Max Size**: 1 MB  
**Used By**: ManualTrigger data field

```go
func ValidateJSONFormat(data interface{}) error {
    if dataStr, ok := data.(string); ok {
        // Size check first (before parsing)
        if len(dataStr) > MaxManualTriggerDataSize {
            return NewStructuredError(...)
        }
        // Format validation
        var jsonTest interface{}
        if err := json.Unmarshal([]byte(dataStr), &jsonTest); err != nil {
            return NewStructuredError(...)
        }
    }
    return nil
}
```

### 2. JavaScript (`Lang_LANG_JAVASCRIPT`)

**Validation**: Size limit (syntax checked at runtime)  
**Max Size**: 100 KB  
**Used By**: CustomCodeNode, FilterNode, BranchNode

```go
func ValidateJavaScriptFormat(data interface{}) error {
    if dataStr, ok := data.(string); ok {
        if len(dataStr) > MaxCustomCodeSourceSize {
            return NewStructuredError(
                ErrorCode_INVALID_NODE_CONFIG,
                "JavaScript code exceeds maximum size limit",
                map[string]interface{}{
                    "size":     len(dataStr),
                    "maxSize":  102400,
                    "language": "JavaScript",
                },
            )
        }
    }
    return nil
}
```

**Coverage**:
- ✅ **CustomCodeNode.Config.Source** - Full programs
- ✅ **FilterNode.Config.Expression** - Filter expressions
- ✅ **BranchNode.Condition.Expression** - Conditions

### 3. GraphQL (`Lang_LANG_GRAPHQL`)

**Validation**: Size limit (syntax checked at runtime)  
**Max Size**: 100 KB  
**Used By**: GraphQLQueryNode

```go
func ValidateGraphQLFormat(data interface{}) error {
    if dataStr, ok := data.(string); ok {
        if len(dataStr) > MaxCustomCodeSourceSize {
            return NewStructuredError(...)
        }
    }
    return nil
}
```

### 4. Handlebars (`Lang_LANG_HANDLEBARS`)

**Validation**: Size limit (syntax checked at runtime)  
**Max Size**: 100 KB  
**Used By**: ContractReadNode, ContractWriteNode, EmailNode, etc. (method_params)

```go
func ValidateHandlebarsFormat(data interface{}) error {
    if dataStr, ok := data.(string); ok {
        if len(dataStr) > MaxCustomCodeSourceSize {
            return NewStructuredError(...)
        }
    }
    return nil
}
```

**Note**: Handlebars validation infrastructure is ready, but enforcement in nodes is tracked in issue #409.

## Size Limits

### Constants (`validation_constants.go`)

```go
const (
    MaxManualTriggerDataSize   = 1024 * 1024      // 1MB  - Manual trigger data
    MaxRestAPIBodySize         = 10 * 1024 * 1024 // 10MB - REST API bodies
    MaxCustomCodeSourceSize    = 100 * 1024       // 100KB - JS/GraphQL/Handlebars
    MaxContractABISize         = 1024 * 1024      // 1MB  - Contract ABI total
    MaxEventTriggerABIItemSize = 100 * 1024       // 100KB - ABI item
)
```

### Rationale

**100KB Limit** is generous for:
- ✅ Complex filter expressions (50-500 bytes typical)
- ✅ Complex branch conditions (1-10 KB typical)
- ✅ Substantial custom code (10-50 KB typical)
- ❌ Prevents abuse (multi-megabyte code bombs)

## Node Coverage

### Nodes with Language Enforcement

| Node | Language | Field | Validation | Status |
|------|----------|-------|------------|--------|
| **ManualTrigger** | JSON | `Config.Data` | Size + Format | ✅ Done |
| **CustomCodeNode** | JavaScript | `Config.Source` | Size (100KB) | ✅ Done |
| **FilterNode** | JavaScript | `Config.Expression` | Size (100KB) | ✅ Done |
| **BranchNode** | JavaScript | `Condition.Expression` | Size (100KB) | ✅ Done |
| **GraphQLQueryNode** | GraphQL | `Config.Query` | Size (100KB) | ✅ Done |
| **ContractReadNode** | Handlebars | `MethodCall.MethodParams` | Ready | 🔜 Issue #409 |
| **ContractWriteNode** | Handlebars | `MethodCall.MethodParams` | Ready | 🔜 Issue #409 |
| **EmailNode** | Handlebars | Template fields | Ready | 🔜 Issue #409 |

## Implementation Examples

### Example 1: FilterNode (JavaScript)

**Before PR #408**: No validation

**After PR #408**:
```go
// vm_runner_filter.go
func (r *FilterProcessor) Execute(stepID string, node *avsproto.FilterNode) (*avsproto.Execution_Step, error) {
    expression := node.Config.Expression
    
    // LANGUAGE ENFORCEMENT: FilterNode uses JavaScript (hardcoded)
    if err := ValidateInputByLanguage(expression, avsproto.Lang_LANG_JAVASCRIPT); err != nil {
        return executionLogStep, fmt.Errorf("FilterNode expression validation failed: %v", err)
    }
    
    // ... rest of execution
}
```

### Example 2: BranchNode (JavaScript)

**Before PR #408**: No validation

**After PR #408**:
```go
// vm_runner_branch.go
func (r *BranchProcessor) Validate(node *avsproto.BranchNode) error {
    for i, condition := range node.Config.Conditions {
        if condition.Type == "if" && strings.TrimSpace(condition.Expression) != "" {
            // LANGUAGE ENFORCEMENT: BranchNode uses JavaScript (hardcoded)
            if err := ValidateInputByLanguage(condition.Expression, avsproto.Lang_LANG_JAVASCRIPT); err != nil {
                return fmt.Errorf("condition at index %d expression validation failed: %w", i, err)
            }
        }
    }
    return nil
}
```

### Example 3: GraphQLQueryNode (GraphQL)

**Before PR #408**: No validation

**After PR #408**:
```go
// vm_runner_graphql_query.go
func (r *GraphqlQueryProcessor) Execute(stepID string, node *avsproto.GraphQLQueryNode) (*avsproto.Execution_Step, any, error) {
    queryStr := node.Config.Query
    
    // LANGUAGE ENFORCEMENT: GraphQLQueryNode uses GraphQL (hardcoded)
    if err = ValidateInputByLanguage(queryStr, avsproto.Lang_LANG_GRAPHQL); err != nil {
        return step, nil, err
    }
    
    // ... rest of execution
}
```

### Example 4: ManualTrigger (JSON - from config)

**Special case**: Language comes from protobuf config (user-specified)

```go
// validation_constants.go
func ValidateManualTriggerFromProtobuf(manualOutput *avsproto.ManualTrigger_Output, task *model.Task) error {
    // Extract language from trigger config
    lang := task.Trigger.GetManual().Config.Lang
    
    // Validate based on language (user's choice: JSON, JavaScript, etc.)
    return ValidateInputByLanguage(data, lang)
}
```

## Error Handling

### Structured Error Format

All validation errors use consistent format:

```go
type StructuredError struct {
    Code    ErrorCode              // e.g., INVALID_NODE_CONFIG
    Message string                 // Human-readable message
    Details map[string]interface{} // Machine-readable details
}
```

**Example**:
```go
StructuredError{
    Code: ErrorCode_INVALID_NODE_CONFIG,
    Message: "JavaScript code exceeds maximum size limit: 150000 bytes (max: 102400 bytes)",
    Details: {
        "field":    "expression",
        "issue":    "size limit exceeded",
        "size":     150000,
        "maxSize":  102400,
        "language": "JavaScript",
    }
}
```

## Testing

### Test Coverage

```bash
# Size limit tests
go test ./core/taskengine -run TestCustomCode_SizeLimit
go test ./core/taskengine -run TestValidationConstants

# Node-specific tests
go test ./core/taskengine -run TestFilterNode
go test ./core/taskengine -run TestBranchNode
go test ./core/taskengine -run TestGraphQL
```

### Test Scenarios
1. ✅ Valid size - Under 100KB → Success
2. ✅ Exceeds limit - Over 100KB → Structured error
3. ✅ Nil/empty - Empty input → Allowed (caught by other validation)
4. ✅ JSON format - Valid JSON → Success
5. ✅ JSON format - Invalid JSON → Format error

## Migration & Backward Compatibility

### No Breaking Changes
- ✅ Protobuf interface unchanged (no new fields in most nodes)
- ✅ Existing valid code continues to work
- ✅ Only extremely large inputs (>100KB) rejected

### Potential Impact
Only users with **extremely large** code/queries (>100KB) will see errors:
- Typical: 50-500 bytes
- Complex: 1-10 KB
- Very complex: 10-50 KB
- **Blocked**: >100KB (likely malicious or mistake)

## Future Enhancements

### Easy to Add
Since validation is centralized, new features benefit ALL nodes automatically:

1. **Syntax Validation**
```go
func ValidateJavaScriptFormat(data interface{}) error {
    // ... existing size check ...
    
    // NEW: Add syntax validation
    if err := parseJavaScript(dataStr); err != nil {
        return NewStructuredError(...)
    }
    return nil
}
```

2. **Complexity Limits**
   - Nesting depth
   - Loop count
   - Function count

3. **AST Analysis**
   - Detect dangerous patterns
   - Block specific APIs

4. **Rate Limiting**
   - Per-user execution limits

## Summary

### What Was Implemented

✅ **Centralized validation architecture**  
✅ **JavaScript size validation** (CustomCodeNode, FilterNode, BranchNode)  
✅ **GraphQL size validation** (GraphQLQueryNode)  
✅ **Handlebars validation function** (ready for use)  
✅ **JSON format + size validation** (ManualTrigger)  
✅ **Consistent structured errors**  

### Next Steps

🔜 **Issue #409**: Add Handlebars validation to remaining nodes  
🔜 **Syntax validation**: Pre-validate code syntax  
🔜 **Complexity limits**: Prevent overly complex code  

### Key Takeaway

All language validation is now **centralized, consistent, and extensible**. Adding new validations or languages requires changes in **one place only**.
