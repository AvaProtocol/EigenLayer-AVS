# Complete Audit: Nodes Using CodeEditor

## Summary

**19 nodes use CodeEditor** - here's the complete status:

| # | Node | Language | Editable | Backend Exists? | Has `lang` Field? | Needs Addition? | Priority |
|---|------|----------|----------|----------------|-------------------|-----------------|----------|
| 1 | **ManualTriggerNode** | `json` | ✅ Yes | ✅ Yes | ❌ NO | **🔴 YES** | **HIGH** |
| 2 | **CustomCodeNode** | dynamic | ✅ Yes | ✅ Yes | ✅ YES | ✅ Done | N/A |
| 3 | **FilterNode** | `javascript` | ✅ Yes | ✅ Yes | ❌ NO | **🔴 YES** | **HIGH** |
| 4 | **SubgraphNode** | `graphql` | ✅ Yes | ✅ Yes | ❌ NO | **🔴 YES** | **HIGH** |
| 5 | **BranchNode** | `handlebars` | ✅ Yes | ✅ Yes | ❌ NO | **🔴 YES** | **HIGH** |
| 6 | **ContractReadNode** | `handlebars` | ✅ Yes | ✅ Yes | ❌ NO | **🟡 MAYBE** | MEDIUM |
| 7 | **ContractWriteNode** | `handlebars` | ✅ Yes | ✅ Yes | ❌ NO | **🟡 MAYBE** | MEDIUM |
| 8 | **EmailNode** | `handlebars` | ✅ Yes | ✅ Yes | ❌ NO | **🟡 MAYBE** | MEDIUM |
| 9 | **TelegramNode** | `handlebars` | ✅ Yes | ✅ Yes | ❌ NO | **🟡 MAYBE** | MEDIUM |
| 10 | **LoopNode** | `json` | ❌ Read-only | ✅ Yes | ❌ NO | 🟢 NO | LOW |
| 11 | **LoopNodeContractRead** | `handlebars` | ✅ Yes | ✅ Yes | ❌ NO | **🟡 MAYBE** | MEDIUM |
| 12 | **LoopNodeContractWrite** | `handlebars` | ✅ Yes | ✅ Yes | ❌ NO | **🟡 MAYBE** | MEDIUM |
| 13 | **LoopNodeCustomCode** | `javascript` | ✅ Yes | ✅ Yes | ❌ NO | **🟡 MAYBE** | MEDIUM |
| 14 | **LoopNodeRestApi** | `handlebars` | ✅ Yes | ✅ Yes | ❌ NO | **🟡 MAYBE** | MEDIUM |

## Detailed Analysis

### 🔴 HIGH PRIORITY (Must Add `lang` Field)

These nodes have **user-editable code/data** and should definitely have `lang` field:

#### 1. ManualTriggerNode ✅ Already in Plan
- **Language**: JSON
- **Why**: User provides JSON test data
- **Current Validation**: Validates all string data as JSON (implicit)
- **Status**: ✅ In implementation plan

#### 2. FilterNode ✅ Already in Plan
- **Language**: JavaScript (for expression)
- **Why**: User writes JavaScript expression to filter arrays
- **Current Validation**: Unknown
- **Status**: ✅ In implementation plan
- **Note**: Also has JSON CodeEditor for variables (read-only)

#### 3. SubgraphNode ✅ Already in Plan
- **Language**: GraphQL
- **Why**: User writes GraphQL queries
- **Current Validation**: Unknown
- **Status**: ✅ In implementation plan

#### 4. BranchNode ✅ Already in Plan
- **Language**: Handlebars
- **Why**: User writes conditional expressions
- **Current Validation**: Unknown
- **Status**: ✅ In implementation plan
- **Note**: Each condition has an expression

### 🟡 MEDIUM PRIORITY (Template Nodes - Consider Adding)

These nodes use **Handlebars templates**. Question: Should template syntax be validated?

#### 5. ContractReadNode
- **Language**: Handlebars
- **Usage**: Template variables for contract method parameters
- **Backend**: `ContractReadNode` message exists
- **Decision Needed**: Does Handlebars need syntax validation?

#### 6. ContractWriteNode
- **Language**: Handlebars
- **Usage**: Template variables for contract method parameters
- **Backend**: `ContractWriteNode` message exists
- **Decision Needed**: Does Handlebars need syntax validation?

#### 7. EmailNode
- **Language**: Handlebars (multiple editors)
- **Usage**: Email body, subject, etc. with template variables
- **Backend**: `EmailNode` message exists
- **Decision Needed**: Does Handlebars need syntax validation?

#### 8. TelegramNode
- **Language**: Handlebars
- **Usage**: Message templates with variables
- **Backend**: `TelegramNode` message exists
- **Decision Needed**: Does Handlebars need syntax validation?

#### 9-13. Loop Node Variants
All loop nodes use CodeEditor for their child node configurations:
- **LoopNodeContractRead**: Handlebars
- **LoopNodeContractWrite**: Handlebars
- **LoopNodeCustomCode**: JavaScript
- **LoopNodeRestApi**: Handlebars

**Question**: Do loop variants need separate `lang` fields, or do they inherit from their child node type?

### 🟢 LOW PRIORITY (Read-Only or Special Cases)

