# Database Migrations

This document outlines how to create and manage database migrations for the EigenLayer-AVS project.

## What are Migrations?

Migrations are a way to make incremental, reversible changes to the database schema or data. They help maintain database consistency across different environments and versions of the application.

## Creating a New Migration

### Automated Migration (Go Struct Changes)

For Go struct changes that affect storage, use the automated migration tools:

```bash
# Check for changes
go run scripts/compare_storage_structure.go main

# Generate migration if changes detected
go run scripts/migration/create_migration.go main
```

### Manual Migration (Protobuf Changes)

For protobuf message structure changes, manual migration creation is required:

1. Create a new Go file in the `migrations` package with a descriptive name (e.g., `my_migration.go`)

2. Define your migration function that implements the required signature:

   ```go
   func YourMigrationName(db storage.Storage) (int, error) {
       // Migration logic here
       // Use db.GetKey(), db.Set(), db.Delete(), etc. to modify data
       
       recordsUpdated := 0
       // ... migration logic ...
       
       return recordsUpdated, nil // Return count and error if migration fails
   }
   ```

3. Register your migration in the `migrations.go` file by adding it to the `Migrations` slice:

   ```go
   var Migrations = []migrator.Migration{
       // Existing migrations...
       {
           Name:     "your-migration-name",
           Function: YourMigrationName,
       },
   }
   ```

## Migration Best Practices

1. **Descriptive Names**: Use clear, descriptive names for your migrations that indicate what they do.

2. **Idempotency**: Migrations should be idempotent (can be run multiple times without side effects).

3. **Atomicity**: Each migration should represent a single, atomic change to the database.

4. **Error Handling**: Properly handle errors and return them to the migrator.

5. **Documentation**: Add comments to your migration code explaining what it does and why.

6. **Data Safety**: For breaking changes, prefer canceling/removing incompatible data over attempting complex transformations.

## Migration Types

### Data Structure Migrations
- Handle changes to Go structs that affect serialized data
- Usually involve field additions, removals, or type changes
- Often can preserve existing data with transformations

### Protobuf Structure Migrations
- Handle changes to protobuf message definitions
- Often involve breaking changes to message structure
- May require data cleanup rather than transformation
- Example: `20250128-120000-protobuf-structure-cleanup.go`

## Migration Lifecycle

Once a migration has been successfully applied in production:

1. Comment it out or remove it from the `Migrations` slice in `migrations.go`
2. Move the migration files to `docs/historical-migrations/` for historical reference
3. Update the historical migrations README with details about what the migration did

This keeps the active migrations directory clean and focused on migrations that still need to run.

## Active Migrations

Current migrations that will run on deployment:

- `20250603-183034-token-metadata-fields` - TokenMetadata struct field additions
- `20250128-120000-protobuf-structure-cleanup` - v1.9.6 protobuf structure cleanup

## Example Migrations

### Go Struct Migration
An example of an active struct migration can be viewed in function `TokenMetadataFieldsMigration`.

### Protobuf Migration  
An example of protobuf structure cleanup can be viewed in function `ProtobufStructureCleanupMigration`.

For examples of completed migrations, see the `docs/historical-migrations/` directory.

## Testing Migrations

Always test migrations thoroughly:

1. **Unit Tests**: Create test functions that verify migration behavior
2. **Integration Tests**: Test against realistic data structures  
3. **Rollback Testing**: Ensure migrations can be safely rolled back if needed
4. **Performance Testing**: Verify migrations complete in reasonable time

## Troubleshooting

### Common Issues

1. **Storage Interface**: Use `db.GetKey()`, `db.Set()`, `db.Delete()` methods
2. **Data Serialization**: Use `model.Task` wrapper and `protojson` for protobuf data
3. **Error Handling**: Always check and handle errors gracefully
4. **Key Prefixes**: Use correct key prefixes (`task:`, `execution:`, etc.)

### Debugging

- Add comprehensive logging to track migration progress
- Use descriptive error messages
- Return detailed statistics on records processed
