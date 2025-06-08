I'll help you clean up the debug logs and address the server-side queue issues.

## 1. Clean Up Debug Logs

Let me remove the debug logging I added to the SDK:
Now let me clean up the debug logging from the `sendGrpcRequest` method:
I need to clean up the remaining debug logging references:
Perfect! Now the debug logs are cleaned up. 

## 2. Server-Side Queue Issues

Now let me address the server-side logging issues you mentioned. Based on the logs, I can see two problems:

### Issue 1: Verbose Queue Logs
The logs show repetitive messages like:
```
INFO apqueue/worker.go:42 process job from queue {"signal": 9449}
INFO apqueue/worker.go:54 decoded job {"job_id": 9449, "jobName": "01JX7208TDFPZ0BSAZ1HV46672", ...}
```

### Issue 2: Orphaned Jobs
Many jobs are failing with:
```
Job processing failed ... "failure_reason": "task not found in storage: 01JX731..."
```

## Recommendations

Here are my recommendations for both issues:

### 1. Simplify Queue Logging

**Current verbose logs:**
- `process job from queue {"signal": 9449}`
- `decoded job {"job_id": 9449, "jobName": "01JX7208TDFPZ0BSAZ1HV46672", ...}`

**Suggested combined log:**
```go
// In worker.go, replace the two separate logs with one:
log.Info("Processing job from queue", "job_id", jobID, "task_id", taskID, "execution_id", executionID, "trigger_type", triggerType)
```

This would produce cleaner logs like:
```
INFO apqueue/worker.go:XX Processing job from queue {"job_id": 9449, "task_id": "01JX7208TDFPZ0BSAZ1HV46672", "execution_id": "01JX74VNF6DQHDGTE6MC7AB40Z", "trigger_type": 4}
```

### 2. Handle Orphaned Jobs

For the "task not found in storage" issue, you should implement:

**A. Dead Letter Queue (DLQ)**
```go
// When a task is not found, instead of just logging and failing:
if taskNotFound {
    // Move to dead letter queue for later analysis
    err := moveToDeadLetterQueue(job, "task_not_found")
    if err != nil {
        log.Error("Failed to move job to DLQ", "job_id", jobID, "error", err)
    }
    log.Warn("Task not found, moved to dead letter queue", 
        "job_id", jobID, 
        "task_id", taskID,
        "reason", "task_deleted_or_missing")
    return nil // Don't retry
}
```

**B. Job Cleanup Service**
```go
// Add a cleanup service that runs periodically:
func (w *Worker) cleanupOrphanedJobs() {
    // Query for jobs older than X hours/days with "task not found" failures
    // Remove them from the queue permanently
    // Log cleanup statistics
}
```

**C. Graceful Task Deletion**
```go
// When deleting a task, also clean up related queue jobs:
func DeleteTask(taskID string) error {
    // 1. Delete task from storage
    err := storage.DeleteTask(taskID)
    if err != nil {
        return err
    }
    
    // 2. Cancel/remove related jobs from queue
    err = queue.CancelJobsForTask(taskID)
    if err != nil {
        log.Warn("Failed to clean up queue jobs for deleted task", 
            "task_id", taskID, "error", err)
        // Don't fail the deletion, just log
    }
    
    return nil
}
```

**D. Queue Health Monitoring**
```go
// Add metrics for queue health:
func (w *Worker) reportQueueMetrics() {
    metrics := &QueueMetrics{
        TotalJobs: w.getTotalJobs(),
        FailedJobs: w.getFailedJobs(),
        OrphanedJobs: w.getOrphanedJobs(), // Jobs with "task not found"
        ProcessingRate: w.getProcessingRate(),
    }
    
    log.Info("Queue health report", 
        "total_jobs", metrics.TotalJobs,
        "failed_jobs", metrics.FailedJobs,
        "orphaned_jobs", metrics.OrphanedJobs,
        "processing_rate", metrics.ProcessingRate)
}
```

### 3. Implementation Priority

1. **Immediate**: Simplify the logging to reduce noise
2. **Short-term**: Implement DLQ for orphaned jobs
3. **Medium-term**: Add cleanup service for old orphaned jobs  
4. **Long-term**: Implement graceful task deletion with queue cleanup

This will significantly reduce log noise and prevent the queue from getting clogged with orphaned jobs that will never succeed.