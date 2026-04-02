# Phase 1: Calibur Smart Wallet — Protobuf & RPC Interface Plan

## Goal

Add Calibur (EIP-7702) wallet support alongside the existing salt-derived SimpleAccount wallets. Both wallet types coexist — no breaking changes to existing RPCs. Users can create tasks against either wallet type.

See `avs-infra/EIP-7702-Smart-Wallet-Migration.md` for the full decision rationale.

---

## Current State

### Wallet Identity Model

Every wallet is identified by `(owner, factory, salt)` → derived contract address via `factory.getAddress(owner, salt)`:

```
owner EOA ──┬── salt=0 ──→ smart wallet 0x...A
            ├── salt=1 ──→ smart wallet 0x...B
            └── salt=2 ──→ smart wallet 0x...C
```

The wallet address is a **separate contract account**, not the user's EOA.

### Current Protobuf Messages

```protobuf
// avs.proto (current)
message SmartWallet {
  string address = 1;
  string salt = 2;
  string factory = 3;
  bool is_hidden = 4;
}

message GetWalletReq {
  string salt = 1;
  string factory_address = 2;
}

message CreateTaskReq {
  // ...
  string smart_wallet_address = 5;  // which wallet executes this task
}
```

### Current Data Model

```go
// model/user.go (current)
type SmartWallet struct {
  Owner    *common.Address
  Address  *common.Address
  Factory  *common.Address
  Salt     *big.Int
  IsHidden bool
}
```

### Current Storage Keys

```
w:{owner}:{wallet_address}          → wallet metadata (JSON)
u:{owner}:{wallet_address}:{task_id} → task storage prefix
```

---

## Phase 1 Design

### New Wallet Identity Model

Calibur wallets have no factory or salt — the wallet address IS the user's EOA:

```
owner EOA ──┬── [basic] salt=0 ──→ smart wallet 0x...A (contract account)
            ├── [basic] salt=1 ──→ smart wallet 0x...B (contract account)
            └── [calibur] ──────→ 0x...EOA itself (delegated via 7702)
```

A user can have at most one Calibur wallet (their EOA). They can still have multiple basic salt-derived wallets.

### Key Design Decisions

1. **Wallet type is an enum, not a boolean.** Future account implementations (Safe, Kernel) become new enum values, not new fields.

2. **Calibur wallets don't use factory/salt.** These fields are empty/zero for Calibur wallets. Wallet type determines how the address is resolved.

3. **Existing RPCs continue working unchanged.** `GetWallet(salt, factory)` still returns basic wallets. Calibur wallets are accessed through new RPCs or a type-aware version of existing ones.

4. **Task execution branches on wallet type.** The `smart_wallet_address` in `CreateTaskReq` can point to either wallet type. The execution pipeline checks the type to determine whether to use the basic SimpleAccount path or the Calibur path.

5. **One Calibur wallet per owner.** Since the wallet IS the EOA, there's no salt/factory variation. The aggregator enforces uniqueness.

---

## Protobuf Changes

### New Enum: WalletType

```protobuf
enum WalletType {
  WALLET_TYPE_UNSPECIFIED = 0;
  WALLET_TYPE_BASIC = 1;        // Salt-derived SimpleAccount (current behavior)
  WALLET_TYPE_CALIBUR = 2;      // EIP-7702 delegated EOA via Calibur
}
```

### Updated Message: SmartWallet

```protobuf
message SmartWallet {
  string address = 1;
  string salt = 2;             // Basic only; empty for Calibur
  string factory = 3;          // Basic only; empty for Calibur
  bool is_hidden = 4;
  WalletType wallet_type = 5;  // NEW — defaults to BASIC for backward compat
}
```

Backward compatibility: existing clients that don't send `wallet_type` get `WALLET_TYPE_UNSPECIFIED` (0), which the server treats as `WALLET_TYPE_BASIC`.

### New Message: CaliburKeyRegistration

Tracks the sub-key that the aggregator registered on the user's Calibur wallet.

```protobuf
message CaliburKeyRegistration {
  string public_key = 1;       // Aggregator's sub-key public key (hex)
  string key_hash = 2;         // Calibur keyHash for this key
  string hook_address = 3;     // Permission hook contract address
  uint64 expiry = 4;           // Key expiry (unix timestamp, 0 = no expiry)
  string status = 5;           // "pending_delegation", "pending_key_registration", "active", "revoked"
}
```

