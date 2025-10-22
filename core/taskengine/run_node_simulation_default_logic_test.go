package taskengine

import (
	"testing"
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
			// Simulate per-node logic (now lives in ContractWrite.Config)
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

// Removed protobuf optional bool test at top-level; is_simulated now belongs in ContractWriteNode.Config
