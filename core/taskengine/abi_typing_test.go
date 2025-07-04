package taskengine

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestABIFieldTyping(t *testing.T) {
	// Test ABI for a sample event with various types
	testABI := `[{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "address", "name": "from", "type": "address"},
			{"indexed": true, "internalType": "address", "name": "to", "type": "address"},
			{"indexed": false, "internalType": "uint256", "name": "value", "type": "uint256"},
			{"indexed": false, "internalType": "uint8", "name": "decimals", "type": "uint8"},
			{"indexed": false, "internalType": "bool", "name": "success", "type": "bool"},
			{"indexed": false, "internalType": "string", "name": "message", "type": "string"}
		],
		"name": "TestEvent",
		"type": "event"
	}]`

	// Parse ABI to get the correct event signature
	parsedABI, err := abi.JSON(strings.NewReader(testABI))
	if err != nil {
		t.Fatalf("Failed to parse test ABI: %v", err)
	}

	testEvent := parsedABI.Events["TestEvent"]
	eventSignature := testEvent.ID

	// Create a mock Engine
	engine := &Engine{
		logger: nil,
	}

	// Create a mock event log
	mockLog := &types.Log{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
		Topics: []common.Hash{
			eventSignature, // Use the correct event signature
			common.BytesToHash(common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd").Bytes()), // from address
			common.BytesToHash(common.HexToAddress("0x1111111111111111111111111111111111111111").Bytes()), // to address
		},
		Data: func() []byte {
			// Pack test data: value (uint256), decimals (uint8), success (bool), message (string)
			// Create test values
			value := big.NewInt(1000000000000000000) // 1 ETH in wei
			decimals := uint8(18)
			success := true
			message := "Test message"

			// Pack the data
			data, _ := testEvent.Inputs.NonIndexed().Pack(value, decimals, success, message)
			return data
		}(),
	}

	// Test the parseEventWithABI function
	parsedData, err := engine.parseEventWithABI(mockLog, testABI, nil)
	if err != nil {
		t.Fatalf("Failed to parse event with ABI: %v", err)
	}

	// Verify the event name
	if parsedData["eventName"] != "TestEvent" {
		t.Errorf("Expected eventName to be 'TestEvent', got %v", parsedData["eventName"])
	}

	// Verify address fields are hex strings (adjust for Ethereum address checksumming)
	fromAddr := parsedData["from"].(string)
	if !strings.EqualFold(fromAddr, "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd") {
		t.Errorf("Expected 'from' to be a hex address (case insensitive), got %v (type: %T)", parsedData["from"], parsedData["from"])
	}

	if parsedData["to"] != "0x1111111111111111111111111111111111111111" {
		t.Errorf("Expected 'to' to be a hex address, got %v (type: %T)", parsedData["to"], parsedData["to"])
	}

	// Verify uint256 is returned as string (to avoid precision loss)
	if parsedData["value"] != "1000000000000000000" {
		t.Errorf("Expected 'value' to be '1000000000000000000', got %v (type: %T)", parsedData["value"], parsedData["value"])
	}

	// Verify uint8 is returned as string (since the current implementation converts to string)
	if parsedData["decimals"] != "18" {
		t.Errorf("Expected 'decimals' to be '18', got %v (type: %T)", parsedData["decimals"], parsedData["decimals"])
	}

	// Verify bool is returned as actual boolean
	if parsedData["success"] != true {
		t.Errorf("Expected 'success' to be true, got %v (type: %T)", parsedData["success"], parsedData["success"])
	}

	// Verify string is returned as string
	if parsedData["message"] != "Test message" {
		t.Errorf("Expected 'message' to be 'Test message', got %v (type: %T)", parsedData["message"], parsedData["message"])
	}

	// Print the parsed data for debugging
	t.Logf("Parsed event data: %+v", parsedData)
}

func TestContractReadABITyping(t *testing.T) {
	// Test ABI for a contract read method with various return types
	testABI := `[{
		"inputs": [],
		"name": "getInfo",
		"outputs": [
			{"internalType": "uint256", "name": "totalSupply", "type": "uint256"},
			{"internalType": "uint8", "name": "decimals", "type": "uint8"},
			{"internalType": "bool", "name": "paused", "type": "bool"},
			{"internalType": "address", "name": "owner", "type": "address"},
			{"internalType": "string", "name": "name", "type": "string"}
		],
		"stateMutability": "view",
		"type": "function"
	}]`

	// Create a mock ContractReadProcessor
	processor := &ContractReadProcessor{
		CommonProcessor: &CommonProcessor{
			vm: &VM{},
		},
	}

	// Parse the ABI
	parsedABI, err := abi.JSON(strings.NewReader(testABI))
	if err != nil {
		t.Fatalf("Failed to parse ABI: %v", err)
	}

	method := parsedABI.Methods["getInfo"]

	// Create mock result data
	result := []interface{}{
		big.NewInt(1000000000000000000), // totalSupply (uint256)
		big.NewInt(18),                  // decimals (uint8)
		true,                            // paused (bool)
		common.HexToAddress("0x1234567890123456789012345678901234567890"), // owner (address)
		"Test Token", // name (string)
	}

	// Test the buildStructuredData function
	structuredFields, err := processor.buildStructuredData(&method, result)
	if err != nil {
		t.Fatalf("Failed to build structured data: %v", err)
	}

	// Convert to map for easier testing
	fieldMap := make(map[string]string)
	for _, field := range structuredFields {
		fieldMap[field.Name] = field.Value
	}

	// Verify uint256 is returned as string
	if fieldMap["totalSupply"] != "1000000000000000000" {
		t.Errorf("Expected 'totalSupply' to be '1000000000000000000', got %v", fieldMap["totalSupply"])
	}

	// Verify uint8 is returned as numeric string (smaller integers)
	if fieldMap["decimals"] != "18" {
		t.Errorf("Expected 'decimals' to be '18', got %v", fieldMap["decimals"])
	}

	// Verify bool is returned as string representation
	if fieldMap["paused"] != "true" {
		t.Errorf("Expected 'paused' to be 'true', got %v", fieldMap["paused"])
	}

	// Verify address is returned as hex string
	if fieldMap["owner"] != "0x1234567890123456789012345678901234567890" {
		t.Errorf("Expected 'owner' to be '0x1234567890123456789012345678901234567890', got %v", fieldMap["owner"])
	}

	// Verify string is returned as-is
	if fieldMap["name"] != "Test Token" {
		t.Errorf("Expected 'name' to be 'Test Token', got %v", fieldMap["name"])
	}

	// Print the structured fields for debugging
	fieldsJSON, _ := json.MarshalIndent(structuredFields, "", "  ")
	t.Logf("Structured fields: %s", fieldsJSON)
}

func TestDecimalFormattingWithProperTypes(t *testing.T) {
	// Test ABI for an ERC20 Transfer event with decimal formatting
	testABI := `[{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "address", "name": "from", "type": "address"},
			{"indexed": true, "internalType": "address", "name": "to", "type": "address"},
			{"indexed": false, "internalType": "uint256", "name": "value", "type": "uint256"}
		],
		"name": "Transfer",
		"type": "event"
	}]`

	// Create a mock Engine
	engine := &Engine{
		logger: nil,
	}

	// Create a mock event log for a transfer of 1.5 tokens (with 18 decimals)
	mockLog := &types.Log{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
		Topics: []common.Hash{
			common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"),        // Transfer event signature
			common.BytesToHash(common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd").Bytes()), // from address
			common.BytesToHash(common.HexToAddress("0x1111111111111111111111111111111111111111").Bytes()), // to address
		},
		Data: func() []byte {
			// Pack 1.5 tokens with 18 decimals = 1500000000000000000 wei
			parsedABI, _ := abi.JSON(strings.NewReader(testABI))
			event := parsedABI.Events["Transfer"]
			value := big.NewInt(1500000000000000000) // 1.5 ETH in wei
			data, _ := event.Inputs.NonIndexed().Pack(value)
			return data
		}(),
	}

	// Mock the callContractMethod to return 18 decimals
	originalRpcConn := rpcConn
	defer func() { rpcConn = originalRpcConn }()

	// For this test, we'll simulate the decimal formatting by setting decimalsValue directly
	// In a real scenario, this would be retrieved via RPC call

	// Test without decimal formatting first
	parsedData, err := engine.parseEventWithABI(mockLog, testABI, nil)
	if err != nil {
		t.Fatalf("Failed to parse event with ABI: %v", err)
	}

	// Verify the raw value is correct (should be string for uint256)
	if parsedData["value"] != "1500000000000000000" {
		t.Errorf("Expected 'value' to be '1500000000000000000', got %v", parsedData["value"])
	}

	// Test with decimal formatting (this would require a working RPC connection in practice)
	// For now, just verify the structure is correct
	t.Logf("Parsed event data without decimal formatting: %+v", parsedData)

	// The decimal formatting would be tested in integration tests with actual RPC calls
}
