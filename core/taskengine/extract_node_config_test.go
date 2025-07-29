package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// Test ABI data - mirrors real user input format (same as JavaScript SDK tests)
const (
	// Simple decimals function - what users actually provide
	testDecimalsABIForConfig = `[{"inputs":[],"name":"decimals","outputs":[{"internalType":"uint8","name":"","type":"uint8"}],"stateMutability":"view","type":"function"}]`

	// ERC20 transfer function with inputs - what users actually provide
	testTransferABIForConfig = `[{"inputs":[{"internalType":"address","name":"to","type":"address"},{"internalType":"uint256","name":"amount","type":"uint256"}],"name":"transfer","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"}]`

	// Simple transfer function - what users actually provide
	testSimpleTransferABI = `[{"inputs":[],"name":"transfer","outputs":[],"stateMutability":"nonpayable","type":"function"}]`
)

func TestExtractNodeConfiguration_LoopNodeRunners(t *testing.T) {
	tests := []struct {
		name        string
		setupNode   func() *avsproto.TaskNode
		expectedErr bool
		validate    func(t *testing.T, config map[string]interface{})
	}{
		{
			name: "RestAPI runner with headers",
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_LOOP,
					TaskType: &avsproto.TaskNode_Loop{
						Loop: &avsproto.LoopNode{
							Config: &avsproto.LoopNode_Config{
								InputNodeName: "source-node-id",
								IterVal:       "value",
								IterKey:       "index",
								ExecutionMode: avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL,
							},
							Runner: &avsproto.LoopNode_RestApi{
								RestApi: &avsproto.RestAPINode{
									Config: &avsproto.RestAPINode_Config{
										Url:    MockAPIEndpoint + "/data",
										Method: "GET",
										Body:   "",
										Headers: map[string]string{
											"Authorization": "Bearer token123",
											"Content-Type":  "application/json",
											"User-Agent":    "AvaProtocol/1.0",
										},
									},
								},
							},
						},
					},
				}
			},
			expectedErr: false,
			validate: func(t *testing.T, config map[string]interface{}) {
				assert.Equal(t, "source-node-id", config["inputNodeName"])
				assert.Equal(t, "value", config["iterVal"])
				assert.Equal(t, "index", config["iterKey"])
				assert.Equal(t, "EXECUTION_MODE_PARALLEL", config["executionMode"])

				runner, ok := config["runner"].(map[string]interface{})
				require.True(t, ok, "runner should be a map[string]interface{}")
				assert.Equal(t, "restApi", runner["type"])

				runnerConfig, ok := runner["config"].(map[string]interface{})
				require.True(t, ok, "runner config should be a map[string]interface{}")
				assert.Equal(t, MockAPIEndpoint+"/data", runnerConfig["url"])
				assert.Equal(t, "GET", runnerConfig["method"])
				assert.Equal(t, "", runnerConfig["body"])

				headers, ok := runnerConfig["headers"].(map[string]interface{})
				require.True(t, ok, "headers should be a map[string]interface{}")
				assert.Equal(t, "Bearer token123", headers["Authorization"])
				assert.Equal(t, "application/json", headers["Content-Type"])
				assert.Equal(t, "AvaProtocol/1.0", headers["User-Agent"])
			},
		},
		{
			name: "GraphQL runner with variables",
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_LOOP,
					TaskType: &avsproto.TaskNode_Loop{
						Loop: &avsproto.LoopNode{
							Config: &avsproto.LoopNode_Config{
								InputNodeName: "source-node-id",
								IterVal:       "item",
								IterKey:       "idx",
								ExecutionMode: avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL,
							},
							Runner: &avsproto.LoopNode_GraphqlDataQuery{
								GraphqlDataQuery: &avsproto.GraphQLQueryNode{
									Config: &avsproto.GraphQLQueryNode_Config{
										Url:   MockAPIEndpoint + "/graphql",
										Query: "query($id: ID!) { user(id: $id) { name email } }",
										Variables: map[string]string{
											"id":     "{{item.id}}",
											"limit":  "10",
											"offset": "{{idx}}",
										},
									},
								},
							},
						},
					},
				}
			},
			expectedErr: false,
			validate: func(t *testing.T, config map[string]interface{}) {
				assert.Equal(t, "source-node-id", config["inputNodeName"])
				assert.Equal(t, "item", config["iterVal"])
				assert.Equal(t, "idx", config["iterKey"])
				assert.Equal(t, "EXECUTION_MODE_SEQUENTIAL", config["executionMode"])

				runner, ok := config["runner"].(map[string]interface{})
				require.True(t, ok, "runner should be a map[string]interface{}")
				assert.Equal(t, "graphqlDataQuery", runner["type"])

				runnerConfig, ok := runner["config"].(map[string]interface{})
				require.True(t, ok, "runner config should be a map[string]interface{}")
				assert.Equal(t, MockAPIEndpoint+"/graphql", runnerConfig["url"])
				assert.Equal(t, "query($id: ID!) { user(id: $id) { name email } }", runnerConfig["query"])

				variables, ok := runnerConfig["variables"].(map[string]interface{})
				require.True(t, ok, "variables should be a map[string]interface{}")
				assert.Equal(t, "{{item.id}}", variables["id"])
				assert.Equal(t, "10", variables["limit"])
				assert.Equal(t, "{{idx}}", variables["offset"])
			},
		},
		{
			name: "ContractRead runner with method calls",
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_LOOP,
					TaskType: &avsproto.TaskNode_Loop{
						Loop: &avsproto.LoopNode{
							Config: &avsproto.LoopNode_Config{
								InputNodeName: "source-node-id",
								IterVal:       "address",
								IterKey:       "index",
								ExecutionMode: avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL,
							},
							Runner: &avsproto.LoopNode_ContractRead{
								ContractRead: &avsproto.ContractReadNode{
									Config: &avsproto.ContractReadNode_Config{
										ContractAddress: "0x1234567890123456789012345678901234567890",
										ContractAbi:     MustConvertJSONABIToProtobufValues(testDecimalsABIForConfig),
										MethodCalls: []*avsproto.ContractReadNode_MethodCall{
											{
												CallData:      stringPtr("0x313ce567"),
												MethodName:    "decimals",
												ApplyToFields: []string{"balance", "amount"},
											},
											{
												CallData:      stringPtr("0x70a082310000000000000000000000001234567890123456789012345678901234567890"),
												MethodName:    "balanceOf",
												ApplyToFields: []string{"balance"},
											},
										},
									},
								},
							},
						},
					},
				}
			},
			expectedErr: false,
			validate: func(t *testing.T, config map[string]interface{}) {
				assert.Equal(t, "source-node-id", config["inputNodeName"])
				assert.Equal(t, "address", config["iterVal"])
				assert.Equal(t, "index", config["iterKey"])
				assert.Equal(t, "EXECUTION_MODE_PARALLEL", config["executionMode"])

				runner, ok := config["runner"].(map[string]interface{})
				require.True(t, ok, "runner should be a map[string]interface{}")
				assert.Equal(t, "contractRead", runner["type"])

				runnerConfig, ok := runner["config"].(map[string]interface{})
				require.True(t, ok, "runner config should be a map[string]interface{}")
				assert.Equal(t, "0x1234567890123456789012345678901234567890", runnerConfig["contractAddress"])
				// Verify contractAbi is properly converted array
				contractAbi, ok := runnerConfig["contractAbi"].([]interface{})
				require.True(t, ok, "contractAbi should be []interface{}")
				assert.NotEmpty(t, contractAbi, "contractAbi should not be empty")

				methodCalls, ok := runnerConfig["methodCalls"]
				require.True(t, ok, "methodCalls should be present")
				// methodCalls should be a slice that can be converted to protobuf
				assert.NotNil(t, methodCalls)
			},
		},
		{
			name: "ContractWrite runner with method calls",
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_LOOP,
					TaskType: &avsproto.TaskNode_Loop{
						Loop: &avsproto.LoopNode{
							Config: &avsproto.LoopNode_Config{
								InputNodeName: "source-node-id",
								IterVal:       "recipient",
								IterKey:       "index",
								ExecutionMode: avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL,
							},
							Runner: &avsproto.LoopNode_ContractWrite{
								ContractWrite: &avsproto.ContractWriteNode{
									Config: &avsproto.ContractWriteNode_Config{
										ContractAddress: "0x1234567890123456789012345678901234567890",
										ContractAbi:     MustConvertJSONABIToProtobufValues(testTransferABIForConfig),
										CallData:        "0xa9059cbb",
										MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
											{
												CallData:   stringPtr("0xa9059cbb0000000000000000000000009876543210987654321098765432109876543210000000000000000000000000000000000000000000000000de0b6b3a7640000"),
												MethodName: "transfer",
											},
										},
									},
								},
							},
						},
					},
				}
			},
			expectedErr: false,
			validate: func(t *testing.T, config map[string]interface{}) {
				assert.Equal(t, "source-node-id", config["inputNodeName"])
				assert.Equal(t, "recipient", config["iterVal"])
				assert.Equal(t, "index", config["iterKey"])
				assert.Equal(t, "EXECUTION_MODE_SEQUENTIAL", config["executionMode"])

				runner, ok := config["runner"].(map[string]interface{})
				require.True(t, ok, "runner should be a map[string]interface{}")
				assert.Equal(t, "contractWrite", runner["type"])

				runnerConfig, ok := runner["config"].(map[string]interface{})
				require.True(t, ok, "runner config should be a map[string]interface{}")
				assert.Equal(t, "0x1234567890123456789012345678901234567890", runnerConfig["contractAddress"])
				// Verify contractAbi is properly converted array
				contractAbi, ok := runnerConfig["contractAbi"].([]interface{})
				require.True(t, ok, "contractAbi should be []interface{}")
				assert.NotEmpty(t, contractAbi, "contractAbi should not be empty")
				assert.Equal(t, "0xa9059cbb", runnerConfig["callData"])

				methodCalls, ok := runnerConfig["methodCalls"]
				require.True(t, ok, "methodCalls should be present")
				assert.NotNil(t, methodCalls)
			},
		},
		{
			name: "CustomCode runner",
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_LOOP,
					TaskType: &avsproto.TaskNode_Loop{
						Loop: &avsproto.LoopNode{
							Config: &avsproto.LoopNode_Config{
								InputNodeName: "source-node-id",
								IterVal:       "data",
								IterKey:       "i",
								ExecutionMode: avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL,
							},
							Runner: &avsproto.LoopNode_CustomCode{
								CustomCode: &avsproto.CustomCodeNode{
									Config: &avsproto.CustomCodeNode_Config{
										Lang:   avsproto.Lang_JavaScript,
										Source: "return data.value * 2;",
									},
								},
							},
						},
					},
				}
			},
			expectedErr: false,
			validate: func(t *testing.T, config map[string]interface{}) {
				assert.Equal(t, "source-node-id", config["inputNodeName"])
				assert.Equal(t, "data", config["iterVal"])
				assert.Equal(t, "i", config["iterKey"])
				assert.Equal(t, "EXECUTION_MODE_PARALLEL", config["executionMode"])

				runner, ok := config["runner"].(map[string]interface{})
				require.True(t, ok, "runner should be a map[string]interface{}")
				assert.Equal(t, "customCode", runner["type"])

				runnerConfig, ok := runner["config"].(map[string]interface{})
				require.True(t, ok, "runner config should be a map[string]interface{}")
				assert.Equal(t, "return data.value * 2;", runnerConfig["source"])
				assert.Equal(t, "JavaScript", runnerConfig["lang"])
			},
		},
		{
			name: "EthTransfer runner",
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_LOOP,
					TaskType: &avsproto.TaskNode_Loop{
						Loop: &avsproto.LoopNode{
							Config: &avsproto.LoopNode_Config{
								InputNodeName: "source-node-id",
								IterVal:       "recipient",
								IterKey:       "index",
								ExecutionMode: avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL,
							},
							Runner: &avsproto.LoopNode_EthTransfer{
								EthTransfer: &avsproto.ETHTransferNode{
									Config: &avsproto.ETHTransferNode_Config{
										Destination: "{{recipient.address}}",
										Amount:      "{{recipient.amount}}",
									},
								},
							},
						},
					},
				}
			},
			expectedErr: false,
			validate: func(t *testing.T, config map[string]interface{}) {
				assert.Equal(t, "source-node-id", config["inputNodeName"])
				assert.Equal(t, "recipient", config["iterVal"])
				assert.Equal(t, "index", config["iterKey"])
				assert.Equal(t, "EXECUTION_MODE_SEQUENTIAL", config["executionMode"])

				runner, ok := config["runner"].(map[string]interface{})
				require.True(t, ok, "runner should be a map[string]interface{}")
				assert.Equal(t, "ethTransfer", runner["type"])

				runnerConfig, ok := runner["config"].(map[string]interface{})
				require.True(t, ok, "runner config should be a map[string]interface{}")
				assert.Equal(t, "{{recipient.address}}", runnerConfig["destination"])
				assert.Equal(t, "{{recipient.amount}}", runnerConfig["amount"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := tt.setupNode()

			// Test ExtractNodeConfiguration
			config := ExtractNodeConfiguration(node)
			require.NotNil(t, config, "ExtractNodeConfiguration should return a config")

			// Validate the configuration structure
			tt.validate(t, config)

			// Test protobuf conversion - this is the critical test that was failing
			configProto, err := structpb.NewValue(config)
			if tt.expectedErr {
				assert.Error(t, err, "Expected protobuf conversion to fail")
			} else {
				assert.NoError(t, err, "Protobuf conversion should not fail")
				assert.NotNil(t, configProto, "Protobuf value should not be nil")

				// Verify we can convert back to map
				configMap := configProto.AsInterface()
				assert.NotNil(t, configMap, "Should be able to convert protobuf back to interface{}")

				// Verify the structure is preserved
				configMapTyped, ok := configMap.(map[string]interface{})
				require.True(t, ok, "Converted config should be a map[string]interface{}")
				assert.Equal(t, config["inputNodeName"], configMapTyped["inputNodeName"])
				assert.Equal(t, config["iterVal"], configMapTyped["iterVal"])
				assert.Equal(t, config["iterKey"], configMapTyped["iterKey"])
			}
		})
	}
}

