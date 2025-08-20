# Production Troubleshooting Guide

This guide provides step-by-step instructions for troubleshooting issues in production environments, particularly for workflow execution failures and system connectivity problems.

## Production Infrastructure

### Production Servers

| Environment | Server | SSH Access | Description |
|-------------|--------|------------|-------------|
| Production | `ap-prod1` | `ssh ap-prod1` | Main production server |

### Docker Containers

| Container Name | Service Type | Purpose |
|----------------|--------------|---------|
| `aggregator-base` | Aggregator | Handles workflow scheduling and coordination |
| `operator-base` | Operator | Executes workflow tasks and maintains connection to aggregator |

### Network Endpoints

| Environment | Aggregator RPC | API Explorer | Telemetry |
|-------------|----------------|--------------|-----------|
| Sepolia | `aggregator-sepolia.avaprotocol.org:2206` | https://api-explorer-sepolia.avaprotocol.org/ | https://aggregator-sepolia.avaprotocol.org/telemetry |
| Mainnet | `aggregator.avaprotocol.org:2206` | https://api-explorer.avaprotocol.org/ | https://aggregator.avaprotocol.org/telemetry |

## Common Troubleshooting Scenarios

### 1. Workflow Failed to Trigger

**Symptoms:**
- Workflow shows as "active" but execution count remains 0
- No executions found in workflow history
- Cron-triggered workflows not running at expected times

**Investigation Steps:**

1. **Extract Workflow Details**
   ```bash
   # Navigate to ava-sdk-js project directory
   cd /path/to/ava-sdk-js/examples
   
   # Run workflow examination command to get detailed workflow information
   yarn start --avs-target=base examineWorkflow WORKFLOW_ID
   
   # This generates a sanitized log file with workflow details including:
   # - Workflow ID and configuration
   # - Start timestamp (startAt)
   # - Cron schedule string
   # - Execution history and status
   # - Expected trigger times
   
   # Example:
   # yarn start --avs-target=base examineWorkflow 01K336QJWNA3VFE29RR32N75WB
   # Output: examineWorkflow.WORKFLOW_ID.sanitized.log
   ```

2. **Check Aggregator Logs**
   ```bash
   # Connect to production server
   ssh ap-prod1
   
   # Check recent aggregator logs for the workflow
   docker logs aggregator-base --since 'YYYY-MM-DDTHH:MM:SS' | grep -i -E '(WORKFLOW_ID|cron|schedule|trigger)'
   
   # Check for workflow creation success
   docker logs aggregator-base --since 'YYYY-MM-DDTHH:MM:SS' | grep -E '(CreateTask|✅.*completed successfully)'
   
   # Look for cron scheduler activity
   docker logs aggregator-base --since 'YYYY-MM-DDTHH:MM:SS' | grep -i -E '(scheduler|cron.*trigger|schedule.*task)'
   ```

3. **Check Operator Logs**
   ```bash
   # Check operator connectivity and task processing
   docker logs operator-base --since 'YYYY-MM-DDTHH:MM:SS' | grep -i -E '(cron|schedule|trigger|WORKFLOW_ID)'
   
   # Look for connection issues
   docker logs operator-base --tail 50 | grep -i -E '(connection|disconnect|reconnect|error)'
   ```

**Common Root Causes:**

- **Operator-Aggregator Connection Issues**: Operator unable to maintain stable connection
- **Cron Scheduler Not Running**: No cron scheduler logs indicate scheduling service may be down
- **Network Connectivity Problems**: Firewall or network issues preventing communication
- **Resource Exhaustion**: High memory/CPU usage preventing task processing

### 2. Connection and Communication Issues

**Check Container Status:**
```bash
# Verify containers are running
docker ps | grep -E '(aggregator|operator)'

# Check container resource usage
docker stats aggregator-base operator-base --no-stream
```

**Network Connectivity Tests:**
```bash
# Test aggregator endpoint
curl -X POST https://aggregator.avaprotocol.org:2206/health

# Test from operator container
docker exec operator-base curl -X POST http://aggregator-base:2206/health
```

### 3. Database and Storage Issues

**Check Storage Health:**
```bash
# Connect to aggregator REPL for database inspection
telnet /tmp/ap.sock

# List all active workflows
list t:a:*

# Get specific workflow details
get t:a:WORKFLOW_ID

# Check for storage corruption
gc
```

**Storage Commands:**
- `list *` - List all keys
- `get <key>` - Get specific value
- `rm <prefix>*` - Delete keys with prefix
- `backup <directory>` - Create database backup

## Workflow Analysis Tools

### Workflow Examination Command

The `examineWorkflow` command in the ava-sdk-js project is essential for gathering detailed workflow information:

```bash
# Navigate to the SDK project
cd /path/to/ava-sdk-js/examples

# Examine a workflow on Base AVS
yarn start --avs-target=base examineWorkflow WORKFLOW_ID

# Examine a workflow on Sepolia testnet
yarn start --avs-target=sepolia examineWorkflow WORKFLOW_ID

# Examine a workflow on Ethereum mainnet
yarn start --avs-target=ethereum examineWorkflow WORKFLOW_ID
```

