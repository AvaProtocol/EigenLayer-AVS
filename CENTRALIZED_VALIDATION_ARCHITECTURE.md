# Centralized Validation Architecture

## The Big Picture: One Validator for All Nodes

```
┌─────────────────────────────────────────────────────────────────┐
│                     USER INPUT (from frontend)                  │
└───────────────┬─────────────────────────────────────────────────┘
                │
                │  All nodes send data + lang field
                ▼
┌───────────────────────────────────────────────────────────────────┐
│                       BACKEND NODES                               │
│                                                                   │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐          │
│  │ ManualTrigger│  │  FilterNode  │  │  BranchNode  │          │
│  │ data + lang  │  │ expr + lang  │  │ cond + lang  │   ...    │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘          │
│         │                 │                 │                    │
│         └─────────────────┴─────────────────┘                    │
│                           │                                       │
└───────────────────────────┼───────────────────────────────────────┘
                            │
                            │  All call the same function
                            ▼
       ┌────────────────────────────────────────────┐
       │  ValidateInputByLanguage(data, lang)       │
       │                                            │
       │  🎯 CENTRALIZED UNIVERSAL VALIDATOR        │
       │                                            │
       │  switch lang {                            │
       │    case JSON -> ValidateJSONFormat()      │
       │    case JavaScript -> ValidateJSSyntax()  │
       │    case GraphQL -> ValidateGraphQL()      │
       │    case Handlebars -> ValidateHandlebars()│
       │  }                                         │
       └────────────────────────────────────────────┘
                            │
                            ▼
                    ✅ Valid or ❌ Error
```

## Before (❌ Scattered Validation)

Each node implements its own validation logic:

```go
// ManualTrigger validation in run_node_immediately.go
func runManualTriggerImmediately(...) {
    if dataStr, ok := data.(string); ok {
        var jsonTest interface{}
        if err := json.Unmarshal([]byte(dataStr), &jsonTest); err != nil {
            return nil, error  // ❌ JSON validation here
        }
    }
}

// FilterNode validation somewhere else
func runFilterNode(...) {
    // ❌ JavaScript validation here (maybe?)
    // ❌ Or maybe no validation?
}

// BranchNode validation in yet another place
func runBranchNode(...) {
    // ❌ Handlebars validation here (or not?)
}

// SubgraphNode validation...
func runSubgraphNode(...) {
    // ❌ GraphQL validation here?
}
```

**Problems:**
- ❌ Duplicate code everywhere
- ❌ Inconsistent validation behavior
- ❌ Hard to add new languages (need to update multiple places)
- ❌ Hard to test (need to test each node separately)
- ❌ No clear ownership of validation logic

## After (✅ Centralized Validation)

All nodes use the same universal validator:

```go
// ============================================
// ONE validation file: validation_constants.go
// ============================================

// Universal validator - ALL nodes use this!
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
        return nil  // No validation for unknown languages
    }
}

// ============================================
// All nodes: Same pattern, different data
// ============================================

// ManualTrigger
func runManualTriggerImmediately(config map[string]interface{}, ...) {
    lang := getLangWithDefault(config, avsproto.Lang_JSON)
    if err := ValidateInputByLanguage(config["data"], lang); err != nil {
        return nil, err
    }
    // ... rest of logic
}

// FilterNode
func runFilterNode(config map[string]interface{}, ...) {
    lang := getLangWithDefault(config, avsproto.Lang_JavaScript)
    if err := ValidateInputByLanguage(config["expression"], lang); err != nil {
        return nil, err
    }
    // ... rest of logic
}

// BranchNode
func runBranchNode(condition Condition, ...) {
    lang := getLangWithDefault(condition, avsproto.Lang_Handlebars)
    if err := ValidateInputByLanguage(condition.Expression, lang); err != nil {
        return nil, err
    }
    // ... rest of logic
}

// SubgraphNode
func runSubgraphNode(config map[string]interface{}, ...) {
    lang := getLangWithDefault(config, avsproto.Lang_GraphQL)
    if err := ValidateInputByLanguage(config["query"], lang); err != nil {
        return nil, err
    }
    // ... rest of logic
}
```

**Benefits:**
- ✅ One place to maintain validation logic
- ✅ Consistent validation across all nodes
- ✅ Easy to add new languages (one switch case)
- ✅ Easy to test (test ValidateInputByLanguage once)
- ✅ Clear ownership and responsibility

## Adding a New Language (Example: YAML)

### Before (❌ Scattered)
Need to update 4+ different files:
1. Update ManualTrigger validation
2. Update FilterNode validation
3. Update BranchNode validation
4. Update SubgraphNode validation
5. Update tests in 4+ files

### After (✅ Centralized)
Update ONE file:

```go
// In validation_constants.go - that's it!
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
    case avsproto.Lang_YAML:  // ← Add new language here
        return ValidateYAMLFormat(data)  // ← Implement once
    default:
        return nil
    }
}

// ALL nodes automatically support YAML now! 🎉
```

## Real-World Example Flow

```
1. User creates ManualTrigger with JSON data in frontend
   ↓
   Frontend: { data: "{ user: 123 }", lang: "JSON" }
   ↓
2. Backend receives request
   ↓
3. runManualTriggerImmediately() extracts lang field
   ↓
4. Calls ValidateInputByLanguage(data, JSON)
   ↓
5. Universal validator routes to ValidateJSONFormat()
   ↓
6. Returns ✅ valid or ❌ error
   ↓
7. If valid, proceed with execution
```

Same flow for FilterNode, BranchNode, SubgraphNode, etc.!

## Code Organization

```
core/taskengine/
├── validation_constants.go          ← 🎯 CENTRAL HUB
│   ├── ValidateInputByLanguage()   ← Universal dispatcher
│   ├── ValidateJSONFormat()        ← JSON validator
│   ├── ValidateJavaScriptSyntax()  ← JS validator
│   ├── ValidateGraphQLSyntax()     ← GraphQL validator
│   └── ValidateHandlebarsSyntax()  ← Handlebars validator
│
├── run_node_immediately.go          ← Nodes just call the hub
│   ├── runManualTriggerImmediately() → calls ValidateInputByLanguage
│   ├── runFilterNode()              → calls ValidateInputByLanguage
│   └── ...
│
└── engine.go                        ← Engine calls the hub
    ├── TriggerTask()                → calls ValidateInputByLanguage
    └── SimulateTask()               → calls ValidateInputByLanguage
```

## Summary

**Core Principle:** 
> Don't ask "What node is this?" Ask "What language is this?"

**Architecture:**
```
Node Type (ManualTrigger, Filter, Branch) 
         ↓
    Extract lang field
         ↓
ValidateInputByLanguage(data, lang)  ← Universal validator
         ↓
    Language-specific validation
         ↓
    ✅ Valid or ❌ Error
```

**Result:**
- **Nodes are thin** - they just extract lang and call validator
- **Validation is centralized** - all logic in one place
- **Languages are first-class** - add once, works everywhere
- **Testing is easy** - test validator once, not per-node
- **Maintenance is simple** - one place to fix bugs
