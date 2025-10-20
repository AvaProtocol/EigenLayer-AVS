# Account Abstraction (AA) Package

## ABI Files and Go Bindings

This package contains smart contract ABIs and their generated Go bindings for Account Abstraction contracts.

### Structure

Each ABI file lives alongside its generated Go binding:

- `account.abi` → `aa.go` (embedded for calldata packing) + `simpleaccount/simpleaccount.go`
- `entrypoint.abi` → `entrypoint.go`
- `factory.abi` → `simplefactory.go`
- `paymaster.abi` → `paymaster/paymaster.go`

### How It Works

**1. Generated Go Bindings (for contract calls)**

The `*.go` files are generated using `abigen` and provide type-safe contract interaction:
- `NewEntryPoint()` - for nonce management, deposits
- `NewSimpleFactory()` - for deriving smart wallet addresses
- `NewSimpleAccount()` - for account operations
- `NewPayMaster()` - for paymaster interactions

These bindings are **committed to git** and used throughout the codebase.

**2. Embedded ABI (for calldata encoding)**

The `account.abi` file is embedded at compile-time using `//go:embed` in `aa.go`. This is used by the `PackExecute*` functions to manually encode calldata for complex batch operations that require precise ABI encoding control.

## Updating ABIs

When contract ABIs change:

1. **Replace the ABI file** in this directory
2. **Regenerate Go bindings** using `abigen` manually:
   ```bash
   abigen --abi=account.abi --pkg=simpleaccount --type=SimpleAccount --out=simpleaccount/simpleaccount.go
   abigen --abi=entrypoint.abi --pkg=aa --type=EntryPoint --out=entrypoint.go
   abigen --abi=factory.abi --pkg=aa --type=SimpleFactory --out=simplefactory.go
   abigen --abi=paymaster.abi --pkg=paymaster --type=PayMaster --out=paymaster/paymaster.go
   ```
3. **Commit both** the ABI file and updated bindings
4. **Test** contract interactions to verify compatibility

> **Note:** ABI files are manually managed. Each ABI lives with its Go binding - no separate `abis/` directory needed.
