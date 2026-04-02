package calibur

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Client handles interactions with Calibur-delegated wallets.
type Client struct {
	ethClient      *ethclient.Client
	chainID        *big.Int
	caliburAddress common.Address
}

// NewClient creates a new Calibur client.
func NewClient(rpcURL string, chainID int64, caliburAddress common.Address) (*Client, error) {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RPC %s: %w", rpcURL, err)
	}

	return &Client{
		ethClient:      client,
		chainID:        big.NewInt(chainID),
		caliburAddress: caliburAddress,
	}, nil
}

// NewClientWithEthClient creates a Calibur client from an existing ethclient.
func NewClientWithEthClient(client *ethclient.Client, chainID int64, caliburAddress common.Address) *Client {
	return &Client{
		ethClient:      client,
		chainID:        big.NewInt(chainID),
		caliburAddress: caliburAddress,
	}
}

// SendSignedBatchedCall builds, signs, and submits a Calibur signed batched call
// as a direct Ethereum transaction (no bundler, no EntryPoint).
//
// Parameters:
//   - eoaAddress: the user's EOA that has delegated to Calibur
//   - calls: the operations to execute
//   - revertOnFailure: if true, revert entire batch on any call failure
//   - signerKey: the Calibur sub-key private key (signs the SignedBatchedCall)
//   - senderKey: the aggregator's infrastructure wallet key (pays tx gas)
//   - deadline: expiry timestamp for the signed call (0 = no deadline)
func (c *Client) SendSignedBatchedCall(
	ctx context.Context,
	eoaAddress common.Address,
	calls []Call,
	revertOnFailure bool,
	signerKey *ecdsa.PrivateKey,
	senderKey *ecdsa.PrivateKey,
	deadline *big.Int,
) (*types.Receipt, error) {
	// Compute keyHash from the signer's public key
	keyHash := ComputeKeyHash(&signerKey.PublicKey)

	// Get the nonce key — use the upper 192 bits derived from keyHash
	// Calibur uses the nonce's upper 192 bits as the key identifier
	nonceKey := new(big.Int).SetBytes(keyHash[:24]) // 24 bytes = 192 bits

	// Get current nonce from on-chain
	nonce, err := GetNonce(ctx, c.ethClient, eoaAddress, nonceKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get Calibur nonce: %w", err)
	}

	// Build the SignedBatchedCall
	signedCall := &SignedBatchedCall{
		BatchedCall: BatchedCall{
			Calls:           calls,
			RevertOnFailure: revertOnFailure,
		},
		Nonce:    nonce,
		KeyHash:  keyHash,
		Executor: common.Address{}, // address(0) = anyone can submit
		Deadline: deadline,
	}

	// Build domain separator
	domainSep := BuildDomainSeparator(c.chainID.Int64(), eoaAddress, c.caliburAddress)

	// Sign with the sub-key
	signature, err := SignBatchedCall(domainSep, signedCall, signerKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign batched call: %w", err)
	}

	// ABI-encode the execute(SignedBatchedCall, bytes) calldata
	calldata, err := EncodeExecuteCalldata(signedCall, signature)
	if err != nil {
		return nil, fmt.Errorf("failed to encode execute calldata: %w", err)
	}

	// Build and send the outer transaction
	senderAddress := crypto.PubkeyToAddress(senderKey.PublicKey)

	// Get sender nonce (standard Ethereum nonce, not Calibur nonce)
	txNonce, err := c.ethClient.PendingNonceAt(ctx, senderAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to get sender nonce: %w", err)
	}

	// Estimate gas
	gasLimit, err := c.ethClient.EstimateGas(ctx, ethereum.CallMsg{
		From: senderAddress,
		To:   &eoaAddress,
		Data: calldata,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to estimate gas: %w", err)
	}

	// Add 20% gas buffer
	gasLimit = gasLimit * 120 / 100

	// Get gas price (EIP-1559)
	gasTipCap, err := c.ethClient.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get gas tip cap: %w", err)
	}

	header, err := c.ethClient.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest header: %w", err)
	}

	gasFeeCap := new(big.Int).Add(
		new(big.Int).Mul(header.BaseFee, big.NewInt(2)),
		gasTipCap,
	)

	// Build dynamic fee tx
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   c.chainID,
		Nonce:     txNonce,
		GasTipCap: gasTipCap,
		GasFeeCap: gasFeeCap,
		Gas:       gasLimit,
		To:        &eoaAddress,
		Value:     big.NewInt(0),
		Data:      calldata,
	})

	// Sign the outer transaction
	signer := types.LatestSignerForChainID(c.chainID)
	signedTx, err := types.SignTx(tx, signer, senderKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Submit
	if err := c.ethClient.SendTransaction(ctx, signedTx); err != nil {
		return nil, fmt.Errorf("failed to send transaction: %w", err)
	}

	// Wait for receipt
	receipt, err := c.waitForReceipt(ctx, signedTx.Hash(), 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed waiting for receipt: %w", err)
	}

	return receipt, nil
}

// waitForReceipt polls for a transaction receipt with timeout.
func (c *Client) waitForReceipt(ctx context.Context, txHash common.Hash, timeout time.Duration) (*types.Receipt, error) {
	deadline := time.Now().Add(timeout)
	interval := 1 * time.Second

	for time.Now().Before(deadline) {
		receipt, err := c.ethClient.TransactionReceipt(ctx, txHash)
		if err == nil {
			return receipt, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		// Backoff: 1s, 1.5s, 2.25s, ... capped at 5s
		interval = time.Duration(float64(interval) * 1.5)
		if interval > 5*time.Second {
			interval = 5 * time.Second
		}
	}

	return nil, fmt.Errorf("timeout waiting for transaction %s receipt after %v", txHash.Hex(), timeout)
}

// CheckDelegationStatus checks if an EOA has delegated to Calibur.
func (c *Client) CheckDelegationStatus(ctx context.Context, eoaAddress common.Address) (bool, error) {
	return CheckDelegation(ctx, c.ethClient, eoaAddress, c.caliburAddress)
}