### New Messages: Calibur Wallet RPCs

```protobuf
// Request to register user's EOA as a Calibur wallet.
// Precondition: user has already delegated their EOA to Calibur via EIP-7702.
message RegisterCaliburWalletReq {
  // No fields needed — the owner EOA is extracted from the auth context.
  // The aggregator verifies on-chain that the EOA has delegated to Calibur.
}

message RegisterCaliburWalletResp {
  string address = 1;                       // The user's EOA (= wallet address)
  CaliburKeyRegistration key_info = 2;      // Sub-key registration details
  WalletType wallet_type = 3;               // Always WALLET_TYPE_CALIBUR
}

// Request to check Calibur delegation and key status for user's EOA.
message GetCaliburWalletReq {
  // No fields — uses auth context.
}

message GetCaliburWalletResp {
  string address = 1;                       // User's EOA
  bool is_delegated = 2;                    // Whether EOA has active 7702 delegation to Calibur
  CaliburKeyRegistration key_info = 3;      // Sub-key status (nil if no key registered)
  WalletType wallet_type = 4;
  bool is_hidden = 5;
  uint64 total_task_count = 6;
  uint64 enabled_task_count = 7;
  uint64 completed_task_count = 8;
  uint64 failed_task_count = 9;
  uint64 disabled_task_count = 10;
}

// Revoke the aggregator's sub-key from the user's Calibur wallet.
message RevokeCaliburKeyReq {
  // No fields — uses auth context. Revokes the aggregator's registered key.
}

message RevokeCaliburKeyResp {
  bool success = 1;
  string message = 2;
}
```

### Updated Messages: Existing RPCs (Backward-Compatible)

**ListWalletReq** — add optional type filter:

```protobuf
message ListWalletReq {
  string factory_address = 1;
  string salt = 2;
  WalletType wallet_type = 3;  // NEW — optional filter; 0 (UNSPECIFIED) returns all types
}
```

**ListWalletResp** — unchanged (SmartWallet already gains `wallet_type` field):

```protobuf
message ListWalletResp {
  repeated SmartWallet items = 1;  // Now includes Calibur wallets with wallet_type set
}
```

**GetWalletReq / GetWalletResp** — unchanged. These RPCs remain basic-only (salt/factory based). Calibur wallets use `GetCaliburWallet`.

**WithdrawFundsReq** — unchanged. The `smart_wallet_address` field works for both wallet types. The server checks the wallet type in the DB to route to the correct execution path (basic UserOp via controller key vs Calibur UserOp via sub-key).

**CreateTaskReq** — unchanged. The `smart_wallet_address` can be a basic wallet or the user's EOA (Calibur). The execution engine resolves the wallet type from storage.

### New Error Codes

```protobuf
enum ErrorCode {
  // ... existing codes ...

  // 8100-8199: Calibur-specific errors
  CALIBUR_NOT_DELEGATED = 8100;         // EOA has not delegated to Calibur contract
  CALIBUR_KEY_ALREADY_REGISTERED = 8101; // Aggregator sub-key already exists for this wallet
  CALIBUR_KEY_NOT_FOUND = 8102;          // No aggregator sub-key registered
  CALIBUR_KEY_EXPIRED = 8103;            // Sub-key has expired
  CALIBUR_KEY_REVOKED = 8104;            // Sub-key has been revoked
  CALIBUR_HOOK_VALIDATION_FAILED = 8105; // Hook rejected the operation
  CALIBUR_WALLET_ALREADY_EXISTS = 8106;  // User already has a Calibur wallet registered
}
```

---

## RPC Service Changes

```protobuf
service Aggregator {
  // ... existing RPCs (unchanged) ...

  // Calibur wallet management
  rpc RegisterCaliburWallet(RegisterCaliburWalletReq) returns (RegisterCaliburWalletResp) {};
  rpc GetCaliburWallet(GetCaliburWalletReq) returns (GetCaliburWalletResp) {};
  rpc RevokeCaliburKey(RevokeCaliburKeyReq) returns (RevokeCaliburKeyResp) {};
}
```

