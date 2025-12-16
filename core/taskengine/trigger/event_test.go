package trigger

import (
	"sync"
	"testing"
	"time"

	"strconv"
	"strings"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
)

// MockLogger is a simple mock logger for testing
type MockLogger struct{}

func (m *MockLogger) Debug(msg string, tags ...any) {}
func (m *MockLogger) Info(msg string, tags ...any)  {}
func (m *MockLogger) Warn(msg string, tags ...any)  {}
func (m *MockLogger) Error(msg string, tags ...any) {}
func (m *MockLogger) Fatal(msg string, tags ...any) {}

func (m *MockLogger) Debugf(template string, args ...interface{}) {}
func (m *MockLogger) Infof(template string, args ...interface{})  {}
func (m *MockLogger) Warnf(template string, args ...interface{})  {}
func (m *MockLogger) Errorf(template string, args ...interface{}) {}
func (m *MockLogger) Fatalf(template string, args ...interface{}) {}

func (m *MockLogger) With(tags ...any) sdklogging.Logger { return m }

func TestBuildFilterQueriesOptimization(t *testing.T) {
	// Create a mock event trigger with proper initialization
	trigger := &EventTrigger{
		registry:   NewTaskRegistry(),
		checks:     sync.Map{},
		legacyMode: false,
		CommonTrigger: &CommonTrigger{
			done:      make(chan bool),
			shutdown:  false,
			rpcOption: &RpcOption{},
			logger:    &MockLogger{},
		},
		triggerCh:                make(chan TriggerMetadata[EventMark], 10),
		subscriptions:            make([]SubscriptionInfo, 0),
		updateSubsCh:             make(chan struct{}, 1),
		eventCounts:              make(map[string]map[uint64]uint32),
		defaultMaxEventsPerQuery: 100,
		defaultMaxTotalEvents:    1000,
		processedEvents:          make(map[string]bool),
	}

	// Create a task with 2 identical queries
	taskID := "test-task-123"
	maxEvents1 := uint32(100)
	maxEvents2 := uint32(200)

	query1 := &avsproto.EventTrigger_Query{
		Addresses: []string{
			"0x1f9840a85d5af5bf1d1762f925bdaddc4201f984",
			"0x20c54c5f742f123abb49a982bfe0af47edb38756",
		},
		Topics: []string{
			"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
		},
		MaxEventsPerBlock: &maxEvents1,
	}

	query2 := &avsproto.EventTrigger_Query{
		Addresses: []string{
			"0x1f9840a85d5af5bf1d1762f925bdaddc4201f984",
			"0x20c54c5f742f123abb49a982bfe0af47edb38756",
		},
		Topics: []string{
			"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
		},
		MaxEventsPerBlock: &maxEvents2, // Different max events
	}

	check := &Check{
		TaskMetadata: &avsproto.SyncMessagesResp_TaskMetadata{
			TaskId: taskID,
		},
		Queries: []*avsproto.EventTrigger_Query{query1, query2},
	}

	trigger.checks.Store(taskID, check)

	// Build optimized queries
	queries := trigger.buildFilterQueries()

	// Should have only 1 query (the identical queries should be combined)
	assert.Equal(t, 1, len(queries), "Should combine identical queries into one")

	// Check that the combined query has the higher maxEventsPerBlock
	assert.Equal(t, uint32(200), queries[0].MaxEventsPerBlock, "Should use the higher maxEventsPerBlock")

	// Check that the description indicates combined queries
	assert.Contains(t, queries[0].Description, "(combined 2 identical queries)", "Description should indicate query combination")

	// Verify the query criteria are correct
	expectedAddresses := []common.Address{
		common.HexToAddress("0x1f9840a85d5af5bf1d1762f925bdaddc4201f984"),
		common.HexToAddress("0x20c54c5f742f123abb49a982bfe0af47edb38756"),
	}
	assert.Equal(t, expectedAddresses, queries[0].Query.Addresses, "Addresses should match")

	expectedTopic := common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	assert.Equal(t, [][]common.Hash{{expectedTopic}}, queries[0].Query.Topics, "Topics should match")
}

