# Node.js Sandbox Service

This document outlines the approach for extracting the custom code runner in EigenLayer-AVS into a separate sandbox service using real Node.js with Linux-specific sandboxing technologies.

## Architecture

The proposed architecture is a microservice that:
1. Receives JavaScript code and input data via HTTP/gRPC
2. Executes the code in a sandboxed Node.js environment
3. Returns the results to the main service

## Option 1: nsjail-based Sandbox

[nsjail](https://github.com/google/nsjail) is a lightweight process isolation tool built on Linux namespaces. It's used by Google's Cloud Functions and provides strong isolation with minimal overhead.

### Implementation

```go
// Example Go code for executing Node.js in nsjail
func RunSandboxedNodeJS(code string, inputs map[string]interface{}) (interface{}, error) {
    // Create temporary directory for the sandbox
    tempDir, err := os.MkdirTemp("", "nodejs-sandbox-")
    if err != nil {
        return nil, err
    }
    defer os.RemoveAll(tempDir)
    
    // Write code and inputs to files
    codeFile := filepath.Join(tempDir, "code.js")
    inputsFile := filepath.Join(tempDir, "inputs.json")
    
    if err := os.WriteFile(codeFile, []byte(code), 0644); err != nil {
        return nil, err
    }
    
    inputsJSON, err := json.Marshal(inputs)
    if err != nil {
        return nil, err
    }
    if err := os.WriteFile(inputsFile, inputsJSON, 0644); err != nil {
        return nil, err
    }
    
    // Execute Node.js with nsjail
    cmd := exec.Command(
        "nsjail",
        "--quiet",
        "--config", "/path/to/nsjail.config",
        "--bindmount", fmt.Sprintf("%s:/sandbox", tempDir),
        "--cwd", "/sandbox",
        "--",
        "/usr/bin/node", "runner.js", "code.js", "inputs.json",
    )
    
    output, err := cmd.CombinedOutput()
    if err != nil {
        return nil, fmt.Errorf("error running sandboxed Node.js: %v, output: %s", err, output)
    }
    
    // Parse and return the result
    var result interface{}
    if err := json.Unmarshal(output, &result); err != nil {
        return nil, err
    }
    
    return result, nil
}
```

### Security Considerations

- Process isolation through Linux namespaces
- Resource limits (CPU, memory, disk, network)
- Time limits for execution
- Read-only filesystem except for specific directories
- Limited system calls

## Option 2: Landlock-based Sandbox

[Landlock](https://landlock.io/) is a Linux security module that allows creating powerful sandboxes from unprivileged processes. It's more recent than nsjail but provides similar isolation capabilities.

### Implementation

Landlock requires Go bindings which can be implemented using:

```go
// Example Go code for Landlock sandboxing
// Note: This requires a more recent kernel and Go version
// Package github.com/landlock-lsm/go-landlock is needed

import (
    "github.com/landlock-lsm/go-landlock/landlock"
    "github.com/landlock-lsm/go-landlock/landlock/ruleset"
)

func RunLandlockSandboxedNodeJS(code string, inputs map[string]interface{}) (interface{}, error) {
    // Setup similar to nsjail example for files
    
    // Create Landlock ruleset
    rs := ruleset.New()
    
    // Add rules for access control
    if err := rs.AddPath("/usr/bin/node", ruleset.PathExec); err != nil {
        return nil, err
    }
    if err := rs.AddPath(tempDir, ruleset.PathReadWrite); err != nil {
        return nil, err
    }
    // Add other necessary paths with minimum privileges
    
    // Apply ruleset
    if err := rs.Restrict(); err != nil {
        return nil, err
    }
    
    // Now execute Node.js in the restricted environment
    cmd := exec.Command("/usr/bin/node", "runner.js", "code.js", "inputs.json")
    cmd.Dir = tempDir
    
    // Continue with execution as in the nsjail example
}
```

### Security Considerations

- Fine-grained filesystem access control
- Process isolation
- No need for root privileges
- Works on newer Linux kernels (5.13+)

## Option 3: Docker or Firecracker-based Isolation

For stronger isolation, a containerization or virtualization approach can be used.

### Docker Implementation

```go
func RunDockerSandboxedNodeJS(code string, inputs map[string]interface{}) (interface{}, error) {
    // Create temporary files for code and inputs
    
    // Run Docker container with Node.js
    cmd := exec.Command(
        "docker", "run",
        "--rm",
        "--network=none",  // No network access
        "--memory=128m",   // Memory limit
        "--cpus=0.1",      // CPU limit
        "-v", fmt.Sprintf("%s:/app:ro", tempDir),
        "node:slim",
        "node", "/app/runner.js", "/app/code.js", "/app/inputs.json",
    )
    
    // Continue with execution as in previous examples
}
```

### Firecracker MicroVM Implementation

[Firecracker](https://firecracker-microvm.github.io/) provides lightweight virtual machines that start in milliseconds and offer strong isolation. This would require a more complex setup but offers the strongest security isolation.

A full implementation would involve:
1. Setting up the Firecracker API
2. Creating a minimal Linux VM with Node.js
3. Injecting the code and inputs
4. Executing and retrieving results

### Security Considerations

- Container or VM isolation
- Complete resource isolation
- Strong security boundaries
- Higher overhead compared to namespace-based solutions

## Recommendations and Tradeoffs

| Solution     | Security | Performance | Complexity | Linux Specific | Requirements                              |
|--------------|----------|-------------|------------|----------------|-------------------------------------------|
| nsjail       | High     | High        | Medium     | Yes            | Linux kernel, nsjail binary               |
| Landlock     | High     | High        | Medium     | Yes            | Recent Linux kernel (5.13+)               |
| Docker       | High     | Medium      | Low        | No*            | Docker engine (works on macOS/Windows too)|
| Firecracker  | Highest  | Medium      | High       | Yes            | Linux kernel, KVM                         |

### Recommendation

For development environments that need to support macOS, the Docker approach provides the best compatibility while still offering good security. For production environments on Linux, nsjail provides an excellent balance of security, performance, and complexity.

If the highest level of isolation is required, Firecracker MicroVMs provide the strongest security boundaries but with higher complexity in implementation.

### Implementation Plan

1. Start with a Docker-based implementation for development
2. Add nsjail support for Linux production environments
3. Consider Firecracker if stronger isolation is needed in the future