---

## Data Model Changes

### model/user.go

```go
type SmartWallet struct {
  Owner      *common.Address `json:"owner"`
  Address    *common.Address `json:"address"`
  Factory    *common.Address `json:"factory,omitempty"`    // nil for Calibur
  Salt       *big.Int        `json:"salt,omitempty"`       // nil for Calibur
  IsHidden   bool            `json:"is_hidden,omitempty"`
  WalletType int32           `json:"wallet_type"`          // 1=basic, 2=calibur
}
```

### New model: CaliburKeyInfo

```go
type CaliburKeyInfo struct {
  PublicKey   string          `json:"public_key"`
  KeyHash     string          `json:"key_hash"`
  HookAddress *common.Address `json:"hook_address"`
  Expiry      uint64          `json:"expiry"`
  Status      string          `json:"status"`  // pending_delegation, pending_key_registration, active, revoked
}
```

### Storage Keys

Calibur wallets use the same key schema — the wallet address is the EOA:

```
w:{owner}:{owner_eoa_address}       → wallet metadata (wallet_type=2)
u:{owner}:{owner_eoa_address}:{task} → task storage prefix

calibur_key:{owner}                  → CaliburKeyInfo (JSON)
```

Since owner == wallet address for Calibur, the storage key `w:{owner}:{owner}` is valid and unique.

---

## Config Changes

### core/config/config.go

```go
type CaliburConfig struct {
  // Calibur singleton address (same on all chains)
  CaliburAddress    common.Address  // 0x000000009B1D0aF20D8C6d0A44e162d11F9b8f00

  // Permission hook contract (deployed per chain by us)
  HookAddress       common.Address

  // RPC endpoint for submitting direct transactions (standard eth_sendRawTransaction)
  EthRpcUrl         string
}
```

```yaml
# config/aggregator.yaml
calibur:
  calibur_address: "0x000000009B1D0aF20D8C6d0A44e162d11F9b8f00"
  hook_address: "0x..."          # our permission hook contract
  eth_rpc_url: "https://..."     # standard RPC for direct tx submission
```

No EntryPoint or bundler config needed for Calibur — execution uses direct signed transactions (see Execution Pipeline below).

The existing `smart_wallet` config block remains unchanged for basic wallets.

---

## Execution Pipeline Branching

### Calibur uses direct transactions, not ERC-4337

Calibur's `ERC4337Account.sol` targets **EntryPoint v0.8** (`0x4337084D9E255Ff0702461CF8895CE9E3b5Ff108`) — it is explicitly not compatible with v0.6. However, Calibur also supports a **signed relay execution path** that bypasses the EntryPoint entirely:

```solidity
// Calibur.sol — direct execution, no EntryPoint needed
function execute(
    SignedBatchedCall calldata signedBatchedCall,
    bytes calldata wrappedSignature
) public payable
```

This function:
1. Verifies `wrappedSignature` is valid for the specified `signedBatchedCall.keyHash`
2. Checks the key is registered and not expired
3. Validates the nonce is unused
4. Calls the hook (if attached to the key)
5. Executes the batched calls

**Anyone can submit this transaction** — your aggregator signs the `SignedBatchedCall` with the sub-key off-chain, then submits it as a regular `eth_sendRawTransaction`. No bundler, no EntryPoint, no UserOp format.

All Calibur execution paths:

| Path | Function | Requires EntryPoint? | Who calls? |
|---|---|---|---|
| Direct owner | `execute(BatchedCall)` | No | EOA owner (root key) |
| **Signed relay** | `execute(SignedBatchedCall, signature)` | **No** | **Anyone — our aggregator submits a regular tx** |
| ERC-7821 | `execute(bytes32, bytes)` | No | EOA owner (root key) |
| 4337 validate | `validateUserOp(...)` | Yes (v0.8 only) | Only EntryPoint |

**We use the signed relay path.** This means:
- No bundler upgrade needed (v0.6 bundler continues serving basic wallets only)
- No EntryPoint v0.7/v0.8 dependency for Calibur
- No paymaster needed for Calibur (see below)

### No paymaster or reimbursement for Calibur wallets

