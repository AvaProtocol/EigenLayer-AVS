# Historical Migrations

This folder contains database migration files that have already been executed in production and are no longer part of the active migration process.

## Why are these files here?

- These migrations have already run successfully in production
- They are kept for historical reference and documentation purposes
- They have been moved out of the main `migrations/` directory to avoid:
  - Being included in regular build/test cycles
  - Causing maintenance overhead
  - Creating confusion about which migrations are still active

## Files

### 20250405-232000-change-epoch-to-ms.go
**Status**: ✅ Completed and archived  
**Purpose**: Converted epoch timestamps from seconds to milliseconds across the database  
**Date Applied**: ~April 2025  

This migration updated:
- Task timestamps (StartAt, ExpiredAt, CompletedAt, LastRanAt)
- Execution timestamps (StartAt, EndAt) 
- Step timestamps (StartAt, EndAt)
- Trigger output timestamps (BlockTimestamp, Epoch/Timestamp fields)

### 20250405-232000-change-epoch-to-ms_test.go
**Status**: ✅ Completed and archived  
**Purpose**: Test file for the epoch conversion migration  

## Notes

- These files use build tags (`//go:build migrations`) to prevent inclusion in normal builds
- The migration logic was updated to work with the current protobuf structure before archiving
- Tests were also updated but are no longer run as part of the regular test suite

## Active Migrations

Active migrations that still need to run remain in the main `migrations/` directory. 