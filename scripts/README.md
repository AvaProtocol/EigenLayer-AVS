# Storage Structure Comparison Tools

This directory contains tools for analyzing storage structure differences between branches to determine if data migrations are needed.

## compare_storage_structure.go

A Go script that compares storage key structures between branches and analyzes specific changes for migration requirements.

Usage:
```
go run compare_storage_structure.go <old_branch> <new_branch>
```

This compares storage structures between old_branch and new_branch, where:
- old_branch: The reference branch (typically main)
- new_branch: The branch with changes to analyze (typically staging or a feature branch)

Example:
```
# Compare main (old/reference) with staging (new/changes)
go run compare_storage_structure.go main staging
```

## compare_storage.sh

A shell script wrapper for easier execution of the Go script.

Usage:
```
./compare_storage.sh <old_branch> <new_branch>
```

This compares storage structures between old_branch and new_branch, where:
- old_branch: The reference branch (typically main)
- new_branch: The branch with changes to analyze (typically staging or a feature branch)

Example:
```
# Compare main (old/reference) with staging (new/changes)
./compare_storage.sh main staging
```

## How to determine if migration is needed

When considering if a migration is needed, analyze the following:

1. **Storage Key Structure Changes**: If the format of keys used to store data has changed, a migration is likely needed.

2. **Data Structure Changes**: If the structure of stored data has changed in a non-backward-compatible way, a migration is needed.

3. **Backward Compatible Changes**: Changes like adding new fields with `omitempty` JSON tags typically don't require migrations.

4. **Runtime vs. Storage Changes**: Changes that only affect runtime behavior and not storage don't require migrations.

## Adding a migration

If a migration is needed, follow these steps:

1. Create a new migration file in the `./migrations` directory with a timestamped name (e.g., `YYYYMMDD-HHMMSS-description.go`)

2. Implement the migration function following the pattern in existing migrations

3. Add the migration to the `Migrations` slice in `./migrations/migrations.go`

4. Test the migration thoroughly before deployment