The paymaster reimbursement pattern exists on basic wallets to work around a v0.6 ERC-4337 limitation: **gas fee calculation is hard to get right** with the UserOp flow. The bundler estimates gas, the paymaster fronts the cost, and then the smart wallet atomically reimburses the paymaster operator's EOA via `executeBatchWithValues()`. This is a workaround — the user's wallet has sufficient balance, but accurately pre-calculating the exact gas cost through the EntryPoint → bundler → paymaster pipeline is unreliable.

With Calibur's direct transaction path, this problem disappears:
- The aggregator submits a standard Ethereum transaction via `eth_sendRawTransaction`
- Gas estimation uses standard `eth_estimateGas` — well-understood, reliable
- The user's EOA pays gas directly from its own ETH balance (it IS the wallet)
- No paymaster, no reimbursement wrapping, no `executeBatchWithValues` needed

### Where the branch happens

In `core/taskengine/vm_runner_contract_write.go`, the execution path branches based on wallet type:

```
ContractWriteProcessor.Execute()
  │
  ├── wallet_type == BASIC
  │     └── existing path: preset.SendUserOp() with v0.6 EntryPoint
  │         - controller key signs UserOp
  │         - SimpleAccount.execute() calldata
  │         - v0.6 bundler
  │         - paymaster + reimbursement wrapping
  │
  └── wallet_type == CALIBUR
        └── new path: calibur.SendSignedBatchedCall()
            - sub-key signs SignedBatchedCall off-chain
            - submit as regular eth_sendRawTransaction
            - no bundler, no EntryPoint, no paymaster
            - Calibur validates signature + hook on-chain
```

### What calibur.SendSignedBatchedCall needs (new module)

- Build `SignedBatchedCall` struct: target(s), value(s), calldata(s), nonce, executor address
- Sign with the aggregator's Calibur sub-key (ECDSA)
- Wrap in a regular Ethereum transaction calling `Calibur.execute(signedBatchedCall, wrappedSignature)`
- Submit via `eth_sendRawTransaction` to standard RPC
- Wait for transaction receipt (standard `eth_getTransactionReceipt` polling)
- Gas estimation via `eth_estimateGas` on the execute call
- The aggregator's infrastructure wallet pays tx gas (it's the `msg.sender`), but the user's EOA pays for value transfers within the batched calls

---

## Registration Flow (Sequence)

```
1. User delegates EOA to Calibur
   ├── User signs EIP-7702 Type 4 tx in their wallet
   ├── Sets EOA code to: 0xef0100 || 0x000000009B1D0aF20D8C6d0A44e162d11F9b8f00
   └── This is done client-side (SDK/frontend), not by our backend

2. User calls RegisterCaliburWallet RPC
   ├── Server extracts owner EOA from auth context
   ├── Server checks on-chain: does EOA have 7702 delegation to Calibur?
   │     └── eth_getCode(eoa) should return 0xef0100 || calibur_address
   ├── If not delegated → return CALIBUR_NOT_DELEGATED error
   ├── Server generates ECDSA keypair for this user
   ├── Server stores private key in secure storage (keyed by owner)
   └── Server returns public key + instructions for key registration

3. User registers aggregator's sub-key on their Calibur wallet
   ├── User calls Calibur.register(keyType, publicKey, expiry, hookAddress)
   │     └── This is a self-call on their EOA (now has Calibur code)
   ├── Calibur stores the key in keySettings[keyHash]
   └── User confirms registration to our backend (or we detect it on-chain)

4. Server marks Calibur wallet as "active"
   ├── Updates CaliburKeyInfo status to "active"
   ├── Stores wallet in DB: w:{owner}:{owner} with wallet_type=2
   └── Wallet is now usable for task creation
```

---

## What Is NOT in Phase 1

- **Hook contract development** — The Solidity hook contract (permission enforcement) is a separate workstream.
- **Key rotation** — Phase 1 supports registration and revocation, not key rotation.
- **Migration tooling** — No automatic migration of existing basic wallets to Calibur.
- **Frontend/SDK changes** — The EIP-7702 delegation signing and key registration UI.

### What is NOT needed at all (removed from scope)

