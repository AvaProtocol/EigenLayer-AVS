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

type jsonRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      interface{}   `json:"id"`
}

func makeDummyUserOp() userop.UserOperation {
	return userop.UserOperation{
		Sender:               common.HexToAddress("0x123"),
		Nonce:                big.NewInt(1),
		InitCode:             common.FromHex("0x"),
		CallData:             common.FromHex("0x"),
		CallGasLimit:         big.NewInt(0),
		VerificationGasLimit: big.NewInt(0),
		PreVerificationGas:   big.NewInt(0),
		MaxFeePerGas:         big.NewInt(0),
		MaxPriorityFeePerGas: big.NewInt(0),
		PaymasterAndData:     common.FromHex("0x"),
		Signature:            common.FromHex("0x"),
	}
}

func TestNewBundlerClient(t *testing.T) {
	t.Run("Valid URL", func(t *testing.T) {
		client, err := NewBundlerClient("http://localhost:8545")
		require.NoError(t, err)
		require.NotNil(t, client)
		defer client.Close()
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
	dummyUserOp := makeDummyUserOp()

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

				var req jsonRPCRequest
				err := json.NewDecoder(r.Body).Decode(&req)
				require.NoError(t, err, "failed to decode JSON-RPC request body")
				assert.Equal(t, "2.0", req.JSONRPC)
				assert.Equal(t, "eth_simulateUserOperation", req.Method)
				require.Len(t, req.Params, 3, "expected params: userOp, entrypoint, latest")

				entrypointParam, ok := req.Params[1].(string)
				require.True(t, ok, "entrypoint param must be a string")
				assert.Equal(t, entrypoint.Hex(), entrypointParam)

				blockTagParam, ok := req.Params[2].(string)
				require.True(t, ok, "block tag param must be a string")
				assert.Equal(t, "latest", blockTagParam)

				w.WriteHeader(tt.mockStatus)
				_ = json.NewEncoder(w).Encode(tt.mockResponse)
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

func TestEstimateUserOperationGas(t *testing.T) {
	entrypoint := common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789")
	dummyUserOp := makeDummyUserOp()

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
			name:          "HTTP Error Status",
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
				assert.Equal(t, "POST", r.Method)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

				var req jsonRPCRequest
				err := json.NewDecoder(r.Body).Decode(&req)
				require.NoError(t, err, "failed to decode JSON-RPC request body")
				assert.Equal(t, "2.0", req.JSONRPC)
				assert.Equal(t, "eth_estimateUserOperationGas", req.Method)
				require.Len(t, req.Params, 3, "expected params: userOp, entrypoint, override")

				entrypointParam, ok := req.Params[1].(string)
				require.True(t, ok, "entrypoint param must be a string")
				assert.Equal(t, entrypoint.Hex(), entrypointParam)

				_, ok = req.Params[2].(map[string]interface{})
				require.True(t, ok, "override param must be an object")

				w.WriteHeader(tt.mockStatus)
				if tt.mockResponse != nil {
					_ = json.NewEncoder(w).Encode(tt.mockResponse)
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
