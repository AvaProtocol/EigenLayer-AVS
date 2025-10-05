# Language Field Implementation Plan

## Problem Statement

Currently, the backend validates **all string data** in ManualTrigger as JSON, but there's no explicit field indicating the data format/language. This is inconsistent with CustomCodeNode which has a `lang` field to specify the code language.

**The core issue**: Validation logic is scattered and implicit. We need a **centralized, universal validation method** that:
1. Works for **all nodes** that use code editors
2. Validates based on the **explicit `lang` field**
3. **Eliminates duplicate validation code** across different nodes
4. Makes validation **language-aware** rather than node-aware

## Current State Analysis

### Backend (Protobuf Definitions)

#### CustomCodeNode (Has Language Field) ✅
```protobuf
enum Lang {
  JavaScript = 0;
}

message CustomCodeNode {
  message Config {
    Lang lang = 1;  // Language explicitly specified
    string source = 2;
  }
}
```

#### Other Nodes (Missing Language Field) ❌
```protobuf
message ManualTrigger {
  message Config {
    google.protobuf.Value data = 1;
    map<string, string> headers = 2;
    map<string, string> pathParams = 3;
    // MISSING: No lang field
  }
}

message FilterNode {
  message Config {
    string expression = 1;
    string input_node_name = 2;
    // MISSING: No lang field
  }
}

message BranchNode {
  message Condition {
    string id = 1;
    string type = 2;
    string expression = 3;
    // MISSING: No lang field
  }
}
```

### Frontend (CodeEditor Usage)

Analysis of all nodes using CodeEditor:

| Node | Language | Editable | Needs Lang Field? | Current Validation |
|------|----------|----------|-------------------|-------------------|
| **ManualTriggerNode** | `json` (hardcoded) | ✅ Yes | **YES** | Validates all strings as JSON |
| **CustomCodeNode** | `{selectedLanguage}` (dynamic) | ✅ Yes | ✅ Has it | Validates size only |
| **FilterNode** | `javascript` | ✅ Yes | **YES** | Unknown |
| **SubgraphNode** | `graphql` (hardcoded) | ✅ Yes | **YES** | Unknown |
| **BranchNode** | `handlebars` | ✅ Yes | **YES** | Expressions need validation |
| **LoopNode** | `json` (hardcoded) | ❌ Read-only | NO | N/A |
| **ContractWriteNode** | `handlebars` | ✅ Yes | Maybe | Uses templates |
| **ContractReadNode** | `handlebars` | ✅ Yes | Maybe | Uses templates |
| **EmailNode** | `handlebars` | ✅ Yes | Maybe | Uses templates |
| **TelegramNode** | `handlebars` | ✅ Yes | Maybe | Uses templates |

### Key Findings

1. **ManualTriggerNode**: Hardcodes `language="json"` in frontend, but backend validates **all string data** as JSON without checking a language field
2. **CustomCodeNode**: Has proper `lang` field in protobuf and uses it correctly ✅
3. **FilterNode**: Uses `javascript` for expression - needs `lang` field
4. **SubgraphNode**: Uses `graphql` - needs `lang` field
5. **BranchNode**: Uses `handlebars` for expressions - needs `lang` field per condition
6. **Template nodes** (handlebars in other nodes): May not need language field since handlebars is validated differently

## Problems with Current Approach

### Issue 1: No Explicit Format Declaration
- Backend assumes string = JSON for ManualTrigger
- No way for client to specify "this is plain text" vs "this is JSON"
- Creates tight coupling between frontend and backend assumptions

### Issue 2: Inconsistent with CustomCodeNode Pattern
- CustomCodeNode explicitly declares its language with `lang` field
- Other nodes should follow the same naming convention

### Issue 3: Future Extensibility
- What if users want to send XML, YAML, or plain text in the future?
- No mechanism to support multiple formats

### Issue 4: Validation Logic Ambiguity
- Current code validates **only if data is a string**
- Should validation depend on explicit language field

## Proposed Solution

### Phase 1: Add Language Field to Relevant Nodes

#### 1. Update Protobuf Definitions

