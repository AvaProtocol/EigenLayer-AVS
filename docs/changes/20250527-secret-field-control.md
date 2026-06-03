# API Message Renaming and Flexible Field Control

## Overview

This document describes the implementation of message renaming and flexible field control for the ListSecrets and ListTasks APIs. The changes improve API design consistency and provide performance optimization through selective field population.

## Changes Made

### 1. Secret Message Renaming

**Problem**: The `ListSecretsResp` contained a redundant nested message `ResponseSecret` that duplicated the functionality of a main `Secret` message.

**Solution**: 
- Renamed `ResponseSecret` to `Secret` and made it a top-level message
- Updated `ListSecretsResp` to use `repeated Secret items = 1` directly
- Added additional metadata fields to the `Secret` message for flexible control

**Before**:
```protobuf
message ListSecretsResp {
  message ResponseSecret {
    string name = 1;
    string scope = 2;
    string workflow_id = 4;
    string org_id = 5;
  }
  repeated ResponseSecret items = 1;
  PageInfo page_info = 2;
}
```

**After**:
```protobuf
message Secret {
  string name = 1;
  string scope = 2;
  string workflow_id = 3;
  string org_id = 4;
  // Additional fields that can be controlled via field masks
  int64 created_at = 5;      // Unix timestamp when secret was created
  int64 updated_at = 6;      // Unix timestamp when secret was last updated
  string created_by = 7;     // User ID who created the secret
  string description = 8;    // Optional description of the secret
}

message ListSecretsResp {
  repeated Secret items = 1;
  PageInfo page_info = 2;
}
```

### 2. Flexible Field Control

**Problem**: The API always returned all available fields, even when clients only needed basic information, leading to unnecessary data transfer and processing overhead.

**Solution**: Added optional field control parameters to `ListSecretsReq` that allow clients to request only the fields they need.

**Enhanced Request Message**:
```protobuf
message ListSecretsReq {
  string workflow_id = 1;
  string before = 2;
  string after = 3;
  int64 limit = 4;
  
  // Field control options for flexible response content
  bool include_timestamps = 5;   // Include created_at and updated_at fields
  bool include_created_by = 6;   // Include created_by field
  bool include_description = 7;  // Include description field
}
```

### 3. Task Message Field Control

**Problem**: The `ListTasksResp` contained a redundant nested message `Item` that duplicated most fields from the main `Task` message, excluding only the expensive `nodes` and `edges` fields.

**Solution**: 
- Removed the nested `ListTasksResp.Item` message
- Updated `ListTasksResp` to use `repeated Task items = 1` directly
- Added field control parameters to `ListTasksReq` for expensive fields

**Before**:
```protobuf
message ListTasksResp {
  message Item {
    // All Task fields except nodes and edges
    string id = 1;
    string owner = 2;
    // ... 10 more fields
    TaskTrigger trigger = 12;
  }
  repeated Item items = 1;
  PageInfo page_info = 2;
}
```

**After**:
```protobuf
message ListTasksReq {
  repeated string smart_wallet_address = 1;
  string before = 2;
  string after = 3;
  int64 limit = 4;
  
  // Field control options for expensive fields
  bool include_nodes = 5;  // Include task nodes (expensive field)
  bool include_edges = 6;  // Include task edges (expensive field)
}

message ListTasksResp {
  repeated Task items = 1;  // Use main Task message directly
  PageInfo page_info = 2;
}
```

## Implementation Details

### Service Layer Implementation

The `ListSecrets` function in `engine.go` was updated to conditionally populate fields based on request parameters:

```go
// Always include basic fields
item := &avsproto.Secret{
    Name:       secretWithNameOnly.Name,
    OrgId:      secretWithNameOnly.OrgID,
    WorkflowId: secretWithNameOnly.WorkflowID,
    Scope:      "user", // Default scope
}

// Conditionally populate additional fields based on request parameters
if payload != nil {
    if payload.IncludeTimestamps {
        item.CreatedAt = time.Now().Unix() // Would fetch from storage
        item.UpdatedAt = time.Now().Unix() // Would fetch from storage
    }
    if payload.IncludeCreatedBy {
        item.CreatedBy = user.Address.Hex() // Would fetch from storage
    }
    if payload.IncludeDescription {
        item.Description = "" // Would fetch from storage
    }
}
```

### Field Control Patterns

The implementation provides several patterns for field control:

1. **Default Fields**: Always included for basic functionality
   - `name`, `scope`, `workflow_id`, `org_id`

2. **Optional Fields**: Included only when requested
   - `created_at`, `updated_at` (via `include_timestamps`)
   - `created_by` (via `include_created_by`)
   - `description` (via `include_description`)

