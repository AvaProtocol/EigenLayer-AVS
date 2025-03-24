package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/AvaProtocol/ap-avs/pkg/erc20"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/shopspring/decimal"
)

// Metadata holds token and block metadata
type Metadata struct {
	Name           string
	Symbol         string
	Decimals       uint8
	FormattedValue string
	BlockNumber    uint64
}

// GetMetadataForTransfer retrieves and caches token metadata and block data
func GetMetadataForTransfer(log *types.Log) (*Metadata, error) {
	tokenAddress := log.Address.Hex()
	cacheKey := fmt.Sprintf("erc20:%s", tokenAddress)

	m := Metadata{}

	// Check if metadata is cached
	if data, err := cache.Get(cacheKey); err == nil {
		var metadata Metadata
		json.Unmarshal(data, &metadata)
	}

	if m.Name == "" {
		// Retrieve token data
		token, err := erc20.NewErc20(log.Address, rpcConn)
		if err != nil {
			return nil, fmt.Errorf("error creating ERC20 instance: %w", err)
		}

		m.Symbol, err = token.Symbol(nil)
		if err != nil {
			return nil, fmt.Errorf("error fetching token symbol: %w", err)
		}

		m.Name, err = token.Name(nil)
		if err != nil {
			return nil, fmt.Errorf("error fetching token name: %w", err)
		}

		m.Decimals, err = token.Decimals(nil)
		if err != nil {
			return nil, fmt.Errorf("error fetching token decimals: %w", err)
		}
		data, err := json.Marshal(m)
		cache.Set(cacheKey, data)
	}

	return &m, nil
}

func GetBlock(blockNumber uint64) (*types.Header, error) {
	cacheKey := fmt.Sprintf("blkt:%d", blockNumber)

	var blockHeader *types.Header
	// Check if metadata is cached
	if data, err := cache.Get(cacheKey); err == nil {
		if err = json.Unmarshal(data, blockHeader); err == nil {
			return blockHeader, nil
		}
	}

	blockHeader, err := rpcConn.HeaderByNumber(context.Background(), big.NewInt(int64(blockNumber)))
	if err != nil {
		return nil, fmt.Errorf("error fetching block header: %w", err)
	}

	data, err := json.Marshal(blockHeader)
	cache.Set(cacheKey, data)

	return blockHeader, nil
}

// ToDecimal converts a big.Int value to a human-readable format
func FormatUnit(value *big.Int, decimals int) *big.Float {
	f := new(big.Float).SetInt(value)
	decimalFactor := new(big.Float).SetFloat64(1)
	ten := big.NewFloat(10)
	for i := 0; i < decimals; i++ {
		decimalFactor.Mul(decimalFactor, ten)
	}
	return new(big.Float).Quo(f, decimalFactor)
}

func ToDecimal(ivalue interface{}, decimals int) decimal.Decimal {
	value := new(big.Int)
	switch v := ivalue.(type) {
	case string:
		value.SetString(v, 10)
	case *big.Int:
		value = v
	}

	mul := decimal.NewFromFloat(float64(10)).Pow(decimal.NewFromFloat(float64(decimals)))
	num, _ := decimal.NewFromString(value.String())
	result := num.Div(mul)

	return result
}
