package taskengine

import (
	"math/big"
	"time"
)

// TransferEventResponse represents the standardized response format for Transfer events
// This serves as the single source of truth for all Transfer event responses
// Note: logIndex and transactionIndex are excluded from main data as they're available in metadata
type TransferEventResponse struct {
	// Contract and token information
	Address       string `json:"address"`       // Contract address
	TokenName     string `json:"tokenName"`     // Token name (e.g., "Wrapped Ether")
	TokenSymbol   string `json:"tokenSymbol"`   // Token symbol (e.g., "WETH")
	TokenDecimals uint32 `json:"tokenDecimals"` // Token decimals (e.g., 18)

	// Transaction information
	TransactionHash string `json:"transactionHash"` // Transaction hash

	// Block information
	BlockNumber    uint64 `json:"blockNumber"`    // Block number
	BlockTimestamp int64  `json:"blockTimestamp"` // Block timestamp (Unix seconds)

	// Transfer-specific data
	FromAddress string `json:"fromAddress"` // Sender address
	ToAddress   string `json:"toAddress"`   // Recipient address
	Value       string `json:"value"`       // Formatted token amount (decimals applied if requested)
}

// GetSampleTransferAmount returns an appropriate sample transfer amount based on token decimals
// This replaces the hard-coded 100500000 value with more realistic amounts
func GetSampleTransferAmount(decimals uint32) *big.Int {
	amount := big.NewInt(0)

	switch {
	case decimals >= 18:
		// High decimal tokens (ETH, WETH, etc): 1.5 tokens
		// 1.5 * 10^18 = 1500000000000000000
		amount.SetString("1500000000000000000", 10)
	case decimals >= 8 && decimals < 18:
		// Medium decimal tokens: 100.5 tokens
		// 100.5 * 10^decimals
		multiplier := big.NewInt(10)
		multiplier.Exp(multiplier, big.NewInt(int64(decimals)), nil)
		baseAmount := big.NewInt(1005) // 100.5 * 10
		amount.Mul(baseAmount, multiplier)
		amount.Div(amount, big.NewInt(10)) // Divide by 10 to get 100.5
	case decimals < 8:
		// Low decimal tokens (USDC, USDT): 100.5 tokens
		// 100.5 * 10^decimals
		multiplier := big.NewInt(10)
		multiplier.Exp(multiplier, big.NewInt(int64(decimals)), nil)
		baseAmount := big.NewInt(1005) // 100.5 * 10
		amount.Mul(baseAmount, multiplier)
		amount.Div(amount, big.NewInt(10)) // Divide by 10 to get 100.5
	default:
		// Fallback: use the original amount for 6 decimals (USDC-style)
		amount.SetString("100500000", 10)
	}

	return amount
}

// CreateStandardizedTransferResponse creates a standardized Transfer event response
// This ensures all Transfer events follow the same format
// Note: logIndex and transactionIndex are excluded as they're available in metadata
func CreateStandardizedTransferResponse(
	contractAddress, txHash string,
	blockNumber uint64,
	fromAddr, toAddr string,
	tokenName, tokenSymbol string,
	tokenDecimals uint32,
	value string,
) TransferEventResponse {
	return TransferEventResponse{
		Address:         contractAddress,
		TokenName:       tokenName,
		TokenSymbol:     tokenSymbol,
		TokenDecimals:   tokenDecimals,
		TransactionHash: txHash,
		BlockNumber:     blockNumber,
		BlockTimestamp:  time.Now().Unix() * 1000, // Convert to milliseconds for JavaScript compatibility
		FromAddress:     fromAddr,
		ToAddress:       toAddr,
		Value:           value,
	}
}
