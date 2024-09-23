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
	buffer := new(big.Int).Div(
		tipCap,
		big.NewInt(100),
	)
	buffer = new(big.Int).Mul(
		buffer,
		big.NewInt(13),
	)

	maxPriorityFeePerGas := new(big.Int).Add(tipCap, buffer)

	var maxFeePerGas *big.Int

	baseFee := header.BaseFee
	if baseFee != nil {
		maxFeePerGas = new(big.Int).Add(
			new(big.Int).Mul(baseFee, big.NewInt(2)),
			maxPriorityFeePerGas,
		)
	} else {
		maxFeePerGas = new(big.Int).Set(maxPriorityFeePerGas)
	}

	return maxFeePerGas, maxPriorityFeePerGas, nil
}