func TestBuildFilterQueriesDistinguishFromTo(t *testing.T) {
	// Create a mock event trigger with proper initialization
	trigger := &EventTrigger{
		registry:   NewTaskRegistry(),
		checks:     sync.Map{},
		legacyMode: false,
		CommonTrigger: &CommonTrigger{
			done:      make(chan bool),
			shutdown:  false,
			rpcOption: &RpcOption{},
			logger:    &MockLogger{},
		},
		triggerCh:                make(chan TriggerMetadata[EventMark], 10),
		subscriptions:            make([]SubscriptionInfo, 0),
		updateSubsCh:             make(chan struct{}, 1),
		eventCounts:              make(map[string]map[uint64]uint32),
		defaultMaxEventsPerQuery: 100,
		defaultMaxTotalEvents:    1000,
		processedEvents:          make(map[string]bool),
	}

	// Create a task with FROM and TO transfer queries (different queries)
	taskID := "test-task-from-to"
	maxEvents := uint32(100)
	coreAddress := "0xfe66125343aabda4a330da667431ec1acb7bbda9"

	// Query 1: Transfer FROM core.address (topic[1] = coreAddress, topic[2] = wildcard)
	query1 := &avsproto.EventTrigger_Query{
		Addresses: []string{
			"0x1f9840a85d5af5bf1d1762f925bdaddc4201f984",
			"0x20c54c5f742f123abb49a982bfe0af47edb38756",
		},
		Topics: []string{
			"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
			coreAddress,
			"", // topic[2] empty string for wildcard
		},
		MaxEventsPerBlock: &maxEvents,
	}

	// Query 2: Transfer TO core.address (topic[1] = wildcard, topic[2] = coreAddress)
	query2 := &avsproto.EventTrigger_Query{
		Addresses: []string{
			"0x1f9840a85d5af5bf1d1762f925bdaddc4201f984",
			"0x20c54c5f742f123abb49a982bfe0af47edb38756",
		},
		Topics: []string{
			"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
			"", // topic[1] empty string for wildcard
			coreAddress,
		},
		MaxEventsPerBlock: &maxEvents,
	}

	check := &Check{
		TaskMetadata: &avsproto.SyncMessagesResp_TaskMetadata{
			TaskId: taskID,
		},
		Queries: []*avsproto.EventTrigger_Query{query1, query2},
	}

	trigger.checks.Store(taskID, check)

	// Build optimized queries
	queries := trigger.buildFilterQueries()

	// Should have 2 queries (FROM and TO are different)
	assert.Equal(t, 2, len(queries), "Should NOT combine FROM and TO transfer queries")

	// Check the structure of the topics array for FROM and TO
	var foundFrom, foundTo bool
	for _, q := range queries {
		topics := q.Query.Topics
		// Debug print
		topicSummary := make([]string, len(topics))
		for i, t := range topics {
			if t == nil {
				topicSummary[i] = "nil"
			} else {
				topicSummary[i] = "["
				for _, h := range t {
					topicSummary[i] += h.Hex() + ","
				}
				topicSummary[i] += "]"
			}
		}
		t.Logf("Query %s topics: %v", q.Description, topicSummary)

		if len(topics) == 3 {
			// FROM: [eventSig], [coreAddress], nil
			if len(topics[0]) == 1 && len(topics[1]) == 1 && topics[2] == nil {
				foundFrom = true
			}
			// TO: [eventSig], nil, [coreAddress]
			if len(topics[0]) == 1 && topics[1] == nil && len(topics[2]) == 1 {
				foundTo = true
			}
		}
	}
	assert.True(t, foundFrom, "Should find FROM transfer query with correct topic structure")
	assert.True(t, foundTo, "Should find TO transfer query with correct topic structure")
}