**Output Information:**
- Complete workflow configuration and metadata
- Trigger details (cron schedule, start time, etc.)
- Execution history and status
- Node configurations and data flow
- Sanitized logs saved to: `examineWorkflow.WORKFLOW_ID.sanitized.log`

**Target Environments:**
- `base` - Base AVS production environment
- `sepolia` - Sepolia testnet environment  
- `ethereum` - Ethereum mainnet environment

**Usage Example:**
```bash
cd /Users/mikasa/Code/ava-sdk-js/examples
yarn start --avs-target=base examineWorkflow 01K336QJWNA3VFE29RR32N75WB

# Output:
# Current environment is: base endpoint: aggregator-base.avaprotocol.org:3206
# Wrote sanitized log: examineWorkflow.01K336QJWNA3VFE29RR32N75WB.sanitized.log
```

## Log Analysis Techniques

### Time-based Log Filtering

```bash
# Get logs for specific time range
docker logs aggregator-base --since '2025-08-20T00:30:00' --until '2025-08-20T02:00:00'

# Get recent logs with tail
docker logs aggregator-base --tail 100

# Follow live logs
docker logs aggregator-base --follow
```

### Pattern Matching

```bash
# Search for workflow-specific logs
docker logs aggregator-base | grep -E '(WORKFLOW_ID|specific-pattern)'

# Case-insensitive search
docker logs aggregator-base | grep -i -E '(error|fail|exception)'

# Multiple pattern search
docker logs aggregator-base | grep -E '(cron|schedule|trigger)' | head -20
```

### Log Correlation

When troubleshooting, correlate logs across services:

1. **Aggregator Logs**: Look for workflow creation, scheduling, and coordination
2. **Operator Logs**: Check for task execution, connection status, and errors
3. **Timestamp Alignment**: Ensure you're checking the right time windows

## Performance Monitoring

### Container Resource Usage

```bash
# Check real-time resource usage
docker stats aggregator-base operator-base

# Check container logs for memory/CPU issues
docker logs aggregator-base | grep -i -E '(memory|cpu|resource|performance)'
```

### Connection Health

```bash
# Check active connections
docker exec aggregator-base netstat -an | grep :2206

# Monitor connection attempts
docker logs operator-base | grep -i connect
```

## Emergency Procedures

### Service Restart

```bash
# Restart specific container
docker restart aggregator-base
docker restart operator-base

# Restart all services
docker compose restart
```

### Log Collection

```bash
# Collect logs for analysis
docker logs aggregator-base --since '24h' > aggregator-logs.txt
docker logs operator-base --since '24h' > operator-logs.txt

# Create timestamped log collection
mkdir logs-$(date +%Y%m%d-%H%M%S)
docker logs aggregator-base --since '24h' > logs-$(date +%Y%m%d-%H%M%S)/aggregator.log
docker logs operator-base --since '24h' > logs-$(date +%Y%m%d-%H%M%S)/operator.log
```

## Preventive Monitoring

### Health Checks

Set up regular health checks:

```bash
# Aggregator health
curl -f https://aggregator.avaprotocol.org:2206/health || echo "Aggregator down"

# Operator connectivity (from operator container)
docker exec operator-base curl -f http://aggregator-base:2206/health || echo "Connection failed"
```

### Log Monitoring

Monitor for critical patterns:

```bash
# Watch for connection issues
docker logs operator-base --follow | grep -i "connection\|disconnect\|error"

# Monitor workflow execution
docker logs aggregator-base --follow | grep -E "CreateTask\|ExecuteTask\|✅.*completed"
```

## Development vs Production Differences

| Aspect | Development | Production |
|--------|-------------|------------|
| Log Level | DEBUG/VERBOSE | INFO/WARN |
| Container Names | Local names | `aggregator-base`, `operator-base` |
| Network | Local Docker | Production infrastructure |
| Storage | Local volumes | Persistent production storage |
| Monitoring | Manual | Automated alerts |

## Useful Commands Reference

### Docker Operations
```bash
# View container logs
docker logs <container-name> [--since TIME] [--until TIME] [--tail N] [--follow]

# Execute commands in container
docker exec -it <container-name> <command>

# Check container status
docker ps -a

# Resource usage
docker stats <container-name>
```

### Log Analysis
```bash
# Pattern search
grep -E 'pattern1|pattern2' logfile
grep -i 'case-insensitive' logfile

# Time-based filtering
grep 'YYYY-MM-DD HH:MM' logfile

# Count occurrences
grep -c 'pattern' logfile
```

### Network Debugging
```bash
# Test connectivity
curl -X POST <endpoint>
telnet <host> <port>

# Check open ports
netstat -an | grep <port>
```

## Security Notes

- **Never include credentials** in troubleshooting documentation
- Use environment variables or secure configuration files for sensitive data
- SSH access should be properly authenticated and logged
- Log files may contain sensitive information - handle appropriately

## Getting Help

When escalating issues, provide:

1. **Workflow ID** and relevant timestamps
2. **Relevant log snippets** (sanitized of sensitive data)
3. **Steps already taken** during troubleshooting
4. **Current system status** (container health, resource usage)
5. **Expected vs actual behavior**

For additional support, consult:
- [Development Guide](Development.md)
- [Operator Documentation](Operator.md)
- [Protocol Documentation](Protocol.md)
