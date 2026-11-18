package preset

import (
	"context"
	"fmt"
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/eip1559"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// Shared by pricing and previews: a chain's suggested tip can be far below
// the bundler's admission floor, even on the same RPC endpoint.
func userOpFeesV07(ctx context.Context, chain *ethclient.Client, bundler *rpc.Client, log logger.Logger) (tip, maxFee *big.Int, err error) {
	if chain == nil {
		return nil, nil, fmt.Errorf("no chain reader for user operation fee quote")
	}
	tip, err = chain.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("suggesting gas tip: %w", err)
	}
	head, err := chain.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("reading latest header: %w", err)
	}
	if bundler != nil {
		if floor, floorErr := bundlerMaxPriorityFee(ctx, bundler); floorErr != nil {
			if log != nil {
				log.Warn("bundler priority fee unavailable; using the chain tip", "chain_tip", tip.String())
				log.Debug("bundler priority fee read failed", "error", floorErr)
			}
		} else if floor.Cmp(tip) > 0 {
			if log != nil {
				log.Debug("raising priority fee to the bundler's floor", "chain_tip", tip.String(), "bundler_tip", floor.String())
			}
			tip = floor
		}
	}
	var baseFee *big.Int
	if head != nil {
		baseFee = head.BaseFee
	}
	return tip, eip1559.MaxFeeFromTipAndBase(tip, baseFee), nil
}

// UserOpMaxFeeV07 reads the same maximum fee an unsponsored send will sign.
// It neither estimates, signs nor submits an operation.
// An optional logger reports when the quote falls back to the chain tip.
func UserOpMaxFeeV07(ctx context.Context, chain *ethclient.Client, cfg *config.SmartWalletConfig, logs ...logger.Logger) (*big.Int, error) {
	if cfg == nil {
		return nil, fmt.Errorf("missing bundler configuration for fee quote")
	}
	url, err := cfg.ActiveBundlerURL()
	if err != nil {
		return nil, err
	}
	bundler, err := rpc.DialContext(ctx, url)
	if err != nil {
		return nil, err
	}
	defer bundler.Close()
	var log logger.Logger
	if len(logs) > 0 {
		log = logs[0]
	}
	_, fee, err := userOpFeesV07(ctx, chain, bundler, log)
	return fee, err
}
