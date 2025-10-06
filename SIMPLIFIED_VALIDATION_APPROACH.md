# Simplified Validation Approach

## Overview

This document describes the simplified approach to input validation that keeps the protobuf interface unchanged while still leveraging the centralized `ValidateInputByLanguage()` function.

## Key Principles

### 1. **No Protobuf Changes for Node-Specific Fields**
- ❌ Do NOT add `lang` field to FilterNode, BranchNode, or other execution nodes
- ✅ Only ManualTrigger has the `lang` field (already exists in protobuf)
- ✅ CustomCodeNode already has `lang` field (already exists in protobuf)

### 2. **Hardcoded Language Per Node Type**
Each node type knows its own language internally:

```go
// FilterNode - JavaScript (hardcoded)
ValidateInputByLanguage(expression, avsproto.Lang_LANG_JAVASCRIPT)

// BranchNode - JavaScript (hardcoded)
ValidateInputByLanguage(condition.Expression, avsproto.Lang_LANG_JAVASCRIPT)

// ManualTrigger - From config field
ValidateInputByLanguage(data, trigger.Config.Lang)

// CustomCodeNode - From config field
ValidateInputByLanguage(source, node.Config.Lang)
```

### 3. **Centralized Universal Validator**
The `ValidateInputByLanguage()` function in `validation_constants.go` provides:
- **Single source of truth** for all validation logic
- **Extensible** - easy to add new languages (YAML, XML, etc.)
- **Consistent** - all nodes validate the same way
- **DRY** - no duplicate validation code

## Implementation

### FilterNode (`vm_runner_filter.go`)

```go
// LANGUAGE ENFORCEMENT: FilterNode uses JavaScript (hardcoded)
// Using centralized ValidateInputByLanguage for consistency
if err := ValidateInputByLanguage(expression, avsproto.Lang_LANG_JAVASCRIPT); err != nil {
    return executionLogStep, fmt.Errorf("FilterNode expression validation failed: %v", err)
}
```

**Why hardcoded?**
- FilterNode ALWAYS uses JavaScript for expressions
- No need for users to specify what they already know
- Simpler API - fewer fields to configure
- Backward compatible - no protobuf changes

### BranchNode (`vm_runner_branch.go`)

**Validate() method:**
```go
// LANGUAGE ENFORCEMENT: Validate expression using centralized validator
if condition.Type == "if" && strings.TrimSpace(condition.Expression) != "" {
    // BranchNode uses JavaScript for expressions - hardcoded language
    if err := ValidateInputByLanguage(condition.Expression, avsproto.Lang_LANG_JAVASCRIPT); err != nil {
        return fmt.Errorf("condition at index %d expression validation failed: %w", i, err)
    }
}
```

**Execute() method:**
```go
// Regular if condition
// BranchNode uses JavaScript (hardcoded) - validation done in Validate() method
expression := condition.Expression
```

**Why hardcoded?**
- BranchNode conditions ALWAYS use JavaScript
- Consistent with FilterNode approach
- No unnecessary complexity
- Validation happens once in Validate() method

## Benefits of This Approach

### 1. **Minimal API Surface**
- Users don't need to specify obvious things
- Fewer fields = fewer mistakes
- Cleaner protobuf definitions

### 2. **Backward Compatibility**
- ✅ No breaking changes to protobuf
- ✅ Existing tasks continue to work
- ✅ No migration needed

### 3. **Centralized Validation Logic**
```
validation_constants.go
    ↓
ValidateInputByLanguage()
    ↓
├── Lang_LANG_JSON       → ValidateJSONFormat()
├── Lang_LANG_JAVASCRIPT → (runtime validation)
├── Lang_LANG_GRAPHQL    → (runtime validation)
└── Lang_LANG_HANDLEBARS → (runtime validation)
```

### 4. **Easy to Extend**
To add a new language:
1. Add enum value to `Lang` enum in protobuf
2. Add case to `ValidateInputByLanguage()`
3. Implement language-specific validator
4. Done! All nodes get it automatically

### 5. **Consistent Error Messages**
All validation errors use the same structured error format:
```go
NewStructuredError(
    avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
    "language field (lang) is required",
    map[string]interface{}{
        "field": "lang",
        "issue": "LANG_UNSPECIFIED not allowed",
    },
)
```

## Comparison: Before vs After

### Before (Proposed but Rejected)
```protobuf
message FilterNode {
  message Config {
    string expression = 1;
    string input_node_name = 2;
    Lang lang = 3;  // ❌ Users must set this even though it's always JavaScript
  }
}
```

### After (Simplified Approach)
```protobuf
message FilterNode {
  message Config {
    string expression = 1;
    string input_node_name = 2;
    // No lang field - hardcoded in implementation
  }
}
```

```go
// In vm_runner_filter.go - hardcoded language
ValidateInputByLanguage(expression, avsproto.Lang_LANG_JAVASCRIPT)
```

## Nodes with Explicit Lang Field

These nodes DO have `lang` in their protobuf because:
1. **ManualTrigger** - Can be JSON or other formats (user's choice)
2. **CustomCodeNode** - Can be JavaScript, Python, etc. (user's choice)

```protobuf
message ManualTrigger {
  message Config {
    string data = 1;
    map<string, string> headers = 2;
    map<string, string> pathParams = 3;
    Lang lang = 4;  // ✅ Explicit - user chooses format
  }
}

message CustomCodeNode {
  message Config {
    Lang lang = 1;  // ✅ Explicit - user chooses language
    string source = 2;
  }
}
```

## Testing

All tests pass with the simplified approach:
```bash
$ go test -v ./core/taskengine -run "TestBranchNode|TestFilterNode"
PASS: TestBranchNodeCamelCaseResolution
PASS: TestBranchNodePreprocessing
PASS: TestBranchNodeSecurity
PASS: TestBranchNodeSafety
PASS: TestFilterNodePreprocessing
PASS: TestFilterNodePreprocessingEdgeCases
```

## Future Extensibility

If we ever need to support multiple languages for a node type:
1. Add `lang` field to that specific node's Config in protobuf
2. Update node implementation to read `lang` from config
3. Pass that lang to `ValidateInputByLanguage()`
4. Done! The centralized validator already handles it

Example:
```go
// If FilterNode someday supports Handlebars too:
lang := node.Config.Lang  // Read from config
if err := ValidateInputByLanguage(expression, lang); err != nil {
    return err
}
```

## Conclusion

This simplified approach:
- ✅ Keeps protobuf interface stable (no breaking changes)
- ✅ Maintains centralized validation abstraction
- ✅ Reduces API complexity
- ✅ Hardcodes what's obvious (JavaScript for FilterNode/BranchNode)
- ✅ Explicit where needed (ManualTrigger, CustomCodeNode)
- ✅ Easy to extend in the future
