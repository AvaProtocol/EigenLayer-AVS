# ERC20 Package

Standard ERC20 token contract bindings.

## Files

- `erc20.abi` - Standard ERC20 contract ABI
- `erc20.go` - Generated Go bindings (created with `abigen`)

## Usage

```go
import "github.com/AvaProtocol/EigenLayer-AVS/pkg/erc20"

// Create token contract instance
token, err := erc20.NewErc20(tokenAddress, client)

// Read balance
balance, err := token.BalanceOf(nil, address)

// Transfer tokens
tx, err := token.Transfer(opts, recipient, amount)
```

## Updating

When updating the ERC20 ABI:

1. Replace `erc20.abi`
2. Regenerate bindings:
   ```bash
   abigen --abi=erc20.abi --pkg=erc20 --out=erc20.go
   ```
3. Commit both files

