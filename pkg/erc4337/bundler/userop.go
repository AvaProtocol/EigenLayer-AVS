package bundler

import (
	"github.com/ethereum/go-ethereum/common"
)

// UserOperation represents an EIP-4337 style transaction for a smart contract account.
type UserOperation struct {
	Sender               common.Address `json:"sender"`
	Nonce                string         `json:"nonce"`
	InitCode             string         `json:"initCode"`
	CallData             string         `json:"callData"`
	CallGasLimit         string         `json:"callGasLimit"`
	VerificationGasLimit string         `json:"verificationGasLimit"`
	PreVerificationGas   string         `json:"preVerificationGas"`
	MaxFeePerGas         string         `json:"maxFeePerGas"`
	MaxPriorityFeePerGas string         `json:"maxPriorityFeePerGas"`
	PaymasterAndData     string         `json:"paymasterAndData"`
	Signature            string         `json:"signature"`
}
