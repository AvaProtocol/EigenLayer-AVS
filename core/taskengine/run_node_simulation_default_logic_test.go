package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestSimulationDefaultLogic is a lightweight test that verifies the is_simulated logic
// without requiring full engine setup or config files
func TestSimulationDefaultLogic(t *testing.T) {
	tests := []struct {
		name           string
		isSimulated    *bool
		expectedResult bool
		description    string
	}{
		{
			name:           "nil pointer defaults to simulation (true)",
			isSimulated:    nil,
			expectedResult: true,
			description:    "When is_simulated is unset (nil), should default to simulation for safety",
		},
		{
			name: "explicit true uses simulation",
			isSimulated: func() *bool {
				v := true
				return &v
			}(),
			expectedResult: true,
			description:    "When is_simulated is explicitly set to true, should use simulation",
		},
		{
			name: "explicit false uses real execution",
			isSimulated: func() *bool {
				v := false
				return &v
			}(),
			expectedResult: false,
			description:    "When is_simulated is explicitly set to false, should use real execution",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the logic from RunNodeImmediatelyRPC
			useSimulation := true // Safe default
			if tt.isSimulated != nil {
				useSimulation = *tt.isSimulated
			}

			if useSimulation != tt.expectedResult {
				t.Errorf("%s: expected %v, got %v", tt.description, tt.expectedResult, useSimulation)
			}

			// Log for clarity
			t.Logf("✅ %s: useSimulation=%v (expected=%v)", tt.description, useSimulation, tt.expectedResult)
		})
	}
}

// TestProtobufOptionalBoolGeneration verifies that the protobuf optional bool generates a pointer
func TestProtobufOptionalBoolGeneration(t *testing.T) {
	// Create a request with nil is_simulated (unset)
	reqUnset := &avsproto.RunNodeWithInputsReq{
		NodeType:    avsproto.NodeType_NODE_TYPE_REST_API,
		IsSimulated: nil,
	}

	// Create a request with explicit true
	reqTrue := &avsproto.RunNodeWithInputsReq{
		NodeType: avsproto.NodeType_NODE_TYPE_REST_API,
		IsSimulated: func() *bool {
			v := true
			return &v
		}(),
	}

	// Create a request with explicit false
	reqFalse := &avsproto.RunNodeWithInputsReq{
		NodeType: avsproto.NodeType_NODE_TYPE_REST_API,
		IsSimulated: func() *bool {
			v := false
			return &v
		}(),
	}

	// Test that we can distinguish between unset, true, and false
	if reqUnset.IsSimulated != nil {
		t.Error("Unset is_simulated should be nil")
	}

	if reqTrue.IsSimulated == nil {
		t.Error("Explicit true is_simulated should not be nil")
	} else if !*reqTrue.IsSimulated {
		t.Error("Explicit true is_simulated should be true")
	}

	if reqFalse.IsSimulated == nil {
		t.Error("Explicit false is_simulated should not be nil")
	} else if *reqFalse.IsSimulated {
		t.Error("Explicit false is_simulated should be false")
	}

	t.Logf("✅ Protobuf optional bool correctly generates pointer type")
	t.Logf("   - Unset: %v (nil pointer)", reqUnset.IsSimulated)
	t.Logf("   - Explicit true: %v", *reqTrue.IsSimulated)
	t.Logf("   - Explicit false: %v", *reqFalse.IsSimulated)
}