func TestCreateQueryKey(t *testing.T) {
	trigger := &EventTrigger{}

	// Test query key generation
	query := ethereum.FilterQuery{
		Addresses: []common.Address{
			common.HexToAddress("0x1f9840a85d5af5bf1d1762f925bdaddc4201f984"),
			common.HexToAddress("0x20c54c5f742f123abb49a982bfe0af47edb38756"),
		},
		Topics: [][]common.Hash{
			{common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")},
		},
	}

	key1 := trigger.createQueryKey(query)
	key2 := trigger.createQueryKey(query)

	// Same query should generate same key
	assert.Equal(t, key1, key2, "Same query should generate same key")

	// Key should contain addresses and topics
	assert.Contains(t, key1, "addrs:", "Key should contain addresses")
	assert.Contains(t, key1, "topic[0]:", "Key should contain topics")
}

func TestEventTriggerQueryDeduplication(t *testing.T) {
	// Inline convertToFilterQuery and createQueryKey from event.go
	convertToFilterQuery := func(query *avsproto.EventTrigger_Query) ethereum.FilterQuery {
		var addresses []common.Address
		for _, addrStr := range query.GetAddresses() {
			if addr := common.HexToAddress(addrStr); addr != (common.Address{}) {
				addresses = append(addresses, addr)
			}
		}

		var topics [][]common.Hash
		// New flat array format: topics is []string where each string is a topic value
		for _, topicStr := range query.GetTopics() {
			if topicStr == "" {
				// Empty string represents null/wildcard
				topics = append(topics, nil)
			} else {
				if hash := common.HexToHash(topicStr); hash != (common.Hash{}) {
					topics = append(topics, []common.Hash{hash})
				} else {
					topics = append(topics, nil)
				}
			}
		}

		return ethereum.FilterQuery{
			Addresses: addresses,
			Topics:    topics,
		}
	}

	createQueryKey := func(query ethereum.FilterQuery) string {
		var keyParts []string
		if len(query.Addresses) > 0 {
			addresses := make([]string, len(query.Addresses))
			for i, addr := range query.Addresses {
				addresses[i] = addr.Hex()
			}
			// No need to sort for this test
			keyParts = append(keyParts, "addrs:"+strings.Join(addresses, ","))
		}
		if len(query.Topics) > 0 {
			for i, topicGroup := range query.Topics {
				idxStr := strconv.Itoa(i)
				if len(topicGroup) > 0 {
					hasWildcard := false
					specificTopics := make([]string, 0)
					for _, topic := range topicGroup {
						if topic == (common.Hash{}) {
							hasWildcard = true
						} else {
							specificTopics = append(specificTopics, topic.Hex())
						}
					}
					if hasWildcard {
						keyParts = append(keyParts, "topic["+idxStr+"]:wildcard+"+strings.Join(specificTopics, ","))
					} else {
						keyParts = append(keyParts, "topic["+idxStr+"]:"+strings.Join(specificTopics, ","))
					}
				} else {
					keyParts = append(keyParts, "topic["+idxStr+"]:any")
				}
			}
		}
		return strings.Join(keyParts, "|")
	}

	// Test address
	targetAddress := "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9"

	// Create FROM and TO queries
	fromQuery := &avsproto.EventTrigger_Query{
		Addresses: []string{},
		Topics: []string{
			"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // Transfer signature
			targetAddress, // FROM address
			"",            // Any TO address (wildcard)
		},
	}

	toQuery := &avsproto.EventTrigger_Query{
		Addresses: []string{},
		Topics: []string{
			"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // Transfer signature
			"",            // Any FROM address (wildcard)
			targetAddress, // TO address
		},
	}

	// Convert to ethereum.FilterQuery
	fromEthQuery := convertToFilterQuery(fromQuery)
	toEthQuery := convertToFilterQuery(toQuery)

	// Generate keys
	fromKey := createQueryKey(fromEthQuery)
	toKey := createQueryKey(toEthQuery)

	t.Logf("FROM query key: %s", fromKey)
	t.Logf("TO query key: %s", toKey)

	// Verify keys are different
	if fromKey == toKey {
		t.Errorf("FROM and TO queries should have different keys, but both have: %s", fromKey)
	} else {
		t.Logf("✅ FROM and TO queries correctly have different keys")
	}

	// Verify the keys contain the expected information
	if !strings.Contains(fromKey, "topic[1]:") {
		t.Errorf("FROM query key should contain topic[1] (FROM address), got: %s", fromKey)
	}
	if !strings.Contains(fromKey, "topic[2]:any") {
		t.Errorf("FROM query key should contain topic[2]:any (wildcard TO), got: %s", fromKey)
	}

	if !strings.Contains(toKey, "topic[1]:any") {
		t.Errorf("TO query key should contain topic[1]:any (wildcard FROM), got: %s", toKey)
	}
	if !strings.Contains(toKey, "topic[2]:") {
		t.Errorf("TO query key should contain topic[2] (TO address), got: %s", toKey)
	}
}

func TestConvertToFilterQueryClientFormat(t *testing.T) {
	// Inline convertToFilterQuery from event.go
	convertToFilterQuery := func(query *avsproto.EventTrigger_Query) ethereum.FilterQuery {
		var addresses []common.Address
		for _, addrStr := range query.GetAddresses() {
			if addr := common.HexToAddress(addrStr); addr != (common.Address{}) {
				addresses = append(addresses, addr)
			}
		}

		var topics [][]common.Hash
		// New flat array format: topics is []string where each string is a topic value
		for _, topicStr := range query.GetTopics() {
			if topicStr == "" {
				// Empty string represents null/wildcard
				topics = append(topics, nil)
			} else {
				if hash := common.HexToHash(topicStr); hash != (common.Hash{}) {
					topics = append(topics, []common.Hash{hash})
				} else {
					topics = append(topics, nil)
				}
			}
		}

		return ethereum.FilterQuery{
			Addresses: addresses,
			Topics:    topics,
		}
	}

	// Test address
	targetAddress := "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9"

	// Create FROM query in client format (flat topics array)
	fromQuery := &avsproto.EventTrigger_Query{
		Addresses: []string{},
		Topics: []string{
			"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // Transfer signature
			targetAddress, // FROM address
			"",            // Any TO address (wildcard)
		},
	}

	// Create TO query in client format (flat topics array)
	toQuery := &avsproto.EventTrigger_Query{
		Addresses: []string{},
		Topics: []string{
			"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // Transfer signature
			"",            // Any FROM address (wildcard)
			targetAddress, // TO address
		},
	}

	// Convert to ethereum.FilterQuery
	fromEthQuery := convertToFilterQuery(fromQuery)
	toEthQuery := convertToFilterQuery(toQuery)

	// Verify the conversion worked correctly
	if len(fromEthQuery.Topics) != 3 {
		t.Errorf("FROM query should have 3 topic positions, got %d", len(fromEthQuery.Topics))
	}

	if len(toEthQuery.Topics) != 3 {
		t.Errorf("TO query should have 3 topic positions, got %d", len(toEthQuery.Topics))
	}

	// Verify FROM query structure
	if len(fromEthQuery.Topics[0]) != 1 || fromEthQuery.Topics[0][0].Hex() != "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" {
		t.Errorf("FROM query topic[0] should contain Transfer signature")
	}
	if len(fromEthQuery.Topics[1]) != 1 || fromEthQuery.Topics[1][0].Hex() != "0x000000000000000000000000fe66125343aabda4a330da667431ec1acb7bbda9" {
		t.Errorf("FROM query topic[1] should contain FROM address")
	}
	if fromEthQuery.Topics[2] != nil {
		t.Errorf("FROM query topic[2] should be nil (wildcard)")
	}

	// Verify TO query structure
	if len(toEthQuery.Topics[0]) != 1 || toEthQuery.Topics[0][0].Hex() != "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" {
		t.Errorf("TO query topic[0] should contain Transfer signature")
	}
	if toEthQuery.Topics[1] != nil {
		t.Errorf("TO query topic[1] should be nil (wildcard)")
	}
	if len(toEthQuery.Topics[2]) != 1 || toEthQuery.Topics[2][0].Hex() != "0x000000000000000000000000fe66125343aabda4a330da667431ec1acb7bbda9" {
		t.Errorf("TO query topic[2] should contain TO address")
	}

	t.Logf("✅ FROM and TO queries correctly converted to different ethereum.FilterQuery structures")
	t.Logf("FROM query topics: %v", fromEthQuery.Topics)
	t.Logf("TO query topics: %v", toEthQuery.Topics)
}

func TestTOSubscriptionFilter(t *testing.T) {
	// Test the TO subscription filter structure
	toQuery := &avsproto.EventTrigger_Query{
		Addresses: []string{
			"0x1f9840a85d5af5bf1d1762f925bdaddc4201f984",
			"0x20c54c5f742f123abb49a982bfe0af47edb38756",
			"0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		},
		Topics: []string{
			"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // Transfer signature
			"", // Any FROM address (wildcard)
			"0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9", // TO address
		},
	}

	// Inline convertToFilterQuery
	convertToFilterQuery := func(query *avsproto.EventTrigger_Query) ethereum.FilterQuery {
		var addresses []common.Address
		for _, addrStr := range query.GetAddresses() {
			if addr := common.HexToAddress(addrStr); addr != (common.Address{}) {
				addresses = append(addresses, addr)
			}
		}

		var topics [][]common.Hash
		// New flat array format: topics is []string where each string is a topic value
		for _, topicStr := range query.GetTopics() {
			if topicStr == "" {
				// Empty string represents null/wildcard
				topics = append(topics, nil)
			} else {
				if hash := common.HexToHash(topicStr); hash != (common.Hash{}) {
					topics = append(topics, []common.Hash{hash})
				} else {
					topics = append(topics, nil)
				}
			}
		}

		return ethereum.FilterQuery{
			Addresses: addresses,
			Topics:    topics,
		}
	}

	ethQuery := convertToFilterQuery(toQuery)

	t.Logf("TO Query structure:")
	t.Logf("  Addresses: %v", ethQuery.Addresses)
	t.Logf("  Topics: %v", ethQuery.Topics)

	// Verify the structure
	if len(ethQuery.Topics) != 3 {
		t.Errorf("Expected 3 topic positions, got %d", len(ethQuery.Topics))
	}

	// Topic 0 should be Transfer signature
	if len(ethQuery.Topics[0]) != 1 {
		t.Errorf("Topic 0 should have 1 value (Transfer signature), got %d", len(ethQuery.Topics[0]))
	}

	// Topic 1 should be nil (wildcard)
	if ethQuery.Topics[1] != nil {
		t.Errorf("Topic 1 should be nil (wildcard FROM), got %v", ethQuery.Topics[1])
	}

	// Topic 2 should be TO address
	if len(ethQuery.Topics[2]) != 1 {
		t.Errorf("Topic 2 should have 1 value (TO address), got %d", len(ethQuery.Topics[2]))
	}

	expectedTOAddr := common.HexToAddress("0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9")
	if ethQuery.Topics[2][0] != common.HexToHash(expectedTOAddr.Hex()) {
		t.Errorf("Topic 2 should contain TO address %s, got %s", expectedTOAddr.Hex(), ethQuery.Topics[2][0].Hex())
	}

	t.Logf("✅ TO subscription filter structure is correct")
}

func TestTOAddressFormat(t *testing.T) {
	// Test the exact format of the TO address in the filter
	targetAddress := "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9"

	// Convert to the format used in the filter
	addr := common.HexToAddress(targetAddress)
	addrHash := common.HexToHash(addr.Hex())

	t.Logf("Target address: %s", targetAddress)
	t.Logf("Address hash: %s", addrHash.Hex())

	// This should match the format in the filter
	expectedFilterFormat := "0x000000000000000000000000fe66125343aabda4a330da667431ec1acb7bbda9"

	if addrHash.Hex() != expectedFilterFormat {
		t.Errorf("Address format mismatch. Expected: %s, Got: %s", expectedFilterFormat, addrHash.Hex())
	} else {
		t.Logf("✅ Address format is correct")
	}

	// Test the actual event data from the transaction
	// From the transaction: To: 0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9
	eventToAddr := common.HexToAddress("0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9")
	eventToHash := common.HexToHash(eventToAddr.Hex())

	t.Logf("Event TO address: %s", eventToAddr.Hex())
	t.Logf("Event TO hash: %s", eventToHash.Hex())

	// These should match
	if addrHash == eventToHash {
		t.Logf("✅ Addresses match correctly")
	} else {
		t.Errorf("❌ Addresses don't match. Filter: %s, Event: %s", addrHash.Hex(), eventToHash.Hex())
	}
}

func TestExactTransactionMatch(t *testing.T) {
	// Test the exact transaction from Etherscan: 0x2e52c134f543a5930b104dc6ffb572595d98d29d70be765c405a58ce27331cf4
	// Block: 8559161, Contract: 0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238
	// From: 0x274888BaB7Cf5191b17E54618F5F2822dF76b05F
	// To: 0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9

	// Create the exact log from the transaction
	log := types.Log{
		Address: common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"),
		Topics: []common.Hash{
			common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"), // Transfer event
			common.HexToHash("0x000000000000000000000000274888bab7cf5191b17e54618f5f2822df76b05f"), // from
			common.HexToHash("0x000000000000000000000000fe66125343aabda4a330da667431ec1acb7bbda9"), // to
		},
		BlockNumber: 8559161,
		TxHash:      common.HexToHash("0x2e52c134f543a5930b104dc6ffb572595d98d29d70be765c405a58ce27331cf4"),
		Index:       0,
	}

	// Create the TO subscription filter (same as in your logs)
	toFilter := ethereum.FilterQuery{
		Addresses: []common.Address{
			common.HexToAddress("0x1f9840a85d5aF5bf1D1762F925BDADdC4201F984"),
			common.HexToAddress("0x20c54C5F742F123Abb49a982BFe0af47edb38756"),
			common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"),
		},
		Topics: [][]common.Hash{
			{common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")}, // Transfer event
			nil, // any from address
			{common.HexToHash("0x000000000000000000000000fe66125343aabda4a330da667431ec1acb7bbda9")}, // to address
		},
	}

	// Create a mock EventTrigger to test the matching
	trigger := &EventTrigger{}

	// Test if the log matches the TO filter
	matches := trigger.logMatchesQuery(log, toFilter)

	t.Logf("Transaction details:")
	t.Logf("  Block: %d", log.BlockNumber)
	t.Logf("  Contract: %s", log.Address.Hex())
	t.Logf("  From: %s", common.HexToAddress(log.Topics[1].Hex()).Hex())
	t.Logf("  To: %s", common.HexToAddress(log.Topics[2].Hex()).Hex())
	t.Logf("  Target: %s", "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9")

	t.Logf("TO Filter details:")
	t.Logf("  Addresses: %v", toFilter.Addresses)
	t.Logf("  Topics[0]: %v", toFilter.Topics[0])
	t.Logf("  Topics[1]: %v", toFilter.Topics[1])
	t.Logf("  Topics[2]: %v", toFilter.Topics[2])

	t.Logf("Match result: %v", matches)

	if !matches {
		t.Errorf("❌ Transaction should match TO filter but doesn't")

		// Debug: check each condition
		// Check addresses
		addrMatch := false
		for _, addr := range toFilter.Addresses {
			if addr == log.Address {
				addrMatch = true
				break
			}
		}
		t.Logf("Address match: %v", addrMatch)

		// Check topics
		for i, topicGroup := range toFilter.Topics {
			if i >= len(log.Topics) {
				t.Logf("Topic[%d]: index out of range", i)
				continue
			}

			if len(topicGroup) > 0 {
				found := false
				for _, expectedTopic := range topicGroup {
					if log.Topics[i] == expectedTopic {
						found = true
						break
					}
				}
				t.Logf("Topic[%d] match: %v (expected: %v, actual: %v)", i, found, topicGroup, log.Topics[i])
			} else {
				t.Logf("Topic[%d]: any value (nil group)", i)
			}
		}
	} else {
		t.Logf("✅ Transaction correctly matches TO filter")
	}
}

func TestSpecificTOTransaction(t *testing.T) {
	// Test the specific TO transaction that wasn't detected: 0xe5bdbc6ed533549b35e4a8259df818188b359e5f727db8e4d712593c235dc793
	// Block: 8559239, Contract: 0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238
	// From: 0x274888BaB7Cf5191b17E54618F5F2822dF76b05F
	// To: 0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9

	// Create the exact log from the transaction
	log := types.Log{
		Address: common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"),
		Topics: []common.Hash{
			common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"), // Transfer event
			common.HexToHash("0x000000000000000000000000274888bab7cf5191b17e54618f5f2822df76b05f"), // from
			common.HexToHash("0x000000000000000000000000fe66125343aabda4a330da667431ec1acb7bbda9"), // to
		},
		BlockNumber: 8559239,
		TxHash:      common.HexToHash("0xe5bdbc6ed533549b35e4a8259df818188b359e5f727db8e4d712593c235dc793"),
		Index:       0,
	}

	// Create the TO subscription filter (same as in your logs)
	toFilter := ethereum.FilterQuery{
		Addresses: []common.Address{
			common.HexToAddress("0x1f9840a85d5aF5bf1D1762F925BDADdC4201F984"),
			common.HexToAddress("0x20c54C5F742F123Abb49a982BFe0af47edb38756"),
			common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"),
		},
		Topics: [][]common.Hash{
			{common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")}, // Transfer event
			nil, // any from address
			{common.HexToHash("0x000000000000000000000000fe66125343aabda4a330da667431ec1acb7bbda9")}, // to address
		},
	}

	// Create a mock EventTrigger to test the matching
	trigger := &EventTrigger{}

	// Test if the log matches the TO filter
	matches := trigger.logMatchesQuery(log, toFilter)

	t.Logf("Transaction details:")
	t.Logf("  Block: %d", log.BlockNumber)
	t.Logf("  Contract: %s", log.Address.Hex())
	t.Logf("  From: %s", common.HexToAddress(log.Topics[1].Hex()).Hex())
	t.Logf("  To: %s", common.HexToAddress(log.Topics[2].Hex()).Hex())
	t.Logf("  Target: %s", "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9")

	t.Logf("TO Filter details:")
	t.Logf("  Addresses: %v", toFilter.Addresses)
	t.Logf("  Topics[0]: %v", toFilter.Topics[0])
	t.Logf("  Topics[1]: %v", toFilter.Topics[1])
	t.Logf("  Topics[2]: %v", toFilter.Topics[2])

	t.Logf("Match result: %v", matches)

	if !matches {
		t.Errorf("❌ TO transaction should match TO filter but doesn't")

		// Debug: check each condition
		// Check addresses
		addrMatch := false
		for _, addr := range toFilter.Addresses {
			if addr == log.Address {
				addrMatch = true
				break
			}
		}
		t.Logf("Address match: %v", addrMatch)

		// Check topics
		for i, topicGroup := range toFilter.Topics {
			if i >= len(log.Topics) {
				t.Logf("Topic[%d]: index out of range", i)
				continue
			}

			if len(topicGroup) > 0 {
				found := false
				for _, expectedTopic := range topicGroup {
					if log.Topics[i] == expectedTopic {
						found = true
						break
					}
				}
				t.Logf("Topic[%d] match: %v (expected: %v, actual: %v)", i, found, topicGroup, log.Topics[i])
			} else {
				t.Logf("Topic[%d]: any value (nil group)", i)
			}
		}
	} else {
		t.Logf("✅ TO transaction correctly matches TO filter")
	}
}

func TestEventTriggerCooldown(t *testing.T) {
	// Create a mock event trigger with proper initialization
	trigger := &EventTrigger{
		registry:   NewTaskRegistry(),
		checks:     sync.Map{},
		legacyMode: false,
		CommonTrigger: &CommonTrigger{
			done:      make(chan bool),
			shutdown:  false,
			rpcOption: &RpcOption{},
			logger:    &MockLogger{},
		},
		triggerCh:                make(chan TriggerMetadata[EventMark], 10),
		subscriptions:            make([]SubscriptionInfo, 0),
		updateSubsCh:             make(chan struct{}, 1),
		eventCounts:              make(map[string]map[uint64]uint32),
		defaultMaxEventsPerQuery: 100,
		defaultMaxTotalEvents:    1000,
		processedEvents:          make(map[string]bool),
		lastTriggerTime:          make(map[string]time.Time),
	}

	t.Run("DefaultCooldownApplied", func(t *testing.T) {
		// Test that default cooldown (300 seconds) is applied when cooldown_seconds is not set
		taskID := "test-task-default"
		cooldownSeconds := DefaultEventTriggerCooldownSeconds // Default value

		// First trigger should be allowed (no previous trigger)
		now := time.Now()
		assert.False(t, trigger.isInCooldown(taskID, cooldownSeconds, now), "First trigger should not be in cooldown")

		// Update timestamp to simulate a trigger
		trigger.updateCooldownTimestamp(taskID, cooldownSeconds, now)

		// Immediately after trigger, should be in cooldown
		assert.True(t, trigger.isInCooldown(taskID, cooldownSeconds, now), "Should be in cooldown immediately after trigger")

		// After 299 seconds, should still be in cooldown
		after299s := now.Add(299 * time.Second)
		assert.True(t, trigger.isInCooldown(taskID, cooldownSeconds, after299s), "Should still be in cooldown after 299 seconds")

		// After 300 seconds, should not be in cooldown
		after300s := now.Add(300 * time.Second)
		assert.False(t, trigger.isInCooldown(taskID, cooldownSeconds, after300s), "Should not be in cooldown after 300 seconds")

		// After 301 seconds, should not be in cooldown
		after301s := now.Add(301 * time.Second)
		assert.False(t, trigger.isInCooldown(taskID, cooldownSeconds, after301s), "Should not be in cooldown after 301 seconds")
	})

	t.Run("CooldownDisabled", func(t *testing.T) {
		// Test that cooldown is disabled when cooldown_seconds is 0
		taskID := "test-task-no-cooldown"
		cooldownSeconds := uint32(0)

		now := time.Now()

		// First trigger should be allowed
		assert.False(t, trigger.isInCooldown(taskID, cooldownSeconds, now), "Should not be in cooldown when disabled")

		// Update timestamp (even though cooldown is disabled)
		trigger.updateCooldownTimestamp(taskID, cooldownSeconds, now)

		// Even after updating timestamp, should not be in cooldown
		assert.False(t, trigger.isInCooldown(taskID, cooldownSeconds, now), "Should not be in cooldown when disabled, even after trigger")

		// Immediately after should still not be in cooldown
		immediatelyAfter := now.Add(1 * time.Second)
		assert.False(t, trigger.isInCooldown(taskID, cooldownSeconds, immediatelyAfter), "Should not be in cooldown when disabled")
	})

	t.Run("CustomCooldown", func(t *testing.T) {
		// Test custom cooldown values
		taskID := "test-task-custom"
		cooldownSeconds := uint32(60) // 1 minute custom cooldown

		now := time.Now()

		// First trigger should be allowed
		assert.False(t, trigger.isInCooldown(taskID, cooldownSeconds, now), "First trigger should not be in cooldown")

		// Update timestamp to simulate a trigger
		trigger.updateCooldownTimestamp(taskID, cooldownSeconds, now)

		// Immediately after trigger, should be in cooldown
		assert.True(t, trigger.isInCooldown(taskID, cooldownSeconds, now), "Should be in cooldown immediately after trigger")

		// After 59 seconds, should still be in cooldown
		after59s := now.Add(59 * time.Second)
		assert.True(t, trigger.isInCooldown(taskID, cooldownSeconds, after59s), "Should still be in cooldown after 59 seconds")

		// After 60 seconds, should not be in cooldown
		after60s := now.Add(60 * time.Second)
		assert.False(t, trigger.isInCooldown(taskID, cooldownSeconds, after60s), "Should not be in cooldown after 60 seconds")
	})

	t.Run("MultipleTasksIndependentCooldown", func(t *testing.T) {
		// Test that different tasks have independent cooldown periods
		taskID1 := "test-task-1"
		taskID2 := "test-task-2"
		cooldownSeconds := uint32(60)

		now := time.Now()

		// Trigger task 1
		trigger.updateCooldownTimestamp(taskID1, cooldownSeconds, now)

		// Task 1 should be in cooldown
		assert.True(t, trigger.isInCooldown(taskID1, cooldownSeconds, now), "Task 1 should be in cooldown")

		// Task 2 should not be in cooldown (never triggered)
		assert.False(t, trigger.isInCooldown(taskID2, cooldownSeconds, now), "Task 2 should not be in cooldown")

		// Trigger task 2 at a later time
		now2 := now.Add(5 * time.Second)
		trigger.updateCooldownTimestamp(taskID2, cooldownSeconds, now2)

		// Both should be in cooldown now
		checkTime := now2
		assert.True(t, trigger.isInCooldown(taskID1, cooldownSeconds, checkTime), "Task 1 should still be in cooldown")
		assert.True(t, trigger.isInCooldown(taskID2, cooldownSeconds, checkTime), "Task 2 should be in cooldown")

		// After cooldown expires for task 1 (triggered at now), task 2 should still be in cooldown (triggered at now2)
		after61s := now.Add(61 * time.Second) // 61 seconds after task 1 was triggered
		assert.False(t, trigger.isInCooldown(taskID1, cooldownSeconds, after61s), "Task 1 should not be in cooldown after expiration")
		// Task 2 was triggered 5 seconds later, so at after61s it's only been 56 seconds since its trigger
		assert.True(t, trigger.isInCooldown(taskID2, cooldownSeconds, after61s), "Task 2 should still be in cooldown (triggered 5s later)")
	})

	t.Run("CooldownPreventsRepeatedTriggers", func(t *testing.T) {
		// Test that cooldown prevents repeated triggers when conditions remain true
		taskID := "test-task-repeated"
		cooldownSeconds := DefaultEventTriggerCooldownSeconds // 5 minutes default

		now := time.Now()

		// First trigger - should be allowed
		assert.False(t, trigger.isInCooldown(taskID, cooldownSeconds, now), "First trigger should be allowed")
		trigger.updateCooldownTimestamp(taskID, cooldownSeconds, now)

		// Second trigger immediately after - should be blocked
		immediatelyAfter := now.Add(1 * time.Second)
		assert.True(t, trigger.isInCooldown(taskID, cooldownSeconds, immediatelyAfter), "Second trigger immediately after should be blocked")

		// Third trigger after 1 minute - should still be blocked
		after1min := now.Add(1 * time.Minute)
		assert.True(t, trigger.isInCooldown(taskID, cooldownSeconds, after1min), "Third trigger after 1 minute should still be blocked")

		// Fourth trigger after 5 minutes - should be allowed
		after5min := now.Add(5 * time.Minute)
		assert.False(t, trigger.isInCooldown(taskID, cooldownSeconds, after5min), "Fourth trigger after 5 minutes should be allowed")
	})

	t.Run("CooldownTimestampUpdate", func(t *testing.T) {
		// Test that cooldown timestamp is updated correctly
		taskID := "test-task-timestamp"
		cooldownSeconds := uint32(60)

		now1 := time.Now()
		trigger.updateCooldownTimestamp(taskID, cooldownSeconds, now1)

		// Check that timestamp was recorded
		trigger.cooldownMutex.RLock()
		lastTrigger, exists := trigger.lastTriggerTime[taskID]
		trigger.cooldownMutex.RUnlock()

		assert.True(t, exists, "Timestamp should be recorded")
		assert.Equal(t, now1.Unix(), lastTrigger.Unix(), "Timestamp should match")

		// Update timestamp again
		now2 := now1.Add(10 * time.Second)
		trigger.updateCooldownTimestamp(taskID, cooldownSeconds, now2)

		// Check that timestamp was updated
		trigger.cooldownMutex.RLock()
		lastTrigger2, exists2 := trigger.lastTriggerTime[taskID]
		trigger.cooldownMutex.RUnlock()

		assert.True(t, exists2, "Timestamp should still exist")
		assert.Equal(t, now2.Unix(), lastTrigger2.Unix(), "Timestamp should be updated")
	})
}
