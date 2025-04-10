// Provide primitive to work with a bundler RPC
// Bundler RPC is stateless
package bundler

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// BundlerClient defines a client for interacting with an EIP-4337 bundler RPC endpoint.
type BundlerClient struct {
	client *rpc.Client
}

// NewBundlerClient creates a new BundlerClient that connects to the given URL.
func NewBundlerClient(url string) (*BundlerClient, error) {
	c, err := rpc.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("Error creating bundler client: %w", err)
	}
	return &BundlerClient{client: c}, nil
}

// Close closes the underlying RPC client connection.
func (bc *BundlerClient) Close() {
	bc.client.Close()
}

// SendUserOperation sends a UserOperation to the bundler.
func (bc *BundlerClient) SendUserOperation(
	ctx context.Context,
	userOp userop.UserOperation,
	entrypoint common.Address,
) (string, error) {
	var txHash string

	uo := UserOperation{
		Sender:               userOp.Sender,
		Nonce:                fmt.Sprintf("0x%x", userOp.Nonce),
		InitCode:             fmt.Sprintf("0x%x", userOp.InitCode),
		CallData:             fmt.Sprintf("0x%x", userOp.CallData),
		CallGasLimit:         fmt.Sprintf("0x%x", userOp.CallGasLimit),
		VerificationGasLimit: fmt.Sprintf("0x%x", userOp.VerificationGasLimit),
		PreVerificationGas:   fmt.Sprintf("0x%x", userOp.PreVerificationGas),
		MaxFeePerGas:         fmt.Sprintf("0x%x", userOp.MaxFeePerGas),
		MaxPriorityFeePerGas: fmt.Sprintf("0x%x", userOp.MaxPriorityFeePerGas),
		PaymasterAndData:     fmt.Sprintf("0x%x", userOp.PaymasterAndData),
		Signature:            fmt.Sprintf("0x%x", userOp.Signature),
	}
	err := bc.client.CallContext(ctx, &txHash, "eth_sendUserOperation", uo, entrypoint)
	return txHash, err
}

// EstimateUserOperationGas estimates the gas required for a UserOperation.
// https://eips.ethereum.org/EIPS/eip-4337#rpc-methods-eth-namespace
// * eth_estimateUserOperationGas
// Estimate the gas values for a UserOperation. Given UserOperation optionally without gas limits and gas prices, return the needed gas limits. The signature field is ignored by the wallet, so that the operation will not require user’s approval. Still, it might require putting a “semi-valid” signature (e.g. a signature in the right length)
func (bc *BundlerClient) EstimateUserOperationGas(
	ctx context.Context,
	userOp userop.UserOperation,
	entrypoint common.Address,
	// https://geth.ethereum.org/docs/interacting-with-geth/rpc/ns-eth
	// Optionally accepts the State Override Set to allow users to modify the state during the gas estimation.
	// This field as well as its behavior is equivalent to the ones defined for eth_call RPC method.
	override map[string]any,
) (*GasEstimation, error) {
	var result struct {
		// PreVerificationGas   int64 `json:"preVerificationGas"`
		// VerificationGasLimit int64 `json:"verificationGasLimit"`
		// CallGasLimit         int64 `json:"callGasLimit"`
		// VerificationGas      int64 `json:"verificationGas"`

		PreVerificationGas   string `json:"preVerificationGas"`
		VerificationGasLimit string `json:"verificationGasLimit"`
		CallGasLimit         string `json:"callGasLimit"`
	}

	uo := UserOperation{
		Sender:               userOp.Sender,
		Nonce:                fmt.Sprintf("0x%x", userOp.Nonce),
		InitCode:             fmt.Sprintf("0x%x", userOp.InitCode),
		CallData:             fmt.Sprintf("0x%x", userOp.CallData),
		CallGasLimit:         fmt.Sprintf("0x%x", userOp.CallGasLimit),
		VerificationGasLimit: fmt.Sprintf("0x%x", userOp.VerificationGasLimit),
		PreVerificationGas:   fmt.Sprintf("0x%x", userOp.PreVerificationGas),
		MaxFeePerGas:         fmt.Sprintf("0x%x", userOp.MaxFeePerGas),
		MaxPriorityFeePerGas: fmt.Sprintf("0x%x", userOp.MaxPriorityFeePerGas),
		PaymasterAndData:     fmt.Sprintf("0x%x", userOp.PaymasterAndData),
		Signature:            fmt.Sprintf("0x%x", userOp.Signature),
	}
	err := bc.client.CallContext(ctx, &result, "eth_estimateUserOperationGas", uo, entrypoint, map[string]string{})

	if err != nil {
		return nil, fmt.Errorf("eth_estimateUserOperationGas RPC response error: %w", err)
	}

	gasEstimation := &GasEstimation{
		PreVerificationGas:   new(big.Int),
		VerificationGasLimit: new(big.Int),
		CallGasLimit:         new(big.Int),
		//VerificationGas:      new(big.Int),
	}

	// gasEstimation.PreVerificationGas.SetInt64(result.PreVerificationGas)
	// gasEstimation.VerificationGasLimit.SetInt64(result.VerificationGasLimit)
	// gasEstimation.CallGasLimit.SetInt64(result.CallGasLimit)
	// gasEstimation.VerificationGas.SetInt64(result.VerificationGas)
	gasEstimation.PreVerificationGas.SetString(result.PreVerificationGas[2:], 16)
	gasEstimation.VerificationGasLimit.SetString(result.VerificationGasLimit[2:], 16)
	gasEstimation.CallGasLimit.SetString(result.CallGasLimit[2:], 16)

	return gasEstimation, nil
}

// GetUserOperationByHash fetches a UserOperation by its hash.
func (bc *BundlerClient) GetUserOperationByHash(ctx context.Context, hash string) (interface{}, error) {
	var userOp interface{}
	err := bc.client.CallContext(ctx, &userOp, "eth_getUserOperationByHash", hash)
	return userOp, err
}

// GetUserOperationReceipt fetches the receipt of a UserOperation.
func (bc *BundlerClient) GetUserOperationReceipt(ctx context.Context, hash string) (interface{}, error) {
	var receipt interface{}
	err := bc.client.CallContext(ctx, &receipt, "eth_getUserOperationReceipt", hash)
	return receipt, err
}