#### 14. LoopNode
- **Language**: JSON (read-only)
- **Usage**: Display loop variables (not editable)
- **Action**: No `lang` field needed

## Missing from Original Plan

The original implementation plan was missing:

### ❌ Not Included (Should Review)
1. **ContractReadNode** - Uses handlebars templates
2. **ContractWriteNode** - Uses handlebars templates
3. **EmailNode** - Uses handlebars templates (multiple editors)
4. **TelegramNode** - Uses handlebars templates
5. **LoopNodeContractRead** - Uses handlebars templates
6. **LoopNodeContractWrite** - Uses handlebars templates
7. **LoopNodeCustomCode** - Uses JavaScript
8. **LoopNodeRestApi** - Uses handlebars templates

## Recommendations

### Phase 1: Core Data/Expression Nodes (HIGH PRIORITY)
✅ **Include in immediate implementation:**
- ManualTriggerNode (JSON)
- FilterNode (JavaScript)
- SubgraphNode (GraphQL)
- BranchNode (Handlebars)

These are **data/logic-driven** nodes where syntax validation provides clear value.

### Phase 2: Template Nodes (MEDIUM PRIORITY)
🟡 **Consider for future:**
- ContractReadNode
- ContractWriteNode
- EmailNode
- TelegramNode
- LoopNode variants

**Decision Point**: Should we validate Handlebars template syntax?
- **Pro**: Catch template errors early (e.g., `{{unclosed`, `{{invalid.path}}`)
- **Con**: Handlebars is more forgiving, runtime resolution might be acceptable
- **Recommendation**: Start with Phase 1, add Phase 2 after we see user feedback

### Phase 3: Loop Variants
🔵 **Needs Architecture Decision:**
- Do loop nodes need their own `lang` field?
- Or do they inherit language from their wrapped node type?
- Recommendation: Handle loop nodes separately after core implementation

## Updated Implementation Plan

### Immediate Action Items

1. ✅ **ManualTrigger** - Add `lang` field (default: `JSON`)
2. ✅ **FilterNode** - Add `lang` field (default: `JavaScript`)
3. ✅ **SubgraphNode** - Add `lang` field (default: `GraphQL`)
4. ✅ **BranchNode.Condition** - Add `lang` field (default: `Handlebars`)

### Future Consideration

5. 🟡 **ContractReadNode** - Evaluate need for `lang` field
6. 🟡 **ContractWriteNode** - Evaluate need for `lang` field
7. 🟡 **EmailNode** - Evaluate need for `lang` field
8. 🟡 **TelegramNode** - Evaluate need for `lang` field
9. 🟡 **Loop Node Variants** - Design approach for loop wrappers

## Protobuf Updates Needed

```protobuf
// Already has lang field ✅
message CustomCodeNode {
  message Config {
    Lang lang = 1;
    string source = 2;
  }
}

// NEED TO ADD lang field ❌
message ManualTrigger {
  message Config {
    google.protobuf.Value data = 1;
    map<string, string> headers = 2;
    map<string, string> pathParams = 3;
    Lang lang = 4;  // ADD THIS
  }
}

// NEED TO ADD lang field ❌
message FilterNode {
  message Config {
    string expression = 1;
    string input_node_name = 2;
    Lang lang = 3;  // ADD THIS
  }
}

// NEED TO ADD lang field ❌
message SubgraphNode {
  message Config {
    string query = 1;
    Lang lang = 2;  // ADD THIS
    // ... other fields
  }
}

// NEED TO ADD lang field ❌
message BranchNode {
  message Condition {
    string id = 1;
    string type = 2;
    string expression = 3;
    Lang lang = 4;  // ADD THIS
  }
  
  message Config {
    repeated Condition conditions = 1;
  }
}

// CONSIDER FOR FUTURE 🟡
message ContractReadNode {
  message Config {
    // ... existing fields
    Lang lang = ?;  // CONSIDER ADDING
  }
}

message ContractWriteNode {
  message Config {
    // ... existing fields
    Lang lang = ?;  // CONSIDER ADDING
  }
}

message EmailNode {
  message Config {
    // ... existing fields
    Lang lang = ?;  // CONSIDER ADDING
  }
}

message TelegramNode {
  message Config {
    // ... existing fields
    Lang lang = ?;  // CONSIDER ADDING
  }
}
```

## Questions for Decision

1. **Handlebars Validation**: Should we validate Handlebars template syntax, or is runtime validation sufficient?
   
2. **Loop Nodes**: Should loop node variants have their own `lang` field, or should they be handled differently?

3. **Phase 2 Scope**: Should template nodes (Email, Telegram, Contract nodes) be included in this implementation, or deferred to a future phase?

4. **Validation Strictness**: How strict should Handlebars validation be? Just syntax, or also check variable references?

## Recommendation

**Start with Phase 1 (4 nodes):**
- ManualTrigger
- FilterNode  
- SubgraphNode
- BranchNode

This gives us:
- Clear value proposition (syntax validation)
- Different languages (JSON, JavaScript, GraphQL, Handlebars)
- Proof of concept for centralized validation
- Foundation for future expansion

**Defer Phase 2** until we see how Phase 1 performs and get user feedback on whether template validation is needed.
