# API Reference: GetExecutionStats vs GetExecutionCount

This document explains the differences between two similar API methods for retrieving execution data: `GetExecutionStats` and `GetExecutionCount`.

## Overview

Both methods allow you to retrieve information about workflow executions, but they serve different purposes and return different levels of detail.

## GetExecutionCount

### Purpose
Returns a simple count of total executions for specified workflows or all workflows belonging to the authenticated user.

### Request Format
```protobuf
message GetExecutionCountReq {
  repeated string workflow_ids = 1; // Optional array of workflow IDs
}
```

### Response Format
```protobuf
message GetExecutionCountResp {
  int64 total = 1; // The total count of executions
}
```

### Behavior
- **Time Scope**: Counts all executions across all time periods
- **Performance**: Fast operation using efficient key counting
- **Data Processing**: Uses `CountKeysByPrefixes()` for optimal performance
- **Use Case**: When you only need to know "how many executions happened"

### Example Usage
```javascript
// Count all executions for the user
const response = await client.getExecutionCount({});
console.log(`Total executions: ${response.total}`);

// Count executions for specific workflows
const response = await client.getExecutionCount({
  workflowIds: ["workflow1", "workflow2"]
});
```

## GetExecutionStats

### Purpose
Returns detailed execution statistics including success/failure breakdown, performance metrics, and time-filtered data.

### Request Format
```protobuf
message GetExecutionStatsReq {
  repeated string workflow_ids = 1; // Optional array of workflow IDs
  int64 days = 2; // Number of days to look back (default: 7)
}
```

### Response Format
```protobuf
message GetExecutionStatsResp {
  int64 total = 1; // Total number of executions
  int64 succeeded = 2; // Number of successful executions
  int64 failed = 3; // Number of failed executions
  double avg_execution_time = 4; // Average execution time in milliseconds
}
```

### Behavior
- **Time Scope**: Configurable time window (default: last 7 days)
- **Performance**: Slower operation due to data processing and analysis
- **Data Processing**: Iterates through execution records, unmarshals protobuf data, and calculates statistics
- **Use Case**: Analytics, monitoring, dashboard metrics, performance analysis

### Example Usage
```javascript
// Get stats for last 7 days (default)
const stats = await client.getExecutionStats({});
console.log(`Success rate: ${(stats.succeeded / stats.total * 100).toFixed(2)}%`);
console.log(`Average execution time: ${stats.avgExecutionTime}ms`);

// Get stats for last 30 days for specific workflows
const stats = await client.getExecutionStats({
  workflowIds: ["workflow1", "workflow2"],
  days: 30
});
```

## Key Differences Summary

| Feature | GetExecutionCount | GetExecutionStats |
|---------|-------------------|-------------------|
| **Data Returned** | Just total count | Total + success/failure + avg time |
| **Time Filtering** | ❌ All time | ✅ Configurable days (default 7) |
| **Performance Metrics** | ❌ No | ✅ Average execution time |
| **Success/Failure Breakdown** | ❌ No | ✅ Yes |
| **Performance** | ⚡ Faster (counting only) | 🐌 Slower (data processing) |
| **Use Case** | Simple counting | Analytics & monitoring |
| **Memory Usage** | Low | Higher (processes execution data) |
| **Time Complexity** | O(1) key counting | O(n) execution processing |

## When to Use Which Method

### Use GetExecutionCount When:
- You only need a simple count of executions
- Performance is critical
- Building simple counters or badges
- Memory usage needs to be minimal
- You need historical data across all time periods

### Use GetExecutionStats When:
- You need detailed analytics and insights
- Building dashboards or monitoring tools
- You want success/failure rates
- Performance metrics are important
- You need time-filtered data (recent activity)
- You're analyzing execution patterns

## Implementation Details

### GetExecutionCount Implementation
```go
// Uses efficient key counting
prefixes := [][]byte{}
for _, id := range workflowIds {
    prefixes = append(prefixes, TaskExecutionPrefix(id))
}
total, err := n.db.CountKeysByPrefixes(prefixes)
```

### GetExecutionStats Implementation
```go
// Processes individual execution records
for _, item := range items {
    execution := &avsproto.Execution{}
    if err := protojson.Unmarshal(item.Value, execution); err != nil {
        continue
    }
    
    if execution.StartAt < cutoffTime {
        continue // Time filtering
    }
    
    total++
    if execution.Success {
        succeeded++
    } else {
        failed++
    }
    
    // Calculate execution time
    if execution.EndAt > execution.StartAt {
        executionTime := execution.EndAt - execution.StartAt
        totalExecutionTime += executionTime
    }
}
```

## Error Handling

Both methods handle similar error scenarios:
- Invalid workflow IDs are ignored (not counted)
- Workflow IDs not belonging to the authenticated user are ignored
- Database errors return appropriate gRPC error codes
- Empty workflow ID arrays default to counting all user workflows

## Performance Considerations

- **GetExecutionCount**: Suitable for high-frequency calls, real-time counters
- **GetExecutionStats**: Better suited for periodic updates, dashboard refreshes
- Consider caching `GetExecutionStats` results for frequently accessed data
- Use appropriate time windows in `GetExecutionStats` to balance detail vs performance

## Migration Guide

If you're currently using `GetExecutionCount` and need more detailed data:

```javascript
// Before: Simple counting
const count = await client.getExecutionCount({ workflowIds });

// After: Detailed stats with same total count
const stats = await client.getExecutionStats({ 
  workflowIds,
  days: 999999 // Large number to get all-time data like GetExecutionCount
});
// stats.total === count.total (for same time period)
```

Note: For true all-time data equivalent to `GetExecutionCount`, you may need to use a very large `days` value or consider the time filtering difference. 