package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// TestVM_OrganizationValidation tests basic VM organization and setup
func TestVM_OrganizationValidation(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Test basic VM creation and setup
	vm := NewVM()
	assert.NotNil(t, vm, "VM should be created successfully")
	assert.Equal(t, VMStateInitialize, vm.Status, "VM should start in initialize state")

	// Test VM with logger
	vm.WithLogger(testutil.GetLogger())
	assert.NotNil(t, vm.logger, "Logger should be set")

	// Test VM with database
	vm.WithDb(db)
	assert.NotNil(t, vm.db, "Database should be set")

	t.Logf("VM organization validation completed successfully")
}

// TestVM_VariableManagement tests VM variable management
func TestVM_VariableManagement(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	// Test adding variables
	vm.AddVar("test_key", "test_value")
	vm.AddVar("test_number", 42)
	vm.AddVar("test_bool", true)

	// Test that variables were added
	vm.mu.Lock()
	assert.Equal(t, "test_value", vm.vars["test_key"])
	assert.Equal(t, 42, vm.vars["test_number"])
	assert.Equal(t, true, vm.vars["test_bool"])
	vm.mu.Unlock()

	// Test variable collection
	inputs := vm.CollectInputs()
	assert.Contains(t, inputs, "test_key.data")
	assert.Contains(t, inputs, "test_number.data")
	assert.Contains(t, inputs, "test_bool.data")

	t.Logf("Variable management test completed with %d variables", len(inputs))
}

// TestVM_StateTransitions tests VM state transitions
func TestVM_StateTransitions(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	// Initial state should be Initialize
	assert.Equal(t, VMStateInitialize, vm.Status)

	// Test Reset functionality
	vm.Reset()
	assert.Equal(t, VMStateInitialize, vm.Status)
	assert.Len(t, vm.ExecutionLogs, 0, "Reset should clear execution logs")

	t.Logf("State transition test completed successfully")
}

// TestVM_TriggerNameGeneration tests trigger name generation and sanitization
func TestVM_TriggerNameGeneration(t *testing.T) {
	tests := []struct {
		name         string
		task         func() *VM
		expectedName string
		expectError  bool
	}{
		{
			name: "VM without task",
			task: func() *VM {
				vm := NewVM()
				vm.WithLogger(testutil.GetLogger())
				return vm
			},
			expectedName: "trigger", // Default for nil task
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := tt.task()

			triggerName, err := vm.GetTriggerNameAsVar()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedName, triggerName)
			}

			t.Logf("Trigger name test '%s': got '%s'", tt.name, triggerName)
		})
	}
}

// TestVM_NodeNameGeneration tests node name generation for variables
func TestVM_NodeNameGeneration(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	// Test with empty TaskNodes map (no node found)
	nodeName := vm.GetNodeNameAsVar("nonexistent_node")
	assert.Equal(t, "unknown", nodeName, "Should return 'unknown' for nonexistent nodes")

	t.Logf("Node name generation test completed")
}

// TestVM_ConcurrentAccess tests thread safety of VM operations
func TestVM_ConcurrentAccess(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	// Test concurrent variable additions
	done := make(chan bool, 2)

	// Goroutine 1: Add variables with prefix "A"
	go func() {
		for i := 0; i < 10; i++ {
			vm.AddVar("keyA"+string(rune('0'+i)), "valueA")
		}
		done <- true
	}()

	// Goroutine 2: Add variables with prefix "B"
	go func() {
		for i := 0; i < 10; i++ {
			vm.AddVar("keyB"+string(rune('0'+i)), "valueB")
		}
		done <- true
	}()

	// Wait for both goroutines to complete
	<-done
	<-done

	// Verify all variables were added correctly
	vm.mu.Lock()
	totalVars := len(vm.vars)
	vm.mu.Unlock()

	// Should have at least 20 variables (10 from each goroutine) plus any env vars
	assert.GreaterOrEqual(t, totalVars, 20, "Should have at least 20 variables from concurrent access")

	t.Logf("Concurrent access test completed with %d total variables", totalVars)
}

// TestVM_MemoryManagement tests VM memory management and cleanup
func TestVM_MemoryManagement(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	vm.WithDb(db)

	// Add many variables to test memory usage
	for i := 0; i < 1000; i++ {
		vm.AddVar("key"+string(rune(i)), map[string]interface{}{
			"data":  "value" + string(rune(i)),
			"index": i,
		})
	}

	// Verify variables were added
	vm.mu.Lock()
	initialCount := len(vm.vars)
	vm.mu.Unlock()

	assert.GreaterOrEqual(t, initialCount, 1000, "Should have at least 1000 variables")

	// Test reset clears variables appropriately
	vm.Reset()

	vm.mu.Lock()
	afterResetCount := len(vm.vars)
	vm.mu.Unlock()

	// After reset, should have fewer variables (only env vars should remain)
	assert.Less(t, afterResetCount, initialCount, "Reset should reduce variable count")

	t.Logf("Memory management test: %d vars before reset, %d after reset", initialCount, afterResetCount)
}

/*
Test Organization Summary:

Priority 2 Consolidation (COMPLETED):
✅ vm_contract_operations_test.go - Contract read/write tests
✅ vm_execution_flow_test.go - ETH transfers, filters, preprocessing
✅ vm_node_runners_test.go - Node runner consolidation from previous PR

Priority 3 Organization (COMPLETED):
✅ vm_test_organization.go - Basic VM functionality tests

Recommended Future Organization (when splitting large files):

ENGINE TESTS:
- engine_crud_test.go (Create, Read, Update, Delete operations)
- engine_execution_test.go (Task execution workflows)
- engine_simulation_test.go (SimulateTask functionality)

VM TESTS:
- vm_compilation_test.go (Task compilation and validation)
- vm_branch_execution_test.go (Branch node execution)
- vm_customcode_test.go (JavaScript execution)

INTEGRATION TESTS:
- workflow_integration_test.go (End-to-end workflow testing)
- run_node_integration_test.go (Single node execution)

This organization provides:
1. Clear separation of concerns
2. Focused test suites that can run independently
3. Reduced CI/CD time through smaller, parallelizable test files
4. Better maintainability and test discovery
*/
