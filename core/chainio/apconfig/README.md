# APConfig Package

Smart contract bindings for the APConfig contract.

## Files

- `apconfig.abi` - Contract ABI (source of truth)
- `binding.go` - Generated Go bindings (created with `abigen`)
- `apconfig.go` - Helper functions for contract interaction

## Updating

When the APConfig contract ABI changes:

1. Replace `apconfig.abi` with the new ABI
2. Regenerate bindings:
   ```bash
   abigen --abi=apconfig.abi --pkg=apconfig --type=APConfig --out=binding.go
   ```
3. Commit both files