- **Bundler v0.7/v0.8 upgrade** — Calibur uses direct signed transactions, not ERC-4337 UserOps. The v0.6 bundler continues serving basic wallets unchanged.
- **Paymaster for Calibur** — Not needed. The paymaster + reimbursement pattern on basic wallets exists to work around v0.6 gas estimation unreliability. With Calibur's direct tx path, standard `eth_estimateGas` is sufficient and the user's EOA pays gas directly.
- **EntryPoint v0.7/v0.8 integration** — Calibur's `validateUserOp` targets EntryPoint v0.8 (`0x4337084D9E255Ff0702461CF8895CE9E3b5Ff108`), but we bypass this entirely via the signed relay path.

---

## Migration Safety

### No breaking changes to existing RPCs

| RPC | Change | Backward compatible? |
|---|---|---|
| `GetWallet` | None | Yes — still salt/factory based |
| `SetWallet` | None | Yes |
| `ListWallets` | New optional `wallet_type` filter | Yes — omitting returns all |
| `WithdrawFunds` | Server routes by wallet type internally | Yes — same request format |
| `CreateTask` | Server resolves wallet type from DB | Yes — same request format |

### Protobuf field numbering

All new fields use the next available field number. No existing field numbers are changed or reused.

### Storage

New storage keys (`calibur_key:{owner}`) don't conflict with existing keys. The `w:{owner}:{address}` schema works for both types since Calibur wallet address (= owner EOA) is distinct from any salt-derived address.

---

## Test Plan

### Test structure mirrors existing patterns

The existing withdrawal tests (`core/taskengine/userops_withdraw_test.go`) provide the template:
- Real chain integration tests gated by environment variables
- Shared setup function that loads config, derives wallet, connects RPC
- Small test amounts (~0.000001 ETH) for repeated runs
- Verify on-chain transaction receipt

Calibur tests follow the same pattern but exercise the **direct tx path** instead of the UserOp → bundler → EntryPoint pipeline. The existing `WithdrawFunds` RPC routes by wallet type internally, so the same RPC can be tested against both wallet types.

### Test environment requirements

```bash
# Required environment variables for Calibur integration tests
TEST_CHAIN=base                    # or sepolia
CALIBUR_TEST_EOA=0x...             # EOA that has delegated to Calibur
CALIBUR_TEST_SUBKEY_PRIVATE=0x...  # Private key of registered sub-key
```

The test EOA must have:
1. Already delegated to Calibur via EIP-7702 (on-chain `eth_getCode` returns `0xef0100 || calibur_address`)
2. A registered sub-key with our test hook (or no hook for initial tests)
3. A small ETH balance for test transactions

### Unit tests

#### `pkg/calibur/signed_batched_call_test.go`

These are pure unit tests — no chain connection needed.

| Test | What it verifies |
|---|---|
| `TestBuildSignedBatchedCall` | Correctly constructs `SignedBatchedCall` struct from targets, values, calldatas |
| `TestBuildSignedBatchedCall_Batch` | Multi-call batching (2+ targets in one call) |
| `TestSignBatchedCall` | ECDSA signature over the `SignedBatchedCall` produces valid `wrappedSignature` |
| `TestSignBatchedCall_WrongKey` | Signing with unregistered key produces a signature that would be rejected |
| `TestEncodeExecuteCalldata` | ABI encoding of `execute(SignedBatchedCall, wrappedSignature)` matches expected bytes |
| `TestNonceIncrement` | Nonce tracking increments correctly across calls |

#### `pkg/calibur/gas_estimation_test.go`

| Test | What it verifies |
|---|---|
| `TestEstimateGas_SimpleTransfer` | `eth_estimateGas` for a Calibur `execute()` call returns reasonable gas |
| `TestEstimateGas_ContractWrite` | Gas estimation for contract interaction via Calibur |

These require a chain connection but NOT a deployed Calibur wallet — they use `eth_estimateGas` against a fork or testnet.

### Integration tests: `pkg/calibur/calibur_integration_test.go`

Mirror the existing `builder_execution_success_test.go` pattern. Gated by environment variable.

```go
if os.Getenv("TEST_CHAIN") == "" || os.Getenv("CALIBUR_TEST_EOA") == "" {
    t.Skip("Skipping: CALIBUR_TEST_EOA not set")
}
```

