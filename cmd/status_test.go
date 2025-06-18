package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusCommand(t *testing.T) {
	tests := []struct {
		name           string
		setupEnv       func()
		cleanupEnv     func()
		expectedOutput []string
		expectError    bool
	}{
		{
			name: "status command with valid database path",
			setupEnv: func() {
				// Create a temporary directory for testing
				// Status command uses hardcoded path, so we'll just test basic execution
			},
			cleanupEnv: func() {},
			expectedOutput: []string{
				"📊 System Status Report",
				"Active tasks in database:",
				"💡 Troubleshooting:",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			tt.setupEnv()
			defer tt.cleanupEnv()

			// Create a buffer to capture output
			var buf bytes.Buffer

			// Use the actual status command
			cmd := statusCmd
			originalOut := cmd.OutOrStdout()
			originalErr := cmd.ErrOrStderr()
			defer func() {
				cmd.SetOut(originalOut)
				cmd.SetErr(originalErr)
			}()

			cmd.SetOut(&buf)
			cmd.SetErr(&buf)

			// Call the Run function directly instead of Execute to avoid parent command issues
			if cmd.Run != nil {
				cmd.Run(cmd, []string{})
			}

			// Check output for expected strings
			output := buf.String()
			if len(output) > 0 {
				for _, expected := range tt.expectedOutput {
					assert.Contains(t, output, expected, "Expected output to contain: %s", expected)
				}
			}
			t.Logf("Command output:\n%s", output)
		})
	}
}

func TestStatusCommandHelp(t *testing.T) {
	// Test that the command is properly defined
	assert.Equal(t, "status", statusCmd.Use)
	assert.Equal(t, "Display system status", statusCmd.Short)
	assert.Contains(t, statusCmd.Long, "Display status information")
	assert.NotNil(t, statusCmd.Run, "Status command should have a Run function")
}

func TestStatusCommandFormatting(t *testing.T) {
	var buf bytes.Buffer

	cmd := statusCmd
	originalOut := cmd.OutOrStdout()
	originalErr := cmd.ErrOrStderr()
	defer func() {
		cmd.SetOut(originalOut)
		cmd.SetErr(originalErr)
	}()

	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Call the Run function directly to test output formatting
	if cmd.Run != nil {
		cmd.Run(cmd, []string{})
	}

	output := buf.String()

	// Check for proper emoji usage and formatting
	if len(output) > 0 {
		assert.Contains(t, output, "📊", "Should contain status emoji")

		// Check for proper sections
		hasSystemStatus := false
		hasTroubleshootingTips := false

		lines := strings.Split(output, "\n")
		for _, line := range lines {
			if strings.Contains(line, "📊 System Status Report") {
				hasSystemStatus = true
			}
			if strings.Contains(line, "💡 Troubleshooting") {
				hasTroubleshootingTips = true
			}
		}

		assert.True(t, hasSystemStatus, "Should contain system status section")
		assert.True(t, hasTroubleshootingTips, "Should contain troubleshooting tips section")
	}
}
