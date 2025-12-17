package taskengine

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestGenerateCallData_InvalidNumericValue(t *testing.T) {
	// Create a simple ABI with a method that takes a uint256 parameter
	abiJSON := `[
		{
			"name": "testMethod",
			"type": "function",
			"inputs": [
				{"name": "amount", "type": "uint256"}
			],
			"outputs": []
		}
	]`

	contractABI, err := abi.JSON(bytes.NewReader([]byte(abiJSON)))
	assert.NoError(t, err)

	tests := []struct {
		name         string
		methodName   string
		methodParams []string
		expectedErr  string
	}{
		{
			name:         "MAX value should fail with clear error",
			methodName:   "testMethod",
			methodParams: []string{"MAX"},
			expectedErr:  "failed to parse amount (uint256): expected numeric value, got 'MAX'",
		},
		{
			name:         "empty string should fail",
			methodName:   "testMethod",
			methodParams: []string{""},
			expectedErr:  "failed to parse amount (uint256): expected numeric value, got ''",
		},
		{
			name:         "invalid string should fail",
			methodName:   "testMethod",
			methodParams: []string{"not-a-number"},
			expectedErr:  "failed to parse amount (uint256): expected numeric value, got 'not-a-number'",
		},
		{
			name:         "valid number should succeed",
			methodName:   "testMethod",
			methodParams: []string{"1000000"},
			expectedErr:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calldata, err := GenerateCallData(tt.methodName, tt.methodParams, &contractABI)

			if tt.expectedErr != "" {
				assert.Error(t, err, "Should return error for invalid input")
				assert.NotEmpty(t, err.Error(), "Error message should not be empty")
				assert.Contains(t, err.Error(), tt.expectedErr)
				assert.Empty(t, calldata, "Calldata should be empty when error occurs")
				// When error occurs, success should be false in the final response
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, calldata)
				assert.True(t, len(calldata) > 0)
			}
		})
	}
}

func TestGenerateCallData_InvalidNumericValueInTuple(t *testing.T) {
	// Create ABI for quoteExactInputSingle with tuple parameter
	abiJSON := `[
		{
			"name": "quoteExactInputSingle",
			"type": "function",
			"inputs": [
				{
					"name": "params",
					"type": "tuple",
					"components": [
						{"name": "tokenIn", "type": "address"},
						{"name": "tokenOut", "type": "address"},
						{"name": "amountIn", "type": "uint256"},
						{"name": "fee", "type": "uint24"},
						{"name": "sqrtPriceLimitX96", "type": "uint160"}
					]
				}
			],
			"outputs": []
		}
	]`

	contractABI, err := abi.JSON(bytes.NewReader([]byte(abiJSON)))
	assert.NoError(t, err)

	tests := []struct {
		name                   string
		methodName             string
		methodParams           []string
		expectedErr            string
		shouldContainFieldName bool
	}{
		{
			name:                   "MAX in tuple amountIn field should fail with field name",
			methodName:             "quoteExactInputSingle",
			methodParams:           []string{`["0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", "0xfff9976782d46cc05630d1f6ebab18b2324d6b14", "MAX", "3000", 0]`},
			expectedErr:            "failed to parse tuple amountIn (uint256): expected numeric value, got 'MAX'",
			shouldContainFieldName: true,
		},
		{
			name:                   "invalid value in tokenIn should fail",
			methodName:             "quoteExactInputSingle",
			methodParams:           []string{`["invalid-address", "0xfff9976782d46cc05630d1f6ebab18b2324d6b14", "1000000", "3000", 0]`},
			expectedErr:            "failed to parse tuple tokenIn (address):",
			shouldContainFieldName: true,
		},
		{
			name:                   "valid tuple should succeed",
			methodName:             "quoteExactInputSingle",
			methodParams:           []string{`["0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", "0xfff9976782d46cc05630d1f6ebab18b2324d6b14", "1000000", "3000", 0]`},
			expectedErr:            "",
			shouldContainFieldName: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calldata, err := GenerateCallData(tt.methodName, tt.methodParams, &contractABI)

			if tt.expectedErr != "" {
				assert.Error(t, err, "Should return error for invalid input")
				assert.NotEmpty(t, err.Error(), "Error message should not be empty")
				assert.Contains(t, err.Error(), tt.expectedErr)
				if tt.shouldContainFieldName {
					// Verify field name is in error message (check for the specific field that failed)
					if tt.name == "MAX in tuple amountIn field should fail with field name" {
						assert.Contains(t, err.Error(), "amountIn", "Error should contain field name 'amountIn'")
					} else if tt.name == "invalid value in tokenIn should fail" {
						assert.Contains(t, err.Error(), "tokenIn", "Error should contain field name 'tokenIn'")
					}
				}
				assert.Empty(t, calldata, "Calldata should be empty when error occurs")
				// When error occurs, success should be false in the final response
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, calldata)
			}
		})
	}
}