func TestExtractNodeConfiguration_ProtobufCompatibility(t *testing.T) {
	// Test that ensures all map[string]string fields are properly converted to map[string]interface{}
	// This is a regression test for the "proto: invalid type: map[string]string" error

	testCases := []struct {
		name      string
		nodeType  avsproto.NodeType
		setupNode func() *avsproto.TaskNode
	}{
		{
			name:     "RestAPI headers conversion",
			nodeType: avsproto.NodeType_NODE_TYPE_LOOP,
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_LOOP,
					TaskType: &avsproto.TaskNode_Loop{
						Loop: &avsproto.LoopNode{
							Config: &avsproto.LoopNode_Config{
								InputNodeName: "test-source",
								IterVal:       "value",
								IterKey:       "key",
							},
							Runner: &avsproto.LoopNode_RestApi{
								RestApi: &avsproto.RestAPINode{
									Config: &avsproto.RestAPINode_Config{
										Url:    MockAPIEndpoint,
										Method: "GET",
										Headers: map[string]string{
											"Authorization": "Bearer test-token",
											"Content-Type":  "application/json",
											"X-Custom":      "custom-value",
										},
									},
								},
							},
						},
					},
				}
			},
		},
		{
			name:     "GraphQL variables conversion",
			nodeType: avsproto.NodeType_NODE_TYPE_LOOP,
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_LOOP,
					TaskType: &avsproto.TaskNode_Loop{
						Loop: &avsproto.LoopNode{
							Config: &avsproto.LoopNode_Config{
								InputNodeName: "test-source",
								IterVal:       "value",
								IterKey:       "key",
							},
							Runner: &avsproto.LoopNode_GraphqlDataQuery{
								GraphqlDataQuery: &avsproto.GraphQLQueryNode{
									Config: &avsproto.GraphQLQueryNode_Config{
										Url:   MockAPIEndpoint + "/graphql",
										Query: "query { test }",
										Variables: map[string]string{
											"var1": "value1",
											"var2": "value2",
											"var3": "value3",
										},
									},
								},
							},
						},
					},
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			node := tc.setupNode()

			// Extract configuration
			config := ExtractNodeConfiguration(node)
			require.NotNil(t, config, "Configuration should not be nil")

			// The critical test: ensure protobuf conversion works without errors
			_, err := structpb.NewValue(config)
			assert.NoError(t, err, "Protobuf conversion should succeed - no map[string]string should remain")

			// Verify that nested arrays are properly converted
			if runner, ok := config["runner"].(map[string]interface{}); ok {
				if headersMap, exists := runner["headersMap"]; exists {
					headersArray, ok := headersMap.([]interface{})
					assert.True(t, ok, "HeadersMap should be []interface{}, not map[string]string")
					assert.NotEmpty(t, headersArray, "HeadersMap array should not be empty")
				}

				if variables, exists := runner["variables"]; exists {
					variablesMap, ok := variables.(map[string]interface{})
					assert.True(t, ok, "Variables should be map[string]interface{}, not map[string]string")
					assert.NotEmpty(t, variablesMap, "Variables map should not be empty")
				}
			}
		})
	}
}

