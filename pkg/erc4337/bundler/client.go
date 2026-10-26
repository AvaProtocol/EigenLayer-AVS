// Provide primitive to work with a bundler RPC
// Bundler RPC is stateless
package bundler

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/rpc"
)

// BundlerClient defines a client for interacting with an EIP-4337 bundler RPC endpoint.
type BundlerClient struct {
	client *rpc.Client
}

// NewBundlerClient creates a new BundlerClient that connects to the given URL.
func NewBundlerClient(url string) (*BundlerClient, error) {
	// Use DialHTTP instead of Dial as it is more compatible with HTTP-based bundler
	// endpoints, but it also supports other protocols such as WebSocket.
	c, err := rpc.DialHTTP(url)
	if err != nil {
		return nil, fmt.Errorf("error creating bundler client: %w", err)
	}
	return &BundlerClient{client: c}, nil
}

// Close closes the underlying RPC client connection.
func (bc *BundlerClient) Close() {
	bc.client.Close()
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
