package eip1559

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/ethclient"
)

// minGweiFloor is a conservative network-wide floor we enforce to avoid
// unrealistically low gas caps on slow testnets. Default: 2 gwei.
var minGweiFloor = big.NewInt(2_000_000_000) // 2 gwei in wei

// MinGweiFloor is the live floor SuggestFee honors. Native preflight
// must not use this as NT's signed maxFee — see SignedOpMaxFeePerGas.
func MinGweiFloor() *big.Int {
	return new(big.Int).Set(minGweiFloor)
}

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

// MaxFeeFromTipAndBase is what production UserOps sign:
// maxFee = tip + 2*baseFee (legacy: tip). No 2 gwei floor. NativeTokenLimitModule
// charges this maxFee off the signed op, so preflight must use it too.
func MaxFeeFromTipAndBase(tip, baseFee *big.Int) *big.Int {
	if tip == nil {
		tip = big.NewInt(0)
	}
	if baseFee == nil {
		return new(big.Int).Set(tip)
	}
	return new(big.Int).Add(tip, new(big.Int).Mul(baseFee, big.NewInt(2)))
}

// SignedOpMaxFeePerGas reads chain tip and baseFee and returns the signed-op
// maxFee. It omits the bundler priority-fee floor that priceOperationV07
// applies at send (tip = max(chainTip, bundlerTip)). That under-prices
// relative to the signed op when the bundler floor exceeds the chain tip
// (Sepolia today ~0.1 vs ~0.001 gwei, about 4% of the total). Preflight
// still nets conservative: A0 measured ~400k actual against the 500k
// steady-state ceiling and ~0.8–1M against the 2M first-op ceiling. Fail
// closed if either RPC call errors.
func SignedOpMaxFeePerGas(ctx context.Context, client *ethclient.Client) (*big.Int, error) {
	if client == nil {
		return nil, fmt.Errorf("no ethclient")
	}
	tip, err := client.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, fmt.Errorf("suggesting gas tip: %w", err)
	}
	head, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("reading latest header: %w", err)
	}
	var baseFee *big.Int
	if head != nil {
		baseFee = head.BaseFee
	}
	return MaxFeeFromTipAndBase(tip, baseFee), nil
}
