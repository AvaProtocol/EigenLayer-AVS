# Completed 2025 Migrations

This directory contains database migrations that have been successfully executed in production and are no longer part of the active migration process.

## Files

### 20250603-183034-token-metadata-fields
**Status**: ✅ Completed and archived  
**Purpose**: Added required fields to TokenMetadata struct  
**Date Applied**: June 2025  
**Production Status**: Successfully applied, 0 records updated (new feature, no existing data)

This migration handled the addition of new TokenMetadata struct fields:
- TokenMetadata.Address: Added required field of type string
- TokenMetadata.Name: Added required field of type string  
- TokenMetadata.Symbol: Added required field of type string
- TokenMetadata.Decimals: Added required field of type uint32
- TokenMetadata.Source: Added required field of type string

### 20250128-120000-protobuf-structure-cleanup
**Status**: ✅ Completed and archived  
**Purpose**: Protobuf structure cleanup migration for v1.9.6  
**Date Applied**: January 2025  
**Production Status**: Successfully applied, cleaned up incompatible data structures

This migration handled cleanup of incompatible data structures after major protobuf changes:
- Canceled workflows with incompatible trigger structures
- Deleted executions with old trigger output structures  
- Cleaned cached trigger and node data
- Removed workflows using deprecated source_id fields

### 20250913-185000-add-execution-indexes
**Status**: ✅ Completed and archived  
**Purpose**: Add proper execution indexes to existing executions based on creation order  
**Date Applied**: September 2025  
**Production Status**: Successfully applied, added sequential indexes to all execution records

This migration handled adding execution sequence tracking:
- Scanned all execution records in database (history:* keys)
- Grouped executions by Task ID
- Sorted executions by ULID creation time (chronological order)
- Assigned sequential indexes (0, 1, 2, ...) to each execution within a task
- Updated execution records in-place with new Index field

## Migration Status

All three migrations have been successfully applied in production and consistently report 0 records updated, indicating:
- All target data structures have been properly migrated
- No legacy data remains that requires transformation
- The migrations can be safely archived

## Notes

- These migrations have been moved out of active deployment to reduce startup overhead
- Migration completion records remain in the database (`migration:*` keys)
- Files use build tags to prevent inclusion in normal builds
- Future deployments will not execute these migrations
