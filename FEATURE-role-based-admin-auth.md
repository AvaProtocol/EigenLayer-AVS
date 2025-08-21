# Feature: Role-Based Admin Authentication

## **Problem**
Currently, the system uses a hack to identify admin users by checking if `user.Address == common.Address{}` (zero address). This approach is:
- ❌ Unclear and confusing (zero address has semantic meaning in Ethereum as burn address)
- ❌ Brittle and hard to maintain
- ❌ Not extensible for future role requirements
- ❌ Not following industry best practices

## **Proposed Solution**
Implement proper role-based authorization using JWT claims that are already present in the system.

### **Current JWT Structure**
```json
{
  "iss": "AvaProtocol",
  "sub": "apikey",
  "exp": 2071113857,
  "roles": ["admin"]  // ← Already exists but not used properly
}
```

## **Implementation Plan**

### **1. Update User Model**
```go
type User struct {
    Address             common.Address
    SmartAccountAddress *common.Address
    Roles               []string // Add roles field
}

// Add convenience methods
func (u *User) HasRole(role string) bool {
    for _, userRole := range u.Roles {
        if userRole == role {
            return true
        }
    }
    return false
}

func (u *User) IsAdmin() bool {
    return u.HasRole("admin")
}
```

### **2. Extract Roles in Auth**
Modify `aggregator/auth.go` to extract roles from JWT claims:
```go
// Extract roles from JWT claims if present
if rolesInterface, ok := claims["roles"]; ok {
    if rolesSlice, ok := rolesInterface.([]interface{}); ok {
        roles := make([]string, 0, len(rolesSlice))
        for _, role := range rolesSlice {
            if roleStr, ok := role.(string); ok {
                roles = append(roles, roleStr)
            }
        }
        user.Roles = roles
    }
}
```

### **3. Replace Zero Address Checks**
Update `core/taskengine/engine.go`:
```go
// Replace this hack:
// isAdminUser := user.Address == (common.Address{})

// With proper role check:
if !user.IsAdmin() && !task.OwnedBy(user.Address) {
    return nil, status.Errorf(codes.NotFound, TaskNotFoundError)
}
```

## **Benefits**
- ✅ **Clear Intent**: `user.IsAdmin()` vs `user.Address == common.Address{}`
- ✅ **Extensible**: Easy to add new roles like "operator", "viewer", "support"
- ✅ **Industry Standard**: Role-based access control is the standard approach
- ✅ **Future-Proof**: JWT already contains roles array for expansion
- ✅ **Maintainable**: No more magic address comparisons

## **Files to Modify**
1. `model/user.go` - Add Roles field and helper methods
2. `aggregator/auth.go` - Extract roles from JWT claims
3. `core/taskengine/engine.go` - Replace zero address checks in `GetTask` and `ListExecutions`
4. Any other files using the zero address hack

## **Testing**
- Verify admin API key authentication still works
- Verify regular user authentication still works
- Test role extraction from JWT claims
- Test admin bypass in GetTask and ListExecutions

## **Migration**
This is a backward-compatible change:
- Existing API keys will continue to work
- Regular users are unaffected
- Only the internal admin detection logic changes

## **Future Enhancements**
Once role-based auth is implemented, we can easily add:
- **Operator Role**: For operator-specific endpoints
- **Viewer Role**: Read-only access to workflows
- **Support Role**: Limited admin access for customer support
- **Fine-grained Permissions**: Role-based access to specific operations
