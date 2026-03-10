package trigger

import (
	"sync"
	"testing"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPollingQuery(t *testing.T) {
	testCases := []struct {
		name     string
		queries  []*avsproto.EventTrigger_Query
		expected bool
	}{
		{
			name:     "nil queries",
			queries:  nil,
			expected: false,
		},
		{
			name:     "empty queries",
			queries:  []*avsproto.EventTrigger_Query{},
			expected: false,
		},
		{
			name: "methodCalls without topics - polling",
			queries: []*avsproto.EventTrigger_Query{
				{
					Addresses: []string{"0x1234567890abcdef1234567890abcdef12345678"},
					MethodCalls: []*avsproto.EventTrigger_MethodCall{
						{MethodName: "latestRoundData"},
					},
				},
			},
			expected: true,
		},
		{
			name: "topics without methodCalls - event-based",
			queries: []*avsproto.EventTrigger_Query{
				{
					Addresses: []string{"0x1234567890abcdef1234567890abcdef12345678"},
					Topics:    []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
				},
			},
			expected: false,
		},
		{
			name: "both topics and methodCalls - event-based",
			queries: []*avsproto.EventTrigger_Query{
				{
					Addresses: []string{"0x1234567890abcdef1234567890abcdef12345678"},
					Topics:    []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
					MethodCalls: []*avsproto.EventTrigger_MethodCall{
						{MethodName: "decimals"},
					},
				},
			},
			expected: false,
		},
		{
			name: "methodCalls without topics but empty methodCalls - not polling",
			queries: []*avsproto.EventTrigger_Query{
				{
					Addresses:   []string{"0x1234567890abcdef1234567890abcdef12345678"},
					MethodCalls: []*avsproto.EventTrigger_MethodCall{},
				},
			},
			expected: false,
		},
		{
			name: "multiple queries all polling",
			queries: []*avsproto.EventTrigger_Query{
				{
					Addresses: []string{"0x1234567890abcdef1234567890abcdef12345678"},
					MethodCalls: []*avsproto.EventTrigger_MethodCall{
						{MethodName: "getUserAccountData", MethodParams: []string{"0x0000000000000000000000000000000000000001"}},
					},
				},
				{
					Addresses: []string{"0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"},
					MethodCalls: []*avsproto.EventTrigger_MethodCall{
						{MethodName: "latestRoundData"},
					},
				},
			},
			expected: true,
		},
		{
			name: "mixed queries - one has topics",
			queries: []*avsproto.EventTrigger_Query{
				{
					Addresses: []string{"0x1234567890abcdef1234567890abcdef12345678"},
					MethodCalls: []*avsproto.EventTrigger_MethodCall{
						{MethodName: "getUserAccountData"},
					},
				},
				{
					Addresses: []string{"0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"},
					Topics:    []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
				},
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := isPollingQuery(tc.queries)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestBuildFilterQueriesSkipsPollingTasks(t *testing.T) {
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

	// Add a polling task (methodCalls without topics)
	pollingTaskMeta := &avsproto.SyncMessagesResp_TaskMetadata{TaskId: "polling-task-1"}
	pollingEventData := &EventTaskData{
		Queries: []*avsproto.EventTrigger_Query{
			{
				Addresses: []string{"0x6Ae43d3271ff6888e7Fc43Fd7321a503ff738951"},
				MethodCalls: []*avsproto.EventTrigger_MethodCall{
					{MethodName: "getUserAccountData", MethodParams: []string{"0x0000000000000000000000000000000000000001"}},
				},
			},
		},
		ParsedABIs:    make(map[int]*abi.ABI),
		IsPollingTask: true,
	}
	trigger.registry.AddTask("polling-task-1", pollingTaskMeta, pollingEventData, nil, nil)

	// Add an event-based task (has topics)
	eventTaskMeta := &avsproto.SyncMessagesResp_TaskMetadata{TaskId: "event-task-1"}
	eventEventData := &EventTaskData{
		Queries: []*avsproto.EventTrigger_Query{
			{
				Addresses: []string{"0x694AA1769357215DE4FAC081bf1f309aDC325306"},
				Topics:    []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
			},
		},
		ParsedABIs:    make(map[int]*abi.ABI),
		IsPollingTask: false,
	}
	trigger.registry.AddTask("event-task-1", eventTaskMeta, eventEventData, nil, nil)

	// Build filter queries
	queries := trigger.buildFilterQueries()

	// Should only contain the event-based task, not the polling task
	assert.Len(t, queries, 1, "Should have exactly 1 query (event-based task only)")

	if len(queries) > 0 {
		assert.Equal(t, "event-task-1", queries[0].TaskID,
			"The query should be for the event-based task")
	}
}

func TestEvaluatePollingConditions(t *testing.T) {
	trigger := &EventTrigger{
		CommonTrigger: &CommonTrigger{
			logger: &MockLogger{},
		},
	}

	testCases := []struct {
		name       string
		data       map[string]interface{}
		conditions []*avsproto.EventCondition
		expected   bool
	}{
		{
			name: "uint256 gt - match",
			data: map[string]interface{}{
				"getUserAccountData": map[string]interface{}{
					"healthFactor": "1400000000000000000",
				},
			},
			conditions: []*avsproto.EventCondition{
				{FieldName: "getUserAccountData.healthFactor", Operator: "gt", Value: "1000000000000000000", FieldType: "uint256"},
			},
			expected: true,
		},
		{
			name: "uint256 lt - match",
			data: map[string]interface{}{
				"getUserAccountData": map[string]interface{}{
					"healthFactor": "1400000000000000000",
				},
			},
			conditions: []*avsproto.EventCondition{
				{FieldName: "getUserAccountData.healthFactor", Operator: "lt", Value: "1500000000000000000", FieldType: "uint256"},
			},
			expected: true,
		},
		{
			name: "uint256 lt - no match",
			data: map[string]interface{}{
				"getUserAccountData": map[string]interface{}{
					"healthFactor": "1400000000000000000",
				},
			},
			conditions: []*avsproto.EventCondition{
				{FieldName: "getUserAccountData.healthFactor", Operator: "lt", Value: "1000000000000000000", FieldType: "uint256"},
			},
			expected: false,
		},
		{
			name: "multiple conditions AND - all match",
			data: map[string]interface{}{
				"getUserAccountData": map[string]interface{}{
					"healthFactor":  "1400000000000000000",
					"totalDebtBase": "100000000000",
				},
			},
			conditions: []*avsproto.EventCondition{
				{FieldName: "getUserAccountData.healthFactor", Operator: "lt", Value: "1500000000000000000", FieldType: "uint256"},
				{FieldName: "getUserAccountData.totalDebtBase", Operator: "gt", Value: "0", FieldType: "uint256"},
			},
			expected: true,
		},
		{
			name: "multiple conditions AND - one fails",
			data: map[string]interface{}{
				"getUserAccountData": map[string]interface{}{
					"healthFactor":  "1400000000000000000",
					"totalDebtBase": "0",
				},
			},
			conditions: []*avsproto.EventCondition{
				{FieldName: "getUserAccountData.healthFactor", Operator: "lt", Value: "1500000000000000000", FieldType: "uint256"},
				{FieldName: "getUserAccountData.totalDebtBase", Operator: "gt", Value: "0", FieldType: "uint256"},
			},
			expected: false,
		},
		{
			name: "nonexistent field - fails",
			data: map[string]interface{}{
				"getUserAccountData": map[string]interface{}{
					"healthFactor": "1400000000000000000",
				},
			},
			conditions: []*avsproto.EventCondition{
				{FieldName: "getUserAccountData.nonexistent", Operator: "gt", Value: "0", FieldType: "uint256"},
			},
			expected: false,
		},
		{
			name: "int256 comparison",
			data: map[string]interface{}{
				"latestRoundData": map[string]interface{}{
					"answer": "380000000000",
				},
			},
			conditions: []*avsproto.EventCondition{
				{FieldName: "latestRoundData.answer", Operator: "gt", Value: "100000000000", FieldType: "int256"},
			},
			expected: true,
		},
		{
			name: "eq operator",
			data: map[string]interface{}{
				"getUserAccountData": map[string]interface{}{
					"healthFactor": "1400000000000000000",
				},
			},
			conditions: []*avsproto.EventCondition{
				{FieldName: "getUserAccountData.healthFactor", Operator: "eq", Value: "1400000000000000000", FieldType: "uint256"},
			},
			expected: true,
		},
		{
			name: "single-output method (direct field)",
			data: map[string]interface{}{
				"decimals": "8",
			},
			conditions: []*avsproto.EventCondition{
				{FieldName: "decimals", Operator: "eq", Value: "8", FieldType: "uint256"},
			},
			expected: true,
		},
		{
			name: "no conditions - always true",
			data: map[string]interface{}{
				"getUserAccountData": map[string]interface{}{
					"healthFactor": "1400000000000000000",
				},
			},
			conditions: []*avsproto.EventCondition{},
			expected:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := trigger.evaluatePollingConditions(tc.data, tc.conditions)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestFormatOutputValue(t *testing.T) {
	// Test bool formatting
	assert.Equal(t, "true", formatOutputValue(true))
	assert.Equal(t, "false", formatOutputValue(false))

	// Test default formatting
	assert.Equal(t, "42", formatOutputValue(42))
}

func TestPollingTaskRegistryIntegration(t *testing.T) {
	// Test that IsPollingTask flag is properly set and used
	registry := NewTaskRegistry()

	// Add a polling task
	pollingMeta := &avsproto.SyncMessagesResp_TaskMetadata{TaskId: "poll-1"}
	pollingData := &EventTaskData{
		Queries: []*avsproto.EventTrigger_Query{
			{
				Addresses: []string{"0x1234567890abcdef1234567890abcdef12345678"},
				MethodCalls: []*avsproto.EventTrigger_MethodCall{
					{MethodName: "getUserAccountData"},
				},
			},
		},
		ParsedABIs:    make(map[int]*abi.ABI),
		IsPollingTask: true,
	}
	registry.AddTask("poll-1", pollingMeta, pollingData, nil, nil)

	// Add an event task
	eventMeta := &avsproto.SyncMessagesResp_TaskMetadata{TaskId: "event-1"}
	eventData := &EventTaskData{
		Queries: []*avsproto.EventTrigger_Query{
			{
				Addresses: []string{"0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"},
				Topics:    []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
			},
		},
		ParsedABIs:    make(map[int]*abi.ABI),
		IsPollingTask: false,
	}
	registry.AddTask("event-1", eventMeta, eventData, nil, nil)

	// Verify task counts
	assert.Equal(t, 2, registry.GetEventTaskCount())

	// Count polling vs event tasks
	pollingCount := 0
	eventCount := 0
	registry.RangeEventTasks(func(taskID string, entry *TaskEntry) bool {
		if entry.EventData.IsPollingTask {
			pollingCount++
		} else {
			eventCount++
		}
		return true
	})

	assert.Equal(t, 1, pollingCount, "Should have 1 polling task")
	assert.Equal(t, 1, eventCount, "Should have 1 event task")

	// Verify individual tasks
	entry, exists := registry.GetTask("poll-1")
	require.True(t, exists)
	assert.True(t, entry.EventData.IsPollingTask)

	entry, exists = registry.GetTask("event-1")
	require.True(t, exists)
	assert.False(t, entry.EventData.IsPollingTask)
}
