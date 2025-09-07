package aggregator

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ERC20 transfer function signature: transfer(address,uint256)
const erc20TransferFunctionSig = "a9059cbb"

// ERC20 ABI for the transfer function
var erc20ABI = `[{"constant":false,"inputs":[{"name":"_to","type":"address"},{"name":"_value","type":"uint256"}],"name":"transfer","outputs":[{"name":"","type":"bool"}],"payable":false,"stateMutability":"nonpayable","type":"function"}]`

// WithdrawalParams represents the parameters needed for a withdrawal
type WithdrawalParams struct {
	RecipientAddress   common.Address
	Amount             *big.Int
	Token              string // "ETH" or contract address
	SmartWalletAddress *common.Address
	Salt               *big.Int
	FactoryAddress     *common.Address
}

// ValidateWithdrawalParams validates the withdrawal parameters
func ValidateWithdrawalParams(params *WithdrawalParams) error {
	if params.RecipientAddress == (common.Address{}) {
		return fmt.Errorf("recipient address cannot be empty")
	}

	if params.Amount == nil || params.Amount.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("amount must be greater than zero")
	}

	if params.Token == "" {
		return fmt.Errorf("token type cannot be empty")
	}

	// If token is not ETH, validate it as a contract address
	if strings.ToUpper(params.Token) != "ETH" && !common.IsHexAddress(params.Token) {
		return fmt.Errorf("invalid token contract address: %s", params.Token)
	}

	return nil
}

// BuildWithdrawalCalldata builds the calldata for either ETH or ERC20 withdrawal
func BuildWithdrawalCalldata(params *WithdrawalParams) ([]byte, error) {
	if err := ValidateWithdrawalParams(params); err != nil {
		return nil, err
	}

	if strings.ToUpper(params.Token) == "ETH" {
		return buildETHWithdrawalCalldata(params.RecipientAddress, params.Amount)
	} else {
		return buildERC20WithdrawalCalldata(common.HexToAddress(params.Token), params.RecipientAddress, params.Amount)
	}
}

// buildETHWithdrawalCalldata builds calldata for ETH withdrawal (simple transfer)
func buildETHWithdrawalCalldata(recipient common.Address, amount *big.Int) ([]byte, error) {
	// For ETH withdrawal, we call the smart wallet's execute function with:
	// - target: recipient address
	// - value: amount to transfer
	// - data: empty (no additional calldata needed)
	return aa.PackExecute(recipient, amount, []byte{})
}

// buildERC20WithdrawalCalldata builds calldata for ERC20 token withdrawal
func buildERC20WithdrawalCalldata(tokenAddress, recipient common.Address, amount *big.Int) ([]byte, error) {
	// Parse ERC20 ABI
	parsedABI, err := abi.JSON(strings.NewReader(erc20ABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ERC20 ABI: %w", err)
	}

	// Pack the transfer function call
	transferCalldata, err := parsedABI.Pack("transfer", recipient, amount)
	if err != nil {
		return nil, fmt.Errorf("failed to pack ERC20 transfer call: %w", err)
	}

	// Use smart wallet's execute function to call the ERC20 contract
	// - target: token contract address
	// - value: 0 (no ETH transfer needed for ERC20)
	// - data: transfer function calldata
	return aa.PackExecute(tokenAddress, big.NewInt(0), transferCalldata)
}

// ResolveSmartWalletAddress resolves the smart wallet address to use for withdrawal
func ResolveSmartWalletAddress(client *ethclient.Client, owner common.Address, params *WithdrawalParams) (*common.Address, error) {
	// If explicit address provided, validate it belongs to the owner
	if params.SmartWalletAddress != nil {
		// Ownership validation: check that the provided smart wallet address matches the expected address for the owner
		salt := big.NewInt(0)
		if params.Salt != nil {
			salt = params.Salt
		}
		var expectedAddr *common.Address
		var err error
		if params.FactoryAddress != nil {
			expectedAddr, err = aa.GetSenderAddressForFactory(client, owner, *params.FactoryAddress, salt)
		} else {
			expectedAddr, err = aa.GetSenderAddress(client, owner, salt)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to compute expected smart wallet address: %w", err)
		}
		if *expectedAddr != *params.SmartWalletAddress {
			return nil, fmt.Errorf("provided smart wallet address does not belong to the owner")
		}
		return params.SmartWalletAddress, nil
	}

	// Use salt if provided, otherwise default to 0
	salt := big.NewInt(0)
	if params.Salt != nil {
		salt = params.Salt
	}

	// Use factory address if provided, otherwise use default
	if params.FactoryAddress != nil {
		return aa.GetSenderAddressForFactory(client, owner, *params.FactoryAddress, salt)
	}

	return aa.GetSenderAddress(client, owner, salt)
}
