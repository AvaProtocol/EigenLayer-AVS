# Database Migrations

This document outlines how to create and manage database migrations for the EigenLayer-AVS project.

## What are Migrations?

Migrations are a way to make incremental, reversible changes to the database schema or data. They help maintain database consistency across different environments and versions of the application.

## Creating a New Migration

To create a new migration, follow these steps:

1. Create a new Go file in the `migrations` package with a descriptive name (e.g., `my_migration.go`)

2. Define your migration function that implements the required signature:

   ```go
   func YourMigrationName(db storage.Storage) error {
       // Migration logic here
       // Use db.Put(), db.Get(), db.Delete(), etc. to modify data
       
       return nil // Return error if migration fails
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

## Migration Lifecycle

Once a migration has been successfully applied in production:

1. Comment it out or remove it from the `Migrations` slice in `migrations.go`
2. Move the migration files to `docs/historical-migrations/` for historical reference
3. Update the historical migrations README with details about what the migration did

This keeps the active migrations directory clean and focused on migrations that still need to run.

## Example Migration

An example of an active migration can be viewed in function `TokenMetadataFieldsMigration`.

For examples of completed migrations, see the `docs/historical-migrations/` directory.