**Key Decision: Use `lang` consistently (not `dataLang`, `queryLang`, etc.) to match CustomCodeNode**

```protobuf
// Expand Lang enum to support more formats
enum Lang {
  JavaScript = 0;
  JSON = 1;
  GraphQL = 2;
  Handlebars = 3;
  // Future: YAML = 4, XML = 5, PlainText = 6
}

message ManualTrigger {
  message Config {
    google.protobuf.Value data = 1;
    map<string, string> headers = 2;
    map<string, string> pathParams = 3;
    Lang lang = 4;  // NEW: Same field name as CustomCodeNode (default: JSON)
  }
}

message SubgraphNode {
  message Config {
    string query = 1;
    Lang lang = 2;  // NEW: Same field name as CustomCodeNode (default: GraphQL)
    // ... other existing fields
  }
}

message FilterNode {
  message Config {
    string expression = 1;
    string input_node_name = 2;
    Lang lang = 3;  // NEW: Same field name as CustomCodeNode (default: JavaScript)
  }
}

message BranchNode {
  message Condition {
    string id = 1;
    string type = 2;
    string expression = 3;
    Lang lang = 4;  // NEW: Same field name as CustomCodeNode (default: Handlebars)
  }
  
  message Config {
    repeated Condition conditions = 1;
  }
}
```

#### 2. Create Centralized Validation Logic

**File**: `core/taskengine/validation_constants.go`

**This is the KEY innovation**: One universal function that ALL nodes can use!

```go
// ValidateInputByLanguage is the UNIVERSAL validation function for all nodes.
// Instead of each node (ManualTrigger, FilterNode, BranchNode, etc.) implementing
// their own validation logic, they ALL call this single function with their lang field.
//
// Benefits:
// - DRY: No duplicate validation code across nodes
// - Extensible: Add new languages in ONE place
// - Consistent: All nodes validate the same way
// - Testable: Test validation logic once, not per-node
func ValidateInputByLanguage(data interface{}, lang avsproto.Lang) error {
    switch lang {
    case avsproto.Lang_JSON:
        return ValidateJSONFormat(data)
    case avsproto.Lang_JavaScript:
        return ValidateJavaScriptSyntax(data)
    case avsproto.Lang_GraphQL:
        return ValidateGraphQLSyntax(data)
    case avsproto.Lang_Handlebars:
        return ValidateHandlebarsSyntax(data)
    default:
        // For unknown/unspecified languages, no validation
        return nil
    }
}

// ValidateJSONFormat validates JSON string format (extracted from current ValidateManualTriggerData)
func ValidateJSONFormat(data interface{}) error {
    // Only validate if data is a string
    if dataStr, ok := data.(string); ok {
        var jsonTest interface{}
        if err := json.Unmarshal([]byte(dataStr), &jsonTest); err != nil {
            return NewStructuredError(
                avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
                fmt.Sprintf("Data must be valid JSON: %s", err.Error()),
                map[string]interface{}{
                    "field": "data",
                    "issue": "invalid JSON format",
                    "error": err.Error(),
                },
            )
        }
    }
    return nil
}
```

#### 3. Update All Nodes to Use Centralized Validation

**The pattern is identical across ALL nodes** - extract `lang` field, call `ValidateInputByLanguage()`:

**ManualTrigger** (`run_node_immediately.go`):
```go
func (n *Engine) runManualTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
    data, exists := triggerConfig["data"]
    if !exists || data == nil {
        return nil, NewStructuredError(...)
    }

    // Get language from config (default to JSON for backward compatibility)
    // Use "lang" for consistency with CustomCodeNode
    lang := avsproto.Lang_JSON
    if langInterface, ok := triggerConfig["lang"]; ok {
        if langInt, ok := langInterface.(int); ok {
            lang = avsproto.Lang(langInt)
        }
    }

    // Validate based on language
    if err := ValidateInputByLanguage(data, lang); err != nil {
        return nil, err
    }
    
    // ... rest of the function
}
```

