# JavaScript Size Validation - Generalized Approach

## Problem Statement

Originally, only `CustomCodeNode` had size validation for JavaScript code (100KB limit). However, other nodes also use JavaScript:
- **FilterNode** - JavaScript expressions for filtering data
- **BranchNode** - JavaScript expressions for conditional branching

Without size limits on these nodes, they were potential DoS vectors where users could submit extremely large JavaScript expressions to overwhelm the system.

## Solution: Generalized JavaScript Validation

Instead of adding duplicate size checks to each node, we **generalized the validation** in the centralized `ValidateInputByLanguage()` function.

### Implementation

#### Before (Node-Specific)
```go
// In vm_runner_customcode.go - ONLY CustomCodeNode had size check
if len(sourceStr) > MaxCustomCodeSourceSize {
    return NewStructuredError(
        avsproto.ErrorCode_INVALID_NODE_CONFIG,
        "Custom code source exceeds maximum size limit",
        // ... error details
    )
}
```

**Problem**: FilterNode and BranchNode had NO size limits!

#### After (Centralized)
```go
// In validation_constants.go - ALL JavaScript is validated
func ValidateJavaScriptFormat(data interface{}) error {
    if dataStr, ok := data.(string); ok {
        if len(dataStr) > MaxCustomCodeSourceSize {
            return NewStructuredError(
                avsproto.ErrorCode_INVALID_NODE_CONFIG,
                "JavaScript code exceeds maximum size limit: ... bytes",
                // ... error details with language: "JavaScript"
            )
        }
    }
    return nil
}
```

**Called by:**
```go
// CustomCodeNode
ValidateInputByLanguage(sourceStr, avsproto.Lang_LANG_JAVASCRIPT)

// FilterNode
ValidateInputByLanguage(expression, avsproto.Lang_LANG_JAVASCRIPT)

// BranchNode (in Validate method)
ValidateInputByLanguage(condition.Expression, avsproto.Lang_LANG_JAVASCRIPT)
```

## Coverage

### Nodes Protected
| Node | Field | Max Size | Before | After |
|------|-------|----------|--------|-------|
| CustomCodeNode | `Config.Source` | 100KB | ✅ Protected | ✅ Protected |
| FilterNode | `Config.Expression` | 100KB | ❌ No limit | ✅ Protected |
| BranchNode | `Condition.Expression` | 100KB | ❌ No limit | ✅ Protected |

### Security Benefits
1. **DoS Prevention** - All JavaScript code has size limits
2. **Consistency** - Same 100KB limit everywhere
3. **Centralized** - One place to update limits
4. **Extensible** - Easy to add more JavaScript-using nodes

## Size Limit Rationale

**100KB (102,400 bytes)** is generous for:
- ✅ Complex filter expressions (`value.age > 18 && value.status === 'active'`)
- ✅ Complex branch conditions (multi-line JavaScript with logic)
- ✅ Substantial custom code programs (most use cases)
- ❌ Prevents abuse (multi-megabyte JavaScript bombs)

## Error Handling

### Structured Error Format
```go
StructuredError{
    Code: ErrorCode_INVALID_NODE_CONFIG,
    Message: "JavaScript code exceeds maximum size limit: 150000 bytes (max: 102400 bytes)",
    Details: {
        "field": "source/expression",
        "issue": "size limit exceeded",
        "size": 150000,
        "maxSize": 102400,
        "language": "JavaScript",
    }
}
```

### User-Facing Error Messages
- **CustomCodeNode**: "JavaScript code exceeds maximum size limit: X bytes (max: 100KB)"
- **FilterNode**: "FilterNode expression validation failed: JavaScript code exceeds maximum size limit"
- **BranchNode**: "condition at index 0 expression validation failed: JavaScript code exceeds maximum size limit"

## Testing

### Test Coverage
```bash
$ go test -v ./core/taskengine -run "TestCustomCode_SizeLimit"
PASS: TestCustomCode_SizeLimit/Valid_size_-_under_limit
PASS: TestCustomCode_SizeLimit/Invalid_size_-_exceeds_limit
```

### Test Scenarios
1. ✅ **Valid size** - JavaScript under 100KB → Success
2. ✅ **Exceeds limit** - JavaScript over 100KB → Error with structured details
3. ✅ **Nil/empty** - Empty expressions → Allowed (caught by other validation)

## Migration Impact

### Backward Compatibility
- ✅ **No breaking changes** - Existing valid code continues to work
- ✅ **Protobuf unchanged** - No API changes
- ✅ **Transparent** - Users with reasonable code sizes unaffected

### Potential Impact
Only users submitting **extremely large** JavaScript expressions (>100KB) would see errors:
- Typical expressions: 50-500 bytes
- Complex expressions: 1-10 KB
- Very complex: 10-50 KB
- **Blocked**: >100KB (likely malicious or mistake)

## Code Organization

### Files Modified
1. **`validation_constants.go`**
   - Added `ValidateJavaScriptFormat()` function
   - Updated `ValidateInputByLanguage()` to call it

2. **`vm_runner_customcode.go`**
   - Removed duplicate size check
   - Now calls `ValidateInputByLanguage()`

3. **`vm_runner_filter.go`**
   - Added `ValidateInputByLanguage()` call (new protection)

4. **`vm_runner_branch.go`**
   - Added `ValidateInputByLanguage()` call in Validate() (new protection)

5. **`validation_size_limits_test.go`**
   - Updated test expectations for centralized error messages

## Future Enhancements

### Possible Additions
1. **Syntax Validation** - Pre-validate JavaScript syntax (currently runtime only)
2. **Complexity Limits** - Limit nesting depth, loop count, etc.
3. **AST Analysis** - Detect dangerous patterns before execution
4. **Rate Limiting** - Per-user limits on JavaScript execution

### Easy to Extend
```go
// Add new validation in ONE place:
func ValidateJavaScriptFormat(data interface{}) error {
    // ... existing size check ...
    
    // NEW: Add syntax validation
    if err := validateJavaScriptSyntax(dataStr); err != nil {
        return NewStructuredError(...)
    }
    
    return nil
}
// All nodes automatically get the new validation!
```

## Conclusion

By **generalizing JavaScript size validation** into the centralized `ValidateInputByLanguage()` function:

✅ **Security** - All JavaScript code protected from DoS  
✅ **Consistency** - Same limits everywhere  
✅ **Maintainability** - One place to update  
✅ **Extensibility** - Easy to add more validations  
✅ **Backward Compatible** - No breaking changes

This follows the **DRY principle** and ensures **comprehensive security coverage** across the entire codebase.
