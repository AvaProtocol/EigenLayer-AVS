# Storage Structure Comparison Tool

This directory contains a tool for analyzing storage structure differences between branches to determine if data migrations are needed.

## compare_storage_structure.go

A Go script that compares storage key structures between branches and analyzes specific changes for migration requirements.

Usage:
```
go run compare_storage_structure.go <old_branch>
```

This compares storage structures between the old_branch and your current branch, where:
- old_branch: The reference branch (typically main)
- current branch: The branch with changes you want to analyze (you must check this out first)

Example:
```
# First checkout the branch you want to analyze
git checkout staging

# Then compare with main (old/reference)
go run compare_storage_structure.go main
```

### Branch History Requirements

For accurate comparison, ensure your branches have a linear history:
- The current branch should be rebased on top of the old_branch
- This ensures all changes are properly detected and analyzed
- If branches don't have linear history, the script will warn you

The script uses git diff to analyze changes in storage-related files, which works best with linear history.

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