**TriggerTask** (`engine.go`):
```go
// In TriggerTask function
if triggerData.Type == avsproto.TriggerType_TRIGGER_TYPE_MANUAL {
    if manualOutput, ok := triggerData.Output.(*avsproto.ManualTrigger_Output); ok && manualOutput != nil {
        if manualOutput.Data != nil {
            var data interface{}
            if stringVal := manualOutput.Data.GetStringValue(); stringVal != "" {
                data = stringVal
            } else {
                data = manualOutput.Data.AsInterface()
            }
            
            // Get language from trigger config
            // Use "Lang" field for consistency with CustomCodeNode
            lang := avsproto.Lang_JSON // Default
            if task.Trigger.GetManual() != nil && task.Trigger.GetManual().Config != nil {
                lang = task.Trigger.GetManual().Config.Lang
            }
            
            if err := ValidateInputByLanguage(data, lang); err != nil {
                return nil, err
            }
        }
    }
}
```

**SimulateTask** (`engine.go`):
```go
// In SimulateTask function before runTriggerImmediately
if triggerType == avsproto.TriggerType_TRIGGER_TYPE_MANUAL {
    if data, exists := triggerConfig["data"]; exists && data != nil {
        // Get language from config
        // Use "lang" for consistency with CustomCodeNode
        lang := avsproto.Lang_JSON
        if langInterface, ok := triggerConfig["lang"]; ok {
            if langEnum, ok := langInterface.(avsproto.Lang); ok {
                lang = langEnum
            }
        }
        
        if err := ValidateInputByLanguage(data, lang); err != nil {
            return nil, err
        }
    }
}
```

#### 4. Update Frontend

**ManualTriggerNode.tsx**:
```typescript
// Use "lang" for consistency with CustomCodeNode
const lang = "json"; // Fixed to JSON, could be a dropdown in future

// When saving:
updateNodeDataCore({
  data: jsonData,
  lang: "JSON", // Same field name as CustomCodeNode
  headers: headers,
  pathParams: pathParams,
});
```

### Phase 2: Backward Compatibility

#### Default Behavior
- If `lang` field is missing/unspecified, default to appropriate language for each node:
  - ManualTrigger: default to `JSON`
  - CustomCodeNode: already has `JavaScript`
  - SubgraphNode: default to `GraphQL`
  - FilterNode: default to `JavaScript`
  - BranchNode expressions: default to `Handlebars`
- This ensures existing workflows continue to work

#### Migration Strategy
1. Add new fields as optional in protobuf
2. Backend defaults to appropriate language validation when field is missing
3. Frontend can be updated gradually
4. No breaking changes to existing workflows

### Phase 3: Extended Format Support (Future)

Could add support for:
- **YAML**: For configuration files
- **XML**: For legacy system integration
- **PlainText**: For free-form text (no validation)
- **Markdown**: For documentation/notes

## Implementation Checklist

### Backend Changes
- [ ] Update `protobuf/avs.proto` to add `Lang` enum values (JSON, GraphQL, Handlebars)
- [ ] Add `lang` field to `ManualTrigger.Config` (consistent with CustomCodeNode)
- [ ] Add `lang` field to `SubgraphNode.Config`
- [ ] Add `lang` field to `FilterNode.Config`
- [ ] Add `lang` field to `BranchNode.Condition`
- [ ] Regenerate protobuf Go files
- [ ] Create `ValidateInputByLanguage()` function
- [ ] Extract `ValidateJSONFormat()` from current validation
- [ ] Update `runManualTriggerImmediately()` to use language-aware validation
- [ ] Update `TriggerTask()` to validate with language
- [ ] Update `SimulateTask()` to validate with language
- [ ] Update all three validation paths consistently

### Testing
- [ ] Update existing tests to include `lang` field
- [ ] Add tests for explicit language specification
- [ ] Add tests for backward compatibility (missing language field)
- [ ] Add tests for future languages (when implemented)
- [ ] Test all three execution paths: RunNodeImmediately, TriggerTask, SimulateTask

### Frontend Changes (Separate PR/Issue)
- [ ] Update ManualTriggerNode to send `lang` field (consistent with CustomCodeNode)
- [ ] Update SubgraphNode to send `lang` field
- [ ] Update FilterNode to send `lang` field
- [ ] Update BranchNode to send `lang` field for each condition
- [ ] Consider UI for language selection (future enhancement)

