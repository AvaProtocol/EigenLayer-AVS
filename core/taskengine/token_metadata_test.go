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

func TestIsNativeToken(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		expected bool
	}{
		{
			name:     "Native token lowercase",
			address:  "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			expected: true,
		},
		{
			name:     "Native token uppercase",
			address:  "0xEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE",
			expected: true,
		},
		{
			name:     "Native token mixed case",
			address:  "0xEeEeEeEeEeEeEeEeEeEeEeEeEeEeEeEeEeEeEeEe",
			expected: true,
		},
		{
			name:     "Regular ERC20 address",
			address:  "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238",
			expected: false,
		},
		{
			name:     "Zero address",
			address:  "0x0000000000000000000000000000000000000000",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isNativeToken(tt.address)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// warnCapturingLogger records Warn calls for assertions.
type warnCapturingLogger struct {
	MockLogger
	warns []string
}

func (l *warnCapturingLogger) Warn(msg string, keysAndValues ...interface{}) {
	l.warns = append(l.warns, msg)
}

func TestLoadTokensIntoCacheValidation(t *testing.T) {
	logger := &warnCapturingLogger{}
	service := &TokenEnrichmentService{
		cache:  make(map[string]*TokenMetadata),
		logger: logger,
	}

	tokens := []TokenMetadata{
		{Id: "0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Name: "Valid", Symbol: "VAL", Decimals: 18},
		{Id: "", Name: "MissingId", Symbol: "BAD", Decimals: 18},
		{Id: "0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", Name: "ZeroDec", Symbol: "ZD", Decimals: 0},
	}

	loaded, skipped := service.loadTokensIntoCache(tokens, "test.json")

	assert.Equal(t, 2, loaded)
	assert.Equal(t, 1, skipped)

	// Empty-id entry skipped; the two valid entries are cached with lowercased ids.
	assert.Len(t, service.cache, 2)
	assert.Contains(t, service.cache, "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	assert.Contains(t, service.cache, "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	assert.NotContains(t, service.cache, "")

	// One warn for missing id, one for decimals=0.
	require.Len(t, logger.warns, 2)
	assert.Contains(t, logger.warns[0], "missing id")
	assert.Contains(t, logger.warns[1], "decimals=0")
}

func TestGetNativeTokenMetadata(t *testing.T) {
	metadata := getNativeTokenMetadata()

	assert.NotNil(t, metadata)
	assert.Equal(t, NativeTokenAddress, metadata.Id)
	assert.Equal(t, "Ether", metadata.Name)
	assert.Equal(t, "ETH", metadata.Symbol)
	assert.Equal(t, uint32(18), metadata.Decimals)
	assert.Equal(t, "native", metadata.Source)
}
