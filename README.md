# Ava Protocol Automation On Ethereum

The Ava Protocol AVS can be compiled directly using Go version 1.22+. Ensure you have the appropriate version of Go installed on your development environment.

Check Go version:

```
go version
```

Compile Ava Protocol AVS:

```
go build -o ap-avs
```

Then you can run `ap-avs` binary. We make an effort to use pure Go so you can also cross compile for any supported architecture that the Go compiler support.

## Run operator

Check how to run an [operator docs](docs/operator.md)

### Run aggregator

To run the aggregator, use the following command:

```
ap-avs aggregator
```

Note: The Ava Protocol team currently manages the aggregator, and the communication IP address between the operator and the aggregator is hardcoded in the operator.

# How it works

<table><tr><td bgcolor='white'><img src="docs/highlevel-diagram.png"/></td></tr></table>

## User wallet

For each owner we deploy a ERC6900 wallet to schedule task and approve spending
to user wallet.

Each task type has its equivalent modular code to re-present its condition
and their actual execution.

## Aggregator

Aggregator accepts RPC request from client to submit Task Payload. Currently, aggregator is managed and run by Ava Protocol team.

Periodically, aggregator combine the task submission, update our internal
storage and a zkSNARK proof will be write back to our TaskManager contract.

Aggregator also accept task condition check result from operator, perform quorum
and consensus check, then write the result back and flag that a task is good to
run.

### Aggregator Address

The aggregator is currently run and managed by the Ava Protocol team. Depend on
testnet or mainnet, you would need to point your operator to the right address
in the operator config file.

#### Holesky Testnet

