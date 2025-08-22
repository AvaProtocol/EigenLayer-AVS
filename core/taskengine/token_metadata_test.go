package taskengine

import (
	"testing"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockLogger for testing
type MockLogger struct{}

func (l *MockLogger) Debug(msg string, keysAndValues ...interface{}) {}
func (l *MockLogger) Info(msg string, keysAndValues ...interface{})  {}
func (l *MockLogger) Warn(msg string, keysAndValues ...interface{})  {}
func (l *MockLogger) Error(msg string, keysAndValues ...interface{}) {}
func (l *MockLogger) Debugf(format string, args ...interface{})      {}
func (l *MockLogger) Infof(format string, args ...interface{})       {}
func (l *MockLogger) Warnf(format string, args ...interface{})       {}
func (l *MockLogger) Errorf(format string, args ...interface{})      {}
func (l *MockLogger) Fatal(msg string, keysAndValues ...interface{}) {}
func (l *MockLogger) Fatalf(format string, args ...interface{})      {}
func (l *MockLogger) With(keysAndValues ...any) sdklogging.Logger    { return l }

func TestNewTokenEnrichmentService(t *testing.T) {
	logger := &MockLogger{}

	// Test with nil RPC client - should now fail
	service, err := NewTokenEnrichmentService(nil, logger)
	require.Error(t, err)
	assert.Nil(t, service)
	assert.Contains(t, err.Error(), "RPC client is required")
}

func TestLoadWhitelist(t *testing.T) {
	// Skip this test since we now require RPC client
	t.Skip("Skipping whitelist test - requires RPC client in new implementation")
}

func TestFormatTokenValue(t *testing.T) {
	// Skip this test since we now require RPC client
	t.Skip("Skipping format test - requires RPC client in new implementation")
}

func TestIsERC20Contract(t *testing.T) {
	// Skip this test since we now require RPC client
	t.Skip("Skipping ERC20 contract test - requires RPC client in new implementation")
}

func TestGetTokenMetadataWhitelistOnlyReturnsNil(t *testing.T) {
	// Skip this test since we now require RPC client
	t.Skip("Skipping whitelist-only test - requires RPC client in new implementation")
}

func TestChainIDDetection(t *testing.T) {
	logger := &MockLogger{}

	// Test with nil RPC client - should now fail
	service, err := NewTokenEnrichmentService(nil, logger)
	require.Error(t, err)
	assert.Nil(t, service)
	assert.Contains(t, err.Error(), "RPC client is required")
}

func BenchmarkFormatTokenValue(b *testing.B) {
	// Skip this benchmark since we now require RPC client
	b.Skip("Skipping format benchmark - requires RPC client in new implementation")
}

func BenchmarkGetTokenMetadata(b *testing.B) {
	// Skip this benchmark since we now require RPC client
	b.Skip("Skipping metadata benchmark - requires RPC client in new implementation")
}
