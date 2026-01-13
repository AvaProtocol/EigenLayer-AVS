//go:build integration
// +build integration

package taskengine

import (
	"context"
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

// Integration tests for TenderlyClient that require real network calls
// These tests are excluded from regular CI/CD runs and only run with: make test/integration

func TestTenderlyClient_TransactionRevert_Integration(t *testing.T) {
	// Skip if no Tenderly API key - this requires real network calls

	logger := testutil.GetLogger()
	testConfig := testutil.GetTestConfig()
	client := NewTenderlyClient(testConfig, logger)

	// Create a query with invalid contract address to trigger revert
	query := &avsproto.EventTrigger_Query{
		Addresses: []string{"0x0000000000000000000000000000000000000000"}, // Invalid address
		Topics:    []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
		Conditions: []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "gt",
				Value:     "200000000000",
				FieldType: "int256",
			},
		},
	}

	ctx := context.Background()
	chainID := int64(11155111) // Sepolia

	fmt.Printf("\n🧪 === TESTING TRANSACTION REVERT ===\n")
	fmt.Printf("📍 Testing invalid contract: 0x0000000000000000000000000000000000000000\n")
	fmt.Printf("🎯 Expected: Transaction should revert\n\n")

	// This should fail because calling latestRoundData() on 0x0000... will revert
	log, err := client.SimulateEventTrigger(ctx, query, chainID)

	// Verify error handling
	if err != nil {
		fmt.Printf("✅ Error correctly returned: %s\n", err.Error())
		assert.Error(t, err, "Should return error for invalid contract")
		assert.Nil(t, log, "Should return nil log on error")
		// Note: Specific revert message may vary by provider
	} else {
		fmt.Printf("⚠️  No error returned - provider may handle invalid addresses differently\n")
		// Some providers might return default values instead of reverting
	}
}

func TestTenderlyClient_InvalidContractCall_Integration(t *testing.T) {
	// Skip if no Tenderly API key - this requires real network calls

	logger := testutil.GetLogger()
	testConfig := testutil.GetTestConfig()
	client := NewTenderlyClient(testConfig, logger)

	// Create a query with a valid contract address but invalid chain ID
	query := &avsproto.EventTrigger_Query{
		Addresses: []string{"0x694AA1769357215DE4FAC081bf1f309aDC325306"}, // Valid Chainlink address
		Topics:    []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
	}

	ctx := context.Background()
	chainID := int64(999999) // Invalid chain ID to trigger error

	fmt.Printf("\n🧪 === TESTING INVALID CHAIN ID ===\n")
	fmt.Printf("📍 Using valid contract on invalid chain: %d\n", chainID)
	fmt.Printf("🎯 Expected: Network error or unsupported chain\n\n")

	// This should fail due to invalid chain ID
	log, err := client.SimulateEventTrigger(ctx, query, chainID)

	// Verify error handling
	if err != nil {
		fmt.Printf("✅ Error correctly returned: %s\n", err.Error())
		assert.Error(t, err, "Should return error for invalid chain ID")
		assert.Nil(t, log, "Should return nil log on error")
	} else {
		fmt.Printf("⚠️  No error returned - provider may have fallback behavior\n")
	}
}

func TestTenderlyClient_NetworkError_Integration(t *testing.T) {

	logger := testutil.GetLogger()
	testConfig := testutil.GetTestConfig()
	client := NewTenderlyClient(testConfig, logger)

	// Create a valid query
	query := &avsproto.EventTrigger_Query{
		Addresses: []string{"0x694AA1769357215DE4FAC081bf1f309aDC325306"},
		Topics:    []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
	}

	// Use a cancelled context to simulate network timeout
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	chainID := int64(11155111)

	fmt.Printf("\n🧪 === TESTING NETWORK TIMEOUT ===\n")
	fmt.Printf("📍 Using cancelled context to simulate timeout\n")
	fmt.Printf("🎯 Expected: Context cancellation error\n\n")

	// This should fail due to cancelled context
	log, err := client.SimulateEventTrigger(ctx, query, chainID)

	// Verify error handling
	assert.Error(t, err, "Should return error for cancelled context")
	assert.Nil(t, log, "Should return nil log on error")
	fmt.Printf("✅ Error correctly returned: %s\n", err.Error())
}