- aggregator-holesky.avaprotocol.org:2206
- [https://api-explorer-holesky.avaprotocol.org/](https://api-explorer-holesky.avaprotocol.org/)

#### Mainnet

- aggregator.avaprotocol.org:2206
- [https://api-explorer.avaprotocol.org/](https://api-explorer.avaprotocol.org/)

## Operators

Operators communicates with aggregators through RPC. It requests task data from aggregator, it performs condition execution to check whether a task can be trigger. The result is then sent back to aggregator.

For task is ok to run, the operator will executed them. The detail of how task
is triggering through our ERC6900 modular wallet will come soon.

## Ava Protocol operator address

Currently, Ava Protocol has deployed our operator on the testnet. Community members can run their own operator and register for Ava Protocol AVS service, or they can delegate their tokens to the Ava Protocol operator.

### Testnet

- Ava Protocol's operator: [0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d](https://holesky.eigenlayer.xyz/operator/0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d).

### Mainnet

- Ava Protocol's operator: [0xc6B87cc9e85b07365b6aBEfff061F237F7cf7Dc3](https://etherscan.io/address/0xc6B87cc9e85b07365b6aBEfff061F237F7cf7Dc3)

## Telemetry Dashboard

Operator that is connected to Ava Protocol aggregator can also check their
operator on our telemetry dashboard as below

### Testnet

https://aggregator-holesky.avaprotocol.org/telemetry

### Mainnet

https://aggregator.avaprotocol.org/telemetry

## Migration Guide

Before merging changes from `staging` to `main`, ensure any storage structure changes are properly migrated:

1. **Check for Storage Changes**
   ```bash
   # First checkout staging branch
   git checkout staging
   
   # Compare with main to detect storage changes
   go run scripts/compare_storage_structure.go main
   ```

2. **When Migration is Needed**
   - Storage key format changes
   - Non-backward-compatible data structure changes
   - Required field additions (without `omitempty`)
   - Field type changes
   - Field removals

3. **Migration Process**
   - Create migration file in `./migrations` (e.g., `YYYYMMDD-HHMMSS-description.go`)
   - Implement migration following existing patterns
   - Add to `Migrations` slice in `./migrations/migrations.go`
   - Test thoroughly before merging to `main`

4. **No Migration Needed For**
   - Adding fields with `omitempty` JSON tags
   - Runtime-only changes
   - Backward-compatible modifications

> **Important**: Always run migrations before merging to `main`. The script will warn if changes require migration.

# Development guide

View docs/development.md

## Testing

### Standard Tests

The Makefile includes two primary test configurations:

```bash
# Default test suite
go test -race -buildvcs -vet=off ./...

# Verbose test output
go test -v -race -buildvcs ./...
```

### Enhanced Test Output

For improved test result formatting, use `gotestfmt`:

1. Install the formatter:

   ```bash
   go install github.com/gotesttools/gotestfmt/v2/cmd/gotestfmt@latest
   ```

2. Run once in the current terminal session to make Bash scripts more robust and error-aware.

   ```bash
   set -euo pipefail
   ```

3. Run tests with formatted output:

   Run all tests with complete output:

   ```base
   go test -v ./...
   ```

   or, run selected test cases:

   ```bash
   go test -json -run ^TestRestRequestErrorHandling$ ./... 2>&1 | gotestfmt --hide=all
   ```

   The `--hide=all` flag suppresses output for skipped and successful tests, showing only failures. For more output configuration options, see the [gotestfmt documentation](https://github.com/GoTestTools/gotestfmt?tab=readme-ov-file#how-do-i-make-the-output-less-verbose).

=======

## Linting and Code Quality

### Running the linter

```bash
# Install golangci-lint
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin v1.55.2

# Run the linter
golangci-lint run ./...

# Or use the Makefile target
make audit
```

### Best practices for running linters

- Run linters before committing code to catch issues early
- Configure your IDE to run linters on save for immediate feedback
- Include linting in CI/CD pipelines to enforce code quality standards
- Fix linting issues as they arise rather than letting them accumulate

## Dependencies

## Update Protobuf Definitions

When you modify any `.proto` files (primarily `protobuf/avs.proto` or `protobuf/node.proto`), you need to regenerate the corresponding Go bindings.

### Prerequisites

1.  **Install `protoc` (the protobuf compiler):**
    If you don't have it, download it from the [protobuf releases page](https://github.com/protocolbuffers/protobuf/releases) and ensure it's in your system's `PATH`.

2.  **Install Go plugins for `protoc`:**
    These commands will install the necessary Go code generators for protobuf messages and gRPC services. Ensure your `$GOPATH/bin` or `$HOME/go/bin` is in your system `PATH`.
    ```bash
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
    ```

### Generation Command

After installing the prerequisites, run the following Make target from the project root:

```bash
make protoc-gen
```

### Expected File Structure & Go Package

*   **Proto source files:** Reside in the `protobuf/` directory (e.g., `protobuf/avs.proto`, `protobuf/node.proto`).
*   **`go_package` option:** Both `avs.proto` and `node.proto` use `option go_package = "github.com/AvaProtocol/EigenLayer-AVS/protobuf;avsproto";`.
    *   This directs `protoc-gen-go` to generate files that will be part of the Go package aliased as `avsproto`.
    *   The import path for this package in your Go code will be `github.com/AvaProtocol/EigenLayer-AVS/protobuf` (assuming `github.com/AvaProtocol/EigenLayer-AVS` is your module name from `go.mod`).
*   **Generated Go files:** The `make protoc-gen` command is configured (via `--go_out=.` and the `go_package` option) to place the generated `*.pb.go` and `*_grpc.pb.go` files directly into the `protobuf/` directory.
    *   Example: `protobuf/avs.pb.go`, `protobuf/node.pb.go`.
    *   All these generated files will contain the Go package declaration: `package avsproto`.

### Using the Generated Code in Go

To use the generated types and gRPC clients/servers in your Go code, import the package as follows:

```go
import (
    // ... other imports
    avspb "github.com/AvaProtocol/EigenLayer-AVS/protobuf" // Use a suitable alias like avspb
)

func main() {
    req := &avspb.IdReq{Id: "test-id"}
    // ... use other types like avspb.Task, avspb.AggregatorClient, etc.
}
```

**Important:** If you change the `go_package` option in the `.proto` files or the output paths in the `Makefile`, you will likely need to update the import paths in your Go codebase accordingly.

## Getting started

Coming soon

## Contract address

### Holesky Testnet

| Name                   | Address                                                                                                                         |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| ProxyAdmin             | [`0x26CF7A7DF7d1E00D83A5Ca24385f697a3ca4577d`](https://holesky.etherscan.io/address/0x26CF7A7DF7d1E00D83A5Ca24385f697a3ca4577d) |
| ServiceManager         | [`0xEA3E82F9Ae371A6a372A6DCffB1a9bD17e0608eF`](https://holesky.etherscan.io/address/0xEA3E82F9Ae371A6a372A6DCffB1a9bD17e0608eF) |
| RegistryCoordinator    | [`0x90c6d6f2A78d5Ce22AB8631Ddb142C03AC87De7a`](https://holesky.etherscan.io/address/0x90c6d6f2A78d5Ce22AB8631Ddb142C03AC87De7a) |
| BLSApkRegistry         | [`0x6752F8BeeE5BF45c9d11FDBC4F8aFfF879925585`](https://holesky.etherscan.io/address/0x6752F8BeeE5BF45c9d11FDBC4F8aFfF879925585) |
| IndexRegistry          | [`0x298a5d3C8F8Db30E8292C9e2BF92292de469C8FF`](https://holesky.etherscan.io/address/0x298a5d3C8F8Db30E8292C9e2BF92292de469C8FF) |
| OperatorStateRetriever | [`0xb7bb920538e038DFFEfcB55caBf713652ED2031F`](https://holesky.etherscan.io/address/0xb7bb920538e038DFFEfcB55caBf713652ED2031F) |
| PauserRegistry         | [`0x3A8ea6e4202CdDe4a9e0cCE19c4Dc1739ba2cF0b`](https://holesky.etherscan.io/address/0x3A8ea6e4202CdDe4a9e0cCE19c4Dc1739ba2cF0b) |
| StakeRegistry          | [`0x7BacD5dd5A7C3acf8bf1a3c88fB0D00B68EE626A`](https://holesky.etherscan.io/address/0x7BacD5dd5A7C3acf8bf1a3c88fB0D00B68EE626A) |
| ApConfig               | [`0xb8abbb082ecaae8d1cd68378cf3b060f6f0e07eb`](https://holesky.etherscan.io/address/0xb8abbb082ecaae8d1cd68378cf3b060f6f0e07eb) |

### Ethereum Mainnet

| Name                   | Address                                                                                                                 |
| ---------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| ProxyAdmin             | [`0x5989934D31f7f397511f105B7E4175a06B7A517F`](https://etherscan.io/address/0x5989934D31f7f397511f105B7E4175a06B7A517F) |
| ServiceManager         | [`0x18343Aa10e3D2F3A861e5649627324aEAD987Adf`](https://etherscan.io/address/0x18343Aa10e3D2F3A861e5649627324aEAD987Adf) |
| RegistryCoordinator    | [`0x8DE3Ee0dE880161Aa0CD8Bf9F8F6a7AfEeB9A44B`](https://etherscan.io/address/0x8DE3Ee0dE880161Aa0CD8Bf9F8F6a7AfEeB9A44B) |
| BLSApkRegistry         | [`0xB58687fF303C8e92C28a484342755d3228081d45`](https://etherscan.io/address/0xB58687fF303C8e92C28a484342755d3228081d45) |
| IndexRegistry          | [`0xc6A464e39d4fA5013D61295501c7cCd050d76612`](https://etherscan.io/address/0xc6A464e39d4fA5013D61295501c7cCd050d76612) |
| OperatorStateRetriever | [`0xb3af70D5f72C04D1f490ff49e5aB189fA7122713`](https://etherscan.io/address/0xb3af70D5f72C04D1f490ff49e5aB189fA7122713) |
| PauserRegistry         | [`0xeec585186c37c517030ba371deac5c17e728c135`](https://etherscan.io/address/0xeec585186c37c517030ba371deac5c17e728c135) |
| StakeRegistry          | [`0x363b3604fE8c2323a98c00906115c8b87a512a12`](https://etherscan.io/address/0x363b3604fE8c2323a98c00906115c8b87a512a12) |
| TaskManager            | [`0x940f62f75cbbbd723d37c9171dc681dfba653b49`](https://etherscan.io/address/0x940f62f75cbbbd723d37c9171dc681dfba653b49) |
| ApConfig               | [`0x9c02dfc92eea988902a98919bf4f035e4aaefced`](https://etherscan.io/address/0x9c02dfc92eea988902a98919bf4f035e4aaefced) |