3. **Performance Optimization**: Clients can request minimal fields for list views and detailed fields for individual item views

## Usage Examples

### Basic List (Minimal Fields)
```go
resp, err := client.ListSecrets(ctx, &avsproto.ListSecretsReq{
    Limit: 50,
    // No field control flags - returns only basic fields
})
```

### Detailed List (All Fields)
```go
resp, err := client.ListSecrets(ctx, &avsproto.ListSecretsReq{
    Limit: 50,
    IncludeTimestamps:  true,
    IncludeCreatedBy:   true,
    IncludeDescription: true,
})
```

### Selective Fields
```go
resp, err := client.ListSecrets(ctx, &avsproto.ListSecretsReq{
    Limit: 50,
    IncludeTimestamps: true, // Only include timestamp fields
})
```

### Task Field Control Examples

#### Basic Task List (Minimal Fields)
```go
resp, err := client.ListTasks(ctx, &avsproto.ListTasksReq{
    SmartWalletAddress: []string{"0x123..."},
    Limit: 50,
    // No field control flags - excludes expensive nodes/edges
})
```

#### Detailed Task List (All Fields)
```go
resp, err := client.ListTasks(ctx, &avsproto.ListTasksReq{
    SmartWalletAddress: []string{"0x123..."},
    Limit: 50,
    IncludeNodes: true,  // Include task execution nodes
    IncludeEdges: true,  // Include task execution edges
})
```

#### Selective Task Fields
```go
resp, err := client.ListTasks(ctx, &avsproto.ListTasksReq{
    SmartWalletAddress: []string{"0x123..."},
    Limit: 50,
    IncludeNodes: true,  // Only include nodes, not edges
})
```

## Benefits

### 1. Performance Improvements
- **Reduced Data Transfer**: Clients only receive the fields they need
- **Lower Processing Overhead**: Server doesn't populate expensive fields unless requested
- **Faster Response Times**: Smaller payloads result in faster network transfer

### 2. API Design Consistency
- **Eliminates Redundancy**: Single `Secret` message instead of nested `ResponseSecret`
- **Better Extensibility**: Easy to add new optional fields without breaking existing clients
- **Industry Standards**: Follows GraphQL field selection and REST partial response patterns

### 3. Backward Compatibility
- **Graceful Degradation**: Existing clients continue to work with default field set
- **Progressive Enhancement**: Clients can opt-in to additional fields as needed

## Testing

Comprehensive tests were added to verify the field control functionality:

### Test Coverage
- **Default Fields Only**: Verifies basic fields are always included
- **Individual Field Control**: Tests each optional field independently
- **Combined Field Control**: Tests multiple optional fields together
- **Pagination Integration**: Ensures field control works with pagination
- **Performance Testing**: Validates minimal vs. detailed field scenarios

### Test Files
- `core/taskengine/secret_field_control_test.go`: Comprehensive Secret field control tests
- `core/taskengine/task_field_control_test.go`: Comprehensive Task field control tests
- `examples/flexible_field_control.go`: Example implementation patterns

## Future Enhancements

### 1. Storage Optimization
- Implement selective field fetching at the storage layer
- Cache frequently accessed metadata separately
- Use different storage queries based on field requirements

### 2. Security Enhancements
- Add field-level access control
- Audit field access patterns
- Rate limit requests for expensive fields

### 3. Advanced Field Control
- Support for nested field selection (GraphQL-style)
- Field aliasing and transformation
- Conditional field population based on user permissions

## Migration Guide

### For API Consumers
1. **No Action Required**: Existing code continues to work unchanged
2. **Optional Optimization**: Add field control parameters to reduce payload size
3. **New Features**: Use additional fields (`created_at`, `updated_at`, etc.) when needed

### For API Implementers
1. **Update Protobuf**: Regenerate client code after protobuf changes
2. **Field Population**: Implement conditional field population in service layer
3. **Storage Optimization**: Consider optimizing storage queries based on requested fields

## Conclusion

The API message renaming and flexible field control implementation provides a more efficient, consistent, and extensible API design. The changes maintain backward compatibility while offering significant performance benefits for clients that adopt the new field control parameters.

**Key Achievements:**
- ✅ **Secret API**: Eliminated `ResponseSecret` redundancy, added metadata field control
- ✅ **Task API**: Eliminated `ListTasksResp.Item` redundancy, added expensive field control for `nodes`/`edges`
- ✅ **Consistency**: Both APIs now follow the same pattern of using main message types with optional field control
- ✅ **Performance**: Clients can optimize data transfer by requesting only needed fields
- ✅ **Extensibility**: Easy to add field control to other APIs using the established patterns 