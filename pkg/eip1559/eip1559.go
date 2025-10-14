package eip1559

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/ethclient"
)

func SuggestFee(client *ethclient.Client) (*big.Int, *big.Int, error) {
	// Get suggested gas tip cap (maxPriorityFeePerGas)
	tipCap, err := client.SuggestGasTipCap(context.Background())
	if err != nil {
		return nil, nil, err
	}

	// Estimate base fee for the next block
	header, err := client.HeaderByNumber(context.Background(), nil)
	if err != nil {
		return nil, nil, err
	}

	// Add 13% buffer to tip for safety
	buffer := new(big.Int).Div(tipCap, big.NewInt(100))
	buffer = new(big.Int).Mul(buffer, big.NewInt(13))
	maxPriorityFeePerGas := new(big.Int).Add(tipCap, buffer)

	// Ensure minimum tip of 2 gwei for bundler profitability
	minTip := big.NewInt(2_000_000_000) // 2 gwei
	if maxPriorityFeePerGas.Cmp(minTip) < 0 {
		maxPriorityFeePerGas = minTip
	}

	var maxFeePerGas *big.Int

	baseFee := header.BaseFee
	if baseFee != nil {
		// EIP-1559: maxFeePerGas must be >= baseFee + maxPriorityFeePerGas
		// Use 2x baseFee for headroom to handle baseFee increases between blocks
		// Formula: maxFeePerGas = (2 * baseFee) + maxPriorityFeePerGas
		// This ensures the UserOp can be included even if baseFee increases by up to 100%
		maxFeePerGas = new(big.Int).Add(
			new(big.Int).Mul(baseFee, big.NewInt(2)),
			maxPriorityFeePerGas,
		)

		// Ensure minimum maxFeePerGas of 20 gwei for high-basefee chains like Base
		minMaxFee := big.NewInt(20_000_000_000) // 20 gwei
		if maxFeePerGas.Cmp(minMaxFee) < 0 {
			maxFeePerGas = minMaxFee
		}
	} else {
		// Legacy (pre-EIP-1559) chain - use maxPriorityFeePerGas as maxFeePerGas
		maxFeePerGas = new(big.Int).Set(maxPriorityFeePerGas)
	}

	return maxFeePerGas, maxPriorityFeePerGas, nil
}