### Documentation
- [ ] Update INPUT_VALIDATION_AUDIT.md
- [ ] Update VALIDATION_ENHANCEMENTS_SUMMARY.md
- [ ] Document the new language field behavior
- [ ] Document backward compatibility guarantees

## Decision Points

### Question 1: Should language be required or optional?
**Recommendation**: Optional with intelligent default
- Default to JSON for ManualTrigger (backward compatible)
- Default to JavaScript for CustomCode
- Default to GraphQL for SubgraphNode
- Default to JavaScript for FilterNode
- Default to Handlebars for BranchNode

### Question 2: Should we use same field name across all nodes?
**Answer**: ✅ YES - use `lang` consistently (like CustomCodeNode), not `dataLang`, `queryLang`, etc.

### Question 3: Should we validate plain text / unknown languages?
**Recommendation**: No validation for unspecified languages
- If language is explicitly "PlainText" or unknown, skip validation
- Allows flexibility for future formats

### Question 4: Should frontend be updated in same PR?
**Recommendation**: Backend first, frontend second
- Phase 1: Backend supports language field (optional, defaults intelligently)
- Phase 2: Frontend sends language field explicitly
- Allows testing and gradual rollout

## Benefits of This Approach

### 🎯 **PRIMARY BENEFIT: Centralized Universal Validation**
**One function to rule them all!** Instead of:
```go
// ❌ OLD WAY: Each node has its own validation
if nodeType == "manualTrigger" {
    validateJSON(data)
} else if nodeType == "filter" {
    validateJavaScript(expression)
} else if nodeType == "branch" {
    validateHandlebars(condition)
}
```

We now have:
```go
// ✅ NEW WAY: One universal validator for all nodes
ValidateInputByLanguage(data, lang)
```

### 🏆 **All Benefits:**

1. **🔄 DRY Principle**: No duplicate validation code - write once, use everywhere
2. **🎯 Centralized Logic**: All language validators in one place (`validation_constants.go`)
3. **📝 Explicit over Implicit**: Language/format is explicitly declared via `lang` field
4. **🔧 Consistent Naming**: Uses `lang` everywhere, just like CustomCodeNode
5. **♻️ Backward Compatible**: Existing workflows continue working with intelligent defaults
6. **🚀 Future-Proof**: Add new languages (YAML, XML) in ONE place, all nodes benefit
7. **✅ Easy to Test**: Test validation logic once, not per-node
8. **📊 Clear Validation Rules**: Validation based on declared language, not node type
9. **💬 Better Error Messages**: Language-specific error messages
10. **🎨 Frontend Flexibility**: Frontend can offer language selection in the future

## Risks and Mitigation

### Risk 1: Breaking Changes
**Mitigation**: Make all new fields optional with intelligent defaults

### Risk 2: Frontend-Backend Mismatch
**Mitigation**: Careful rollout, backend accepts both old and new formats

### Risk 3: Validation Performance
**Mitigation**: Validation only when language requires it, cache validators

## Summary Table

### **Nodes That Need Language Fields:**

| Node | Current Language | Needs Lang Field | Field Name | Default Value |
|------|-----------------|------------------|------------|---------------|
| ManualTriggerNode | `json` (hardcoded) | **YES** | `lang` | `JSON` |
| SubgraphNode | `graphql` (hardcoded) | **YES** | `lang` | `GraphQL` |
| FilterNode | `javascript` | **YES** | `lang` | `JavaScript` |
| BranchNode | `handlebars` | **YES** | `lang` (per condition) | `Handlebars` |
| CustomCodeNode | dynamic | ✅ Already has | `lang` | `JavaScript` |

## Conclusion

The current approach of implicitly validating all string data as JSON is:
- ✅ Simple and works for current use case
- ❌ Not explicit or extensible
- ❌ Inconsistent with CustomCodeNode pattern
- ❌ Doesn't allow for future format support

**Recommendation**: Implement `lang` field consistently across all nodes following CustomCodeNode pattern, with backward compatibility through intelligent defaults.