| Test | What it verifies | Mirrors |
|---|---|---|
| `TestCaliburETHTransfer` | Send 0.000001 ETH from Calibur wallet via signed relay path. Verify tx receipt, balance change. | `TestUserOpETHTransferWithPaymaster` |
| `TestCaliburContractWrite` | Execute ERC-20 transfer (e.g., 0.01 USDC) via Calibur signed batched call. | `TestUserOpUSDCWithdrawalWithPaymaster` |
| `TestCaliburBatchedCall` | Execute 2+ calls in a single `SignedBatchedCall`. Verify all calls executed atomically. | No existing equivalent |
| `TestCaliburInsufficientBalance` | Attempt transfer exceeding balance. Verify revert. | `TestUserOpExecutionFailureExcessiveTransfer` |
| `TestCaliburExpiredKey` | Attempt execution with expired sub-key. Verify rejection. | No existing equivalent |

### Integration tests: wallet registration

#### `core/taskengine/calibur_wallet_test.go`

| Test | What it verifies |
|---|---|
| `TestRegisterCaliburWallet` | RegisterCaliburWallet RPC creates wallet in DB with `wallet_type=2`, address = owner EOA |
| `TestRegisterCaliburWallet_NotDelegated` | Returns `CALIBUR_NOT_DELEGATED` when EOA has no 7702 delegation |
| `TestRegisterCaliburWallet_AlreadyExists` | Returns `CALIBUR_WALLET_ALREADY_EXISTS` on duplicate registration |
| `TestGetCaliburWallet` | Returns wallet status, delegation check, key info, task counts |
| `TestRevokeCaliburKey` | Marks key as revoked in DB |
| `TestListWallets_IncludesCaliburWallet` | `ListWallets` returns both basic and Calibur wallets with correct `wallet_type` |
| `TestListWallets_FilterByType` | `ListWallets` with `wallet_type=CALIBUR` returns only Calibur wallets |

### Integration tests: WithdrawFunds through Calibur

#### `core/taskengine/calibur_withdraw_test.go`

These reuse the `WithdrawFunds` RPC endpoint — the server routes to the Calibur execution path based on wallet type.

| Test | What it verifies | Mirrors |
|---|---|---|
| `TestWithdrawFundsETH_Calibur` | ETH withdrawal from Calibur wallet via `WithdrawFunds` RPC. Verify direct tx path used (no UserOp hash, has tx hash). | `TestUserOpETHTransferWithPaymaster` |
| `TestWithdrawFundsUSDC_Calibur` | USDC withdrawal from Calibur wallet. | `TestUserOpUSDCWithdrawalWithPaymaster` |
| `TestWithdrawFundsMax_Calibur` | "amount: max" withdrawal. Simpler than basic wallet version — no paymaster reimbursement to subtract, just balance minus gas. | `TestWithdrawAllETH_Sepolia` |

### Integration tests: task execution through Calibur

#### `core/taskengine/calibur_task_integration_test.go`

| Test | What it verifies | Mirrors |
|---|---|---|
| `TestETHTransferTask_Calibur` | Full task lifecycle: create task with Calibur wallet → trigger → execute via signed relay → verify on-chain | `TestETHTransferTaskIntegration` |
| `TestContractWriteTask_Calibur` | ContractWrite node execution through Calibur path | Existing contract write tests |

### What does NOT need new tests

- **Bundler client tests** (`pkg/erc4337/bundler/`) — unchanged, basic wallets only
- **Nonce manager tests** — Calibur uses its own nonce (on-chain in Calibur contract), not the EntryPoint nonce manager
- **Paymaster/reimbursement tests** — not applicable to Calibur
- **Existing basic wallet tests** — must continue passing unchanged (regression)

### Test helpers to add in `core/testutil/utils.go`

```go
// Calibur-specific test helpers
GetCaliburTestConfig() *config.CaliburConfig         // Load from config YAML
GetCaliburTestEOA() common.Address                    // From CALIBUR_TEST_EOA env var
GetCaliburTestSubKeyPrivate() *ecdsa.PrivateKey       // From CALIBUR_TEST_SUBKEY_PRIVATE env var
CheckCaliburDelegation(client, eoa) bool              // Verify eth_getCode returns Calibur delegation
```
