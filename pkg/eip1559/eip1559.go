package eip1559

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/ethclient"
)

// minGweiFloor is a conservative network-wide floor we enforce to avoid
// unrealistically low gas caps on slow testnets. Default: 2 gwei.
var minGweiFloor = big.NewInt(2_000_000_000) // 2 gwei in wei

// SetMinGweiFloor allows overriding the minimum gas floor (in wei).
// Pass nil to reset to default 2 gwei.
func SetMinGweiFloor(wei *big.Int) {
	if wei == nil || wei.Sign() <= 0 {
		minGweiFloor = big.NewInt(2_000_000_000)
		return
	}
	minGweiFloor = new(big.Int).Set(wei)
}

func SuggestFee(client *ethclient.Client) (*big.Int, *big.Int, error) {
	// Get suggested gas tip cap (maxPriorityFeePerGas) from the chain
	tipCap, err := client.SuggestGasTipCap(context.Background())
	if err != nil {
		return nil, nil, err
	}

	// Estimate base fee for the next block
	header, err := client.HeaderByNumber(context.Background(), nil)
	if err != nil {
		return nil, nil, err
	}

	baseFee := header.BaseFee

	// Add 13% buffer to tip for safety margin against mempool competition
	buffer := new(big.Int).Div(tipCap, big.NewInt(100))
	buffer = new(big.Int).Mul(buffer, big.NewInt(13))
	maxPriorityFeePerGas := new(big.Int).Add(tipCap, buffer)

	// Enforce a floor for priority fee to avoid stalled inclusion on testnets
	if maxPriorityFeePerGas.Cmp(minGweiFloor) < 0 {
		maxPriorityFeePerGas = new(big.Int).Set(minGweiFloor)
	}

	var maxFeePerGas *big.Int
	if baseFee != nil {
		// EIP-1559: maxFeePerGas must be >= baseFee + maxPriorityFeePerGas
		// Use 2x baseFee for headroom to handle baseFee increases between blocks
		// Formula: maxFeePerGas = (2 * baseFee) + maxPriorityFeePerGas
		// This ensures the UserOp can be included even if baseFee increases by up to 100%
		maxFeePerGas = new(big.Int).Add(
			new(big.Int).Mul(baseFee, big.NewInt(2)),
			maxPriorityFeePerGas,
		)

		// Enforce a floor for maxFeePerGas too
		if maxFeePerGas.Cmp(minGweiFloor) < 0 {
			maxFeePerGas = new(big.Int).Set(minGweiFloor)
		}
	} else {
		// Legacy (pre-EIP-1559) chain - use maxPriorityFeePerGas as maxFeePerGas
		maxFeePerGas = new(big.Int).Set(maxPriorityFeePerGas)
		if maxFeePerGas.Cmp(minGweiFloor) < 0 {
			maxFeePerGas = new(big.Int).Set(minGweiFloor)
		}
	}

	return maxFeePerGas, maxPriorityFeePerGas, nil
}