func TestParseABIParameter_InvalidNumericValue(t *testing.T) {
	tests := []struct {
		name        string
		param       string
		abiType     abi.Type
		expectedErr string
	}{
		{
			name:        "MAX should fail",
			param:       "MAX",
			abiType:     abi.Type{T: abi.UintTy, Size: 256},
			expectedErr: "expected numeric value, got 'MAX'",
		},
		{
			name:        "empty string should fail",
			param:       "",
			abiType:     abi.Type{T: abi.UintTy, Size: 256},
			expectedErr: "expected numeric value, got ''",
		},
		{
			name:        "invalid string should fail",
			param:       "not-a-number",
			abiType:     abi.Type{T: abi.UintTy, Size: 256},
			expectedErr: "expected numeric value, got 'not-a-number'",
		},
		{
			name:        "valid number should succeed",
			param:       "1000000",
			abiType:     abi.Type{T: abi.UintTy, Size: 256},
			expectedErr: "",
		},
		{
			name:        "valid hex should succeed",
			param:       "0x1234",
			abiType:     abi.Type{T: abi.UintTy, Size: 256},
			expectedErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseABIParameter(tt.param, tt.abiType)

			if tt.expectedErr != "" {
				assert.Error(t, err, "Should return error for invalid input")
				assert.NotEmpty(t, err.Error(), "Error message should not be empty")
				assert.Contains(t, err.Error(), tt.expectedErr)
				assert.Nil(t, result, "Result should be nil when error occurs")
				// When error occurs, success should be false in the final response
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

// TestContractRead_InvalidNumericValue_ResponseStructure tests that invalid numeric values
// result in a response with success=false, non-empty error, and proper error code
func TestContractRead_InvalidNumericValue_ResponseStructure(t *testing.T) {
	// Setup test environment
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Create test user
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("Owner EOA address not set, skipping test")
	}
	ownerEOA := *ownerAddr
	user := &model.User{Address: ownerEOA}

	// Create ContractRead node with MAX value in tuple (should fail)
	contractAbi := []*structpb.Value{
		structpb.NewStructValue(&structpb.Struct{
			Fields: map[string]*structpb.Value{
				"inputs": structpb.NewListValue(&structpb.ListValue{
					Values: []*structpb.Value{
						structpb.NewStructValue(&structpb.Struct{
							Fields: map[string]*structpb.Value{
								"name": structpb.NewStringValue("params"),
								"type": structpb.NewStringValue("tuple"),
								"components": structpb.NewListValue(&structpb.ListValue{
									Values: []*structpb.Value{
										structpb.NewStructValue(&structpb.Struct{
											Fields: map[string]*structpb.Value{
												"name": structpb.NewStringValue("tokenIn"),
												"type": structpb.NewStringValue("address"),
											},
										}),
										structpb.NewStructValue(&structpb.Struct{
											Fields: map[string]*structpb.Value{
												"name": structpb.NewStringValue("tokenOut"),
												"type": structpb.NewStringValue("address"),
											},
										}),
										structpb.NewStructValue(&structpb.Struct{
											Fields: map[string]*structpb.Value{
												"name": structpb.NewStringValue("amountIn"),
												"type": structpb.NewStringValue("uint256"),
											},
										}),
										structpb.NewStructValue(&structpb.Struct{
											Fields: map[string]*structpb.Value{
												"name": structpb.NewStringValue("fee"),
												"type": structpb.NewStringValue("uint24"),
											},
										}),
										structpb.NewStructValue(&structpb.Struct{
											Fields: map[string]*structpb.Value{
												"name": structpb.NewStringValue("sqrtPriceLimitX96"),
												"type": structpb.NewStringValue("uint160"),
											},
										}),
									},
								}),
							},
						}),
					},
				}),
				"name": structpb.NewStringValue("quoteExactInputSingle"),
				"outputs": structpb.NewListValue(&structpb.ListValue{
					Values: []*structpb.Value{
						structpb.NewStructValue(&structpb.Struct{
							Fields: map[string]*structpb.Value{
								"name": structpb.NewStringValue("amountOut"),
								"type": structpb.NewStringValue("uint256"),
							},
						}),
					},
				}),
				"stateMutability": structpb.NewStringValue("nonpayable"),
				"type":            structpb.NewStringValue("function"),
			},
		}),
	}

	contractReadNode := &avsproto.TaskNode{
		Id:   "test-contract-read",
		Name: "quoteExactInputSingle",
		Type: avsproto.NodeType_NODE_TYPE_CONTRACT_READ,
		TaskType: &avsproto.TaskNode_ContractRead{
			ContractRead: &avsproto.ContractReadNode{
				Config: &avsproto.ContractReadNode_Config{
					ContractAddress: "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3",
					ContractAbi:     contractAbi,
					MethodCalls: []*avsproto.ContractReadNode_MethodCall{
						{
							MethodName: "quoteExactInputSingle",
							MethodParams: []string{
								`["0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", "0xfff9976782d46cc05630d1f6ebab18b2324d6b14", "MAX", "3000", 0]`,
							},
						},
					},
				},
			},
		},
	}

	req := &avsproto.RunNodeWithInputsReq{
		Node: contractReadNode,
		InputVariables: map[string]*structpb.Value{
			"settings": structpb.NewStructValue(&structpb.Struct{
				Fields: map[string]*structpb.Value{
					"chain_id": structpb.NewNumberValue(11155111),
				},
			}),
		},
	}

	// Execute via RPC
	result, err := engine.RunNodeImmediatelyRPC(user, req)

	// Verify no system error occurred
	require.NoError(t, err, "RunNodeImmediatelyRPC should not return system error")
	require.NotNil(t, result, "Response should not be nil")

	// Debug: Print actual response values
	t.Logf("Response Success: %v", result.Success)
	t.Logf("Response Error: %q", result.Error)
	t.Logf("Response ErrorCode: %v", result.ErrorCode)

	// Verify response structure for error case
	assert.False(t, result.Success, "Success should be false when error occurs")
	assert.NotEmpty(t, result.Error, "Error message should not be empty")
	assert.Contains(t, result.Error, "failed to parse tuple amountIn", "Error should contain parsing error")
	assert.Contains(t, result.Error, "expected numeric value", "Error should indicate invalid numeric value")
	assert.Contains(t, result.Error, "got 'MAX'", "Error should show the actual invalid value")
	assert.Equal(t, avsproto.ErrorCode_INVALID_REQUEST, result.ErrorCode, "Error code should be INVALID_REQUEST (3000)")
}

// TestContractWrite_InvalidNumericValue_ResponseStructure tests that invalid numeric values
// result in a response with success=false, non-empty error, and proper error code
func TestContractWrite_InvalidNumericValue_ResponseStructure(t *testing.T) {
	// Setup test environment
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Create test user
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("Owner EOA address not set, skipping test")
	}
	ownerEOA := *ownerAddr
	user := &model.User{Address: ownerEOA}

	// Get smart wallet address for settings
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	client, err := ethclient.Dial(config.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to RPC")
	defer client.Close()

	runnerAddr, err := aa.GetSenderAddress(client, ownerEOA, big.NewInt(0))
	require.NoError(t, err, "Failed to derive smart wallet address")

	// Seed wallet in DB for validation
	_ = StoreWallet(db, ownerEOA, &model.SmartWallet{
		Owner:   &ownerEOA,
		Address: runnerAddr,
		Factory: &smartWalletConfig.FactoryAddress,
		Salt:    big.NewInt(0),
	})

	// Create ContractWrite node with MAX value in tuple (should fail)
	contractAbi := []*structpb.Value{
		structpb.NewStructValue(&structpb.Struct{
			Fields: map[string]*structpb.Value{
				"inputs": structpb.NewListValue(&structpb.ListValue{
					Values: []*structpb.Value{
						structpb.NewStructValue(&structpb.Struct{
							Fields: map[string]*structpb.Value{
								"name": structpb.NewStringValue("params"),
								"type": structpb.NewStringValue("tuple"),
								"components": structpb.NewListValue(&structpb.ListValue{
									Values: []*structpb.Value{
										structpb.NewStructValue(&structpb.Struct{
											Fields: map[string]*structpb.Value{
												"name": structpb.NewStringValue("tokenIn"),
												"type": structpb.NewStringValue("address"),
											},
										}),
										structpb.NewStructValue(&structpb.Struct{
											Fields: map[string]*structpb.Value{
												"name": structpb.NewStringValue("tokenOut"),
												"type": structpb.NewStringValue("address"),
											},
										}),
										structpb.NewStructValue(&structpb.Struct{
											Fields: map[string]*structpb.Value{
												"name": structpb.NewStringValue("amountIn"),
												"type": structpb.NewStringValue("uint256"),
											},
										}),
										structpb.NewStructValue(&structpb.Struct{
											Fields: map[string]*structpb.Value{
												"name": structpb.NewStringValue("fee"),
												"type": structpb.NewStringValue("uint24"),
											},
										}),
										structpb.NewStructValue(&structpb.Struct{
											Fields: map[string]*structpb.Value{
												"name": structpb.NewStringValue("sqrtPriceLimitX96"),
												"type": structpb.NewStringValue("uint160"),
											},
										}),
									},
								}),
							},
						}),
					},
				}),
				"name": structpb.NewStringValue("swapExactInputSingle"),
				"outputs": structpb.NewListValue(&structpb.ListValue{
					Values: []*structpb.Value{
						structpb.NewStructValue(&structpb.Struct{
							Fields: map[string]*structpb.Value{
								"name": structpb.NewStringValue("amountOut"),
								"type": structpb.NewStringValue("uint256"),
							},
						}),
					},
				}),
				"stateMutability": structpb.NewStringValue("nonpayable"),
				"type":            structpb.NewStringValue("function"),
			},
		}),
	}

	isSimulated := true
	value := "0"

	contractWriteNode := &avsproto.TaskNode{
		Id:   "test-contract-write",
		Name: "swapExactInputSingle",
		Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
		TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{
					ContractAddress: "0xA0b86a33E6441d0be3c7bb50e65Eb42d5E0b2b4b",
					ContractAbi:     contractAbi,
					MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
						{
							MethodName: "swapExactInputSingle",
							MethodParams: []string{
								`["0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", "0xfff9976782d46cc05630d1f6ebab18b2324d6b14", "MAX", "3000", 0]`,
							},
						},
					},
					IsSimulated: &isSimulated,
					Value:       &value,
				},
			},
		},
	}

	req := &avsproto.RunNodeWithInputsReq{
		Node: contractWriteNode,
		InputVariables: map[string]*structpb.Value{
			"settings": structpb.NewStructValue(&structpb.Struct{
				Fields: map[string]*structpb.Value{
					"chain_id": structpb.NewNumberValue(11155111),
					"runner":   structpb.NewStringValue(runnerAddr.Hex()),
				},
			}),
		},
	}

	// Execute via RPC
	result, err := engine.RunNodeImmediatelyRPC(user, req)

	// Verify no system error occurred
	require.NoError(t, err, "RunNodeImmediatelyRPC should not return system error")
	require.NotNil(t, result, "Response should not be nil")

	// Debug: Print actual response values
	t.Logf("Response Success: %v", result.Success)
	t.Logf("Response Error: %q", result.Error)
	t.Logf("Response ErrorCode: %v", result.ErrorCode)

	// Verify response structure for error case
	assert.False(t, result.Success, "Success should be false when error occurs")
	assert.NotEmpty(t, result.Error, "Error message should not be empty")
	assert.Contains(t, result.Error, "failed to parse tuple amountIn", "Error should contain parsing error")
	assert.Contains(t, result.Error, "expected numeric value", "Error should indicate invalid numeric value")
	assert.Contains(t, result.Error, "got 'MAX'", "Error should show the actual invalid value")
	assert.Equal(t, avsproto.ErrorCode_INVALID_REQUEST, result.ErrorCode, "Error code should be INVALID_REQUEST (3000)")
}
