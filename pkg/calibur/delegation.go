package calibur

import (
	"bytes"
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// EIP-7702 delegation designation prefix: 0xef0100
var delegationPrefix = []byte{0xef, 0x01, 0x00}

// CheckDelegation verifies that an EOA has delegated to the Calibur contract via EIP-7702.
// An EIP-7702 delegated EOA has code set to: 0xef0100 || delegateAddress (23 bytes total).
func CheckDelegation(ctx context.Context, client *ethclient.Client, eoaAddress common.Address, caliburAddress common.Address) (bool, error) {
	code, err := client.CodeAt(ctx, eoaAddress, nil)
	if err != nil {
		return false, fmt.Errorf("failed to get code for %s: %w", eoaAddress.Hex(), err)
	}

	// EIP-7702 delegation code: 3 bytes prefix + 20 bytes address = 23 bytes
	if len(code) != 23 {
		return false, nil
	}

	if !bytes.HasPrefix(code, delegationPrefix) {
		return false, nil
	}

	// Extract the delegate address from the code (bytes 3-23)
	delegateAddress := common.BytesToAddress(code[3:23])
	return delegateAddress == caliburAddress, nil
}
