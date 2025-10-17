package eip1559

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/ethclient"
)

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

	// Dynamic minimum based on actual chain conditions, not hardcoded values
	// Use the chain's suggested tip or a small fraction of baseFee, whichever is higher
	var minTip *big.Int
	if baseFee != nil {
		// For EIP-1559 chains: minimum tip is 1% of baseFee or the suggested tip, whichever is higher
		// This ensures we don't use artificially low tips on high-fee chains
		onePercentOfBaseFee := new(big.Int).Div(baseFee, big.NewInt(100))
		minTip = onePercentOfBaseFee
	} else {
		// For legacy chains: use a very small minimum (0.001 gwei)
		minTip = big.NewInt(1_000_000) // 0.001 gwei
	}

	if maxPriorityFeePerGas.Cmp(minTip) < 0 {
		maxPriorityFeePerGas = minTip
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

		// NO hardcoded minimums - trust the chain's real-time data
		// Base chain baseFee ~0.001 gwei → maxFeePerGas ~0.002 gwei (realistic)
		// Ethereum baseFee ~30 gwei → maxFeePerGas ~62 gwei (realistic)
	} else {
		// Legacy (pre-EIP-1559) chain - use maxPriorityFeePerGas as maxFeePerGas
		maxFeePerGas = new(big.Int).Set(maxPriorityFeePerGas)
	}

	return maxFeePerGas, maxPriorityFeePerGas, nil
}
