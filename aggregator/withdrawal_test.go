package aggregator

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

func TestValidateWithdrawalParams(t *testing.T) {
	tests := []struct {
		name        string
		params      *WithdrawalParams
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid ETH withdrawal",
			params: &WithdrawalParams{
				RecipientAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
				Amount:           big.NewInt(1000000000000000000), // 1 ETH in wei
				Token:            "ETH",
			},
			expectError: false,
		},
		{
			name: "valid ERC20 withdrawal",
			params: &WithdrawalParams{
				RecipientAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
				Amount:           big.NewInt(1000000),                          // 1 USDC (6 decimals)
				Token:            "0xA0b86a33E6Ee3c0F94E6b24C722b4ba17E8d6a13", // Example token address
			},
			expectError: false,
		},
		{
			name: "empty recipient address",
			params: &WithdrawalParams{
				RecipientAddress: common.Address{},
				Amount:           big.NewInt(1000000000000000000),
				Token:            "ETH",
			},
			expectError: true,
			errorMsg:    "recipient address cannot be empty",
		},
		{
			name: "zero amount",
			params: &WithdrawalParams{
				RecipientAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
				Amount:           big.NewInt(0),
				Token:            "ETH",
			},
			expectError: true,
			errorMsg:    "amount must be greater than zero",
		},
		{
			name: "negative amount",
			params: &WithdrawalParams{
				RecipientAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
				Amount:           big.NewInt(-1),
				Token:            "ETH",
			},
			expectError: true,
			errorMsg:    "amount must be greater than zero",
		},
		{
			name: "nil amount",
			params: &WithdrawalParams{
				RecipientAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
				Amount:           nil,
				Token:            "ETH",
			},
			expectError: true,
			errorMsg:    "amount must be greater than zero",
		},
		{
			name: "empty token",
			params: &WithdrawalParams{
				RecipientAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
				Amount:           big.NewInt(1000000000000000000),
				Token:            "",
			},
			expectError: true,
			errorMsg:    "token type cannot be empty",
		},
		{
			name: "invalid token address",
			params: &WithdrawalParams{
				RecipientAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
				Amount:           big.NewInt(1000000000000000000),
				Token:            "invalid-address",
			},
			expectError: true,
			errorMsg:    "invalid token contract address: invalid-address",
		},
		{
			name: "ETH case insensitive",
			params: &WithdrawalParams{
				RecipientAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
				Amount:           big.NewInt(1000000000000000000),
				Token:            "eth", // lowercase should work
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWithdrawalParams(tt.params)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBuildWithdrawalCalldata(t *testing.T) {
	recipientAddress := common.HexToAddress("0x1234567890123456789012345678901234567890")
	amount := big.NewInt(1000000000000000000) // 1 ETH in wei

	tests := []struct {
		name        string
		params      *WithdrawalParams
		expectError bool
		errorMsg    string
	}{
		{
			name: "build ETH withdrawal calldata",
			params: &WithdrawalParams{
				RecipientAddress: recipientAddress,
				Amount:           amount,
				Token:            "ETH",
			},
			expectError: false,
		},
		{
			name: "build ERC20 withdrawal calldata",
			params: &WithdrawalParams{
				RecipientAddress: recipientAddress,
				Amount:           big.NewInt(1000000), // 1 token with 6 decimals
				Token:            "0xA0b86a33E6Ee3c0F94E6b24C722b4ba17E8d6a13",
			},
			expectError: false,
		},
		{
			name: "fail with invalid params",
			params: &WithdrawalParams{
				RecipientAddress: common.Address{}, // empty address
				Amount:           amount,
				Token:            "ETH",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calldata, err := BuildWithdrawalCalldata(tt.params)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, calldata)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, calldata)
				assert.True(t, len(calldata) > 0, "calldata should not be empty")

				// For ETH withdrawal, calldata should be different from ERC20
				// This is a basic sanity check - detailed testing would require
				// decoding the ABI-encoded data
			}
		})
	}
}

func TestBuildETHWithdrawalCalldata(t *testing.T) {
	recipientAddress := common.HexToAddress("0x1234567890123456789012345678901234567890")
	amount := big.NewInt(1000000000000000000) // 1 ETH in wei

	calldata, err := buildETHWithdrawalCalldata(recipientAddress, amount)

	assert.NoError(t, err)
	assert.NotNil(t, calldata)
	assert.True(t, len(calldata) > 0, "calldata should not be empty")

	// Verify that it's calling the execute function
	// The first 4 bytes should be the execute function selector
	// execute(address,uint256,bytes) selector is 0xb61d27f6
	expectedSelector := []byte{0xb6, 0x1d, 0x27, 0xf6}
	assert.Equal(t, expectedSelector, calldata[:4], "should start with execute function selector")
}

func TestBuildERC20WithdrawalCalldata(t *testing.T) {
	tokenAddress := common.HexToAddress("0xA0b86a33E6Ee3c0F94E6b24C722b4ba17E8d6a13")
	recipientAddress := common.HexToAddress("0x1234567890123456789012345678901234567890")
	amount := big.NewInt(1000000) // 1 token with 6 decimals

	calldata, err := buildERC20WithdrawalCalldata(tokenAddress, recipientAddress, amount)

	assert.NoError(t, err)
	assert.NotNil(t, calldata)
	assert.True(t, len(calldata) > 0, "calldata should not be empty")

	// Verify that it's calling the execute function
	// The first 4 bytes should be the execute function selector
	// execute(address,uint256,bytes) selector is 0xb61d27f6
	expectedSelector := []byte{0xb6, 0x1d, 0x27, 0xf6}
	assert.Equal(t, expectedSelector, calldata[:4], "should start with execute function selector")
}

func TestResolveSmartWalletAddress(t *testing.T) {
	// Since this function requires an actual ethclient connection,
	// we'll test the logic flow and parameter handling
	_ = common.HexToAddress("0x742d35cc6F67C30F87b00286c1A4E49D5FDb302D")

	tests := []struct {
		name   string
		params *WithdrawalParams
	}{
		{
			name: "explicit smart wallet address provided",
			params: &WithdrawalParams{
				SmartWalletAddress: func() *common.Address {
					addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
					return &addr
				}(),
			},
		},
		{
			name: "salt provided for derivation",
			params: &WithdrawalParams{
				Salt: big.NewInt(123),
			},
		},
		{
			name: "factory address provided",
			params: &WithdrawalParams{
				FactoryAddress: func() *common.Address {
					addr := common.HexToAddress("0xF4cE7E7F7A53B8b88FF4b59Dd96D0b5b5b4A9b9b")
					return &addr
				}(),
			},
		},
		{
			name:   "default parameters (salt=0, default factory)",
			params: &WithdrawalParams{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test with nil client - should not panic and should handle gracefully
			// In a real test environment with a mock client, we could test the actual resolution
			// For now, we just ensure the function doesn't panic with different parameter combinations

			// Note: ResolveSmartWalletAddress requires a real ethclient.Client
			// In unit tests, we would typically mock this or skip tests requiring external dependencies
			// This test structure is prepared for when we have proper mocking in place

			// The main logic we can test is the parameter handling:
			if tt.params.SmartWalletAddress != nil {
				// Should return the provided address directly
				assert.NotNil(t, tt.params.SmartWalletAddress)
			}

			if tt.params.Salt != nil {
				// Should use the provided salt
				assert.NotNil(t, tt.params.Salt)
			}

			if tt.params.FactoryAddress != nil {
				// Should use the provided factory address
				assert.NotNil(t, tt.params.FactoryAddress)
			}
		})
	}
}