func TestExtractNodeConfiguration_StandaloneNodesProtobufCompatibility(t *testing.T) {
	// Test that ensures standalone nodes also have proper protobuf compatibility
	// This is a regression test to ensure our fixes work for both loop nodes and standalone nodes

	testCases := []struct {
		name      string
		nodeType  avsproto.NodeType
		setupNode func() *avsproto.TaskNode
	}{
		{
			name:     "Standalone RestAPI node",
			nodeType: avsproto.NodeType_NODE_TYPE_REST_API,
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_REST_API,
					TaskType: &avsproto.TaskNode_RestApi{
						RestApi: &avsproto.RestAPINode{
							Config: &avsproto.RestAPINode_Config{
								Url:    MockAPIEndpoint,
								Method: "GET",
								Headers: map[string]string{
									"Authorization": "Bearer test-token",
									"Content-Type":  "application/json",
								},
							},
						},
					},
				}
			},
		},
		{
			name:     "Standalone GraphQL node",
			nodeType: avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY,
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY,
					TaskType: &avsproto.TaskNode_GraphqlQuery{
						GraphqlQuery: &avsproto.GraphQLQueryNode{
							Config: &avsproto.GraphQLQueryNode_Config{
								Url:   MockAPIEndpoint + "/graphql",
								Query: "query { test }",
								Variables: map[string]string{
									"var1": "value1",
									"var2": "value2",
								},
							},
						},
					},
				}
			},
		},
		{
			name:     "Standalone ContractRead node",
			nodeType: avsproto.NodeType_NODE_TYPE_CONTRACT_READ,
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_CONTRACT_READ,
					TaskType: &avsproto.TaskNode_ContractRead{
						ContractRead: &avsproto.ContractReadNode{
							Config: &avsproto.ContractReadNode_Config{
								ContractAddress: "0x1234567890123456789012345678901234567890",
								ContractAbi:     MustConvertJSONABIToProtobufValues(testDecimalsABIForConfig),
								MethodCalls: []*avsproto.ContractReadNode_MethodCall{
									{
										CallData:      stringPtr("0x313ce567"),
										MethodName:    "decimals",
										ApplyToFields: []string{"balance", "amount"},
									},
								},
							},
						},
					},
				}
			},
		},
		{
			name:     "Standalone ContractWrite node",
			nodeType: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			setupNode: func() *avsproto.TaskNode {
				return &avsproto.TaskNode{
					Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
					TaskType: &avsproto.TaskNode_ContractWrite{
						ContractWrite: &avsproto.ContractWriteNode{
							Config: &avsproto.ContractWriteNode_Config{
								ContractAddress: "0x1234567890123456789012345678901234567890",
								ContractAbi:     MustConvertJSONABIToProtobufValues(testSimpleTransferABI),
								MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
									{
										CallData:   stringPtr("0xa9059cbb"),
										MethodName: "transfer",
									},
								},
							},
						},
					},
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			node := tc.setupNode()

			// Extract configuration
			config := ExtractNodeConfiguration(node)
			require.NotNil(t, config, "Configuration should not be nil")

			// The critical test: ensure protobuf conversion works without errors
			_, err := structpb.NewValue(config)
			assert.NoError(t, err, "Protobuf conversion should succeed for standalone nodes")

			// Additional verification for specific node types
			switch tc.nodeType {
			case avsproto.NodeType_NODE_TYPE_REST_API:
				// REST API should use headersMap format for consistency
				assert.Contains(t, config, "headersMap", "REST API standalone node should have headersMap")
				assert.NotContains(t, config, "headers", "REST API standalone node should not have headers")

			case avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY:
				// GraphQL variables should be converted to interface{}
				if variables, exists := config["variables"]; exists {
					variablesMap, ok := variables.(map[string]interface{})
					assert.True(t, ok, "GraphQL variables should be map[string]interface{}")
					assert.NotEmpty(t, variablesMap, "Variables map should not be empty")
				}

			case avsproto.NodeType_NODE_TYPE_CONTRACT_READ, avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE:
				// Contract nodes should have properly converted methodCalls
				if methodCalls, exists := config["methodCalls"]; exists {
					methodCallsArray, ok := methodCalls.([]interface{})
					assert.True(t, ok, "MethodCalls should be []interface{}")
					assert.NotEmpty(t, methodCallsArray, "MethodCalls array should not be empty")
				}
			}
		})
	}
}
