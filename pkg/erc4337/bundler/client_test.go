package bundler

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

func TestNewBundlerClient(t *testing.T) {
	t.Run("Valid URL", func(t *testing.T) {
		client, err := NewBundlerClient("http://localhost:8545")
		require.NoError(t, err)
		require.NotNil(t, client)
		require.Equal(t, "http://localhost:8545", client.url)
	})

	t.Run("Invalid URL", func(t *testing.T) {
		// rpc.DialHTTP should fail with a completely malformed URL
		_, err := NewBundlerClient("://invalid-url")
		require.Error(t, err)
	})
}

func TestSimulateUserOperation(t *testing.T) {
	entrypoint := common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789")
	dummyUserOp := userop.UserOperation{
		Sender: common.HexToAddress("0x123"),
		Nonce:  big.NewInt(1),
	}

	tests := []struct {
		name          string
		mockResponse  map[string]interface{}
		mockStatus    int
		expectedError string
	}{
		{
			name: "Success - No Error in JSON",
			mockResponse: map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  "0x",
			},
			mockStatus:    http.StatusOK,
			expectedError: "",
		},
		{
			name: "RPC Error - Method Not Found (Ignored)",
			mockResponse: map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"error": map[string]interface{}{
					"code":    float64(-32601),
					"message": "Method not found",
				},
			},
			mockStatus:    http.StatusOK,
			expectedError: "", // Our code explicitly ignores -32601
		},
		{
			name: "RPC Error - Actual Error",
			mockResponse: map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"error": map[string]interface{}{
					"code":    float64(-32000),
					"message": "execution reverted",
				},
			},
			mockStatus:    http.StatusOK,
			expectedError: "JSON-RPC error -32000: execution reverted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "POST", r.Method)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

				w.WriteHeader(tt.mockStatus)
				json.NewEncoder(w).Encode(tt.mockResponse)
			}))
			defer server.Close()

			client, err := NewBundlerClient(server.URL)
			require.NoError(t, err)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			err = client.SimulateUserOperation(ctx, dummyUserOp, entrypoint)

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEstimateUserOperationGasHTTP(t *testing.T) {
	entrypoint := common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789")
	dummyUserOp := userop.UserOperation{
		Sender: common.HexToAddress("0x123"),
		Nonce:  big.NewInt(1),
	}

	tests := []struct {
		name          string
		mockResponse  map[string]interface{}
		mockStatus    int
		expectedError string
	}{
		{
			name: "Success",
			mockResponse: map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]interface{}{
					"preVerificationGas":   "0x186a0", // 100000
					"verificationGasLimit": "0x186a0",
					"callGasLimit":         "0x186a0",
				},
			},
			mockStatus:    http.StatusOK,
			expectedError: "",
		},
		{
			name: "HTTP Error Status",
			mockResponse:  nil,
			mockStatus:    http.StatusInternalServerError,
			expectedError: "500 Internal Server Error",
		},
		{
			name: "RPC Error",
			mockResponse: map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"error": map[string]interface{}{
					"code":    -32000,
					"message": "gas required exceeds allowance",
				},
			},
			mockStatus:    http.StatusOK,
			expectedError: "JSON-RPC error -32000: gas required exceeds allowance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.mockStatus)
				if tt.mockResponse != nil {
					json.NewEncoder(w).Encode(tt.mockResponse)
				}
			}))
			defer server.Close()

			client, err := NewBundlerClient(server.URL)
			require.NoError(t, err)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			res, err := client.EstimateUserOperationGas(ctx, dummyUserOp, entrypoint, nil)

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				assert.Nil(t, res)
			} else {
				require.NoError(t, err)
				require.NotNil(t, res)
				// 0x186a0 hex is 100000 in decimal
				assert.Equal(t, int64(100000), res.PreVerificationGas.Int64())
				assert.Equal(t, int64(100000), res.VerificationGasLimit.Int64())
				assert.Equal(t, int64(100000), res.CallGasLimit.Int64())
			}
		})
	}
}