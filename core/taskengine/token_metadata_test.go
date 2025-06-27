package taskengine

import (
	"os"
	"path/filepath"
	"testing"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockLogger for testing
type MockLogger struct{}

func (l *MockLogger) Info(msg string, keysAndValues ...interface{})  {}
func (l *MockLogger) Debug(msg string, keysAndValues ...interface{}) {}
func (l *MockLogger) Warn(msg string, keysAndValues ...interface{})  {}
func (l *MockLogger) Error(msg string, keysAndValues ...interface{}) {}
func (l *MockLogger) Infof(format string, args ...interface{})       {}
func (l *MockLogger) Debugf(format string, args ...interface{})      {}
func (l *MockLogger) Warnf(format string, args ...interface{})       {}
func (l *MockLogger) Errorf(format string, args ...interface{})      {}
func (l *MockLogger) Fatal(msg string, keysAndValues ...interface{}) {}
func (l *MockLogger) Fatalf(format string, args ...interface{})      {}
func (l *MockLogger) With(keysAndValues ...any) sdklogging.Logger    { return l }

func TestNewTokenEnrichmentService(t *testing.T) {
	logger := &MockLogger{}

	// Test with nil RPC client
	service, err := NewTokenEnrichmentService(nil, logger)
	require.NoError(t, err)
	assert.NotNil(t, service)
	assert.Equal(t, ChainIDEthereum, service.chainID) // Should default to Ethereum
	assert.NotNil(t, service.cache)
}

func TestLoadWhitelist(t *testing.T) {
	logger := &MockLogger{}

	// Create a temporary directory for test whitelist files
	tempDir, err := os.MkdirTemp("", "token_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Change to temp directory
	originalWd, _ := os.Getwd()
	err = os.Chdir(tempDir)
	require.NoError(t, err)
	defer os.Chdir(originalWd)

	// Create token_whitelist directory
	err = os.Mkdir("token_whitelist", 0755)
	require.NoError(t, err)

	// Create test ethereum.json
	testTokens := `[
		{
			"name": "Test Token",
			"symbol": "TEST",
			"decimals": 18,
			"address": "0x1234567890123456789012345678901234567890"
		},
		{
			"name": "Another Token",
			"symbol": "ANOTHER",
			"decimals": 6,
			"address": "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"
		}
	]`

	err = os.WriteFile(filepath.Join("token_whitelist", "ethereum.json"), []byte(testTokens), 0644)
	require.NoError(t, err)

	// Test service creation and whitelist loading
	service, err := NewTokenEnrichmentService(nil, logger)
	require.NoError(t, err)

	// Check that tokens were loaded
	assert.Equal(t, 2, service.GetCacheSize())

	// Check specific token metadata
	metadata, err := service.GetTokenMetadata("0x1234567890123456789012345678901234567890")
	require.NoError(t, err)
	assert.Equal(t, "Test Token", metadata.Name)
	assert.Equal(t, "TEST", metadata.Symbol)
	assert.Equal(t, uint32(18), metadata.Decimals)

	// Test case insensitive address lookup
	metadata, err = service.GetTokenMetadata("0X1234567890123456789012345678901234567890")
	require.NoError(t, err)
	assert.Equal(t, "Test Token", metadata.Name)
}

func TestFormatTokenValue(t *testing.T) {
	logger := &MockLogger{}
	service, _ := NewTokenEnrichmentService(nil, logger)

	tests := []struct {
		name     string
		value    string
		decimals uint32
		expected string
	}{
		{
			name:     "Simple value with 18 decimals",
			value:    "0x01", // 1 wei
			decimals: 18,
			expected: "0.000000000000000001",
		},
		{
			name:     "1 ETH with 18 decimals",
			value:    "0xde0b6b3a7640000", // 1 ETH in wei
			decimals: 18,
			expected: "1",
		},
		{
			name:     "USDC with 6 decimals",
			value:    "0xf4240", // 1 USDC (1,000,000 micro USDC)
			decimals: 6,
			expected: "1",
		},
		{
			name:     "Value with no decimals",
			value:    "0x64", // 100
			decimals: 0,
			expected: "100",
		},
		{
			name:     "Fractional value with trimmed zeros",
			value:    "0x2386f26fc10000", // 0.01 ETH
			decimals: 18,
			expected: "0.01",
		},
		{
			name:     "Value without 0x prefix",
			value:    "1000000000000000000", // 1 ETH in wei as decimal string
			decimals: 18,
			expected: "1",
		},
		{
			name:     "Decimal string input - USDC",
			value:    "1000000", // 1 USDC as decimal string
			decimals: 6,
			expected: "1",
		},
		{
			name:     "Decimal string input - problematic case from user",
			value:    "100500000000000000000", // Actual value from user's data
			decimals: 6,
			expected: "100500000000000", // Should be 100.5 trillion tokens
		},
		{
			name:     "Large decimal value",
			value:    "123456789012345678901234567890", // Very large decimal
			decimals: 18,
			expected: "123456789012.34567890123456789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := service.FormatTokenValue(tt.value, tt.decimals)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsERC20Contract(t *testing.T) {
	logger := &MockLogger{}

	// Create a temporary directory for test whitelist files
	tempDir, err := os.MkdirTemp("", "token_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Change to temp directory
	originalWd, _ := os.Getwd()
	err = os.Chdir(tempDir)
	require.NoError(t, err)
	defer os.Chdir(originalWd)

	// Create token_whitelist directory
	err = os.Mkdir("token_whitelist", 0755)
	require.NoError(t, err)

	// Create test ethereum.json
	testTokens := `[
		{
			"name": "Test Token",
			"symbol": "TEST",
			"decimals": 18,
			"address": "0x1234567890123456789012345678901234567890"
		}
	]`

	err = os.WriteFile(filepath.Join("token_whitelist", "ethereum.json"), []byte(testTokens), 0644)
	require.NoError(t, err)

	service, err := NewTokenEnrichmentService(nil, logger)
	require.NoError(t, err)

	// Test with token in whitelist
	isERC20 := service.IsERC20Contract("0x1234567890123456789012345678901234567890")
	assert.True(t, isERC20)

	// Test with token not in whitelist (should default to true when no RPC client)
	isERC20 = service.IsERC20Contract("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")
	assert.True(t, isERC20)
}

func TestGetTokenMetadataWhitelistOnlyReturnsNil(t *testing.T) {
	logger := &MockLogger{}

	// Create service without RPC client (whitelist-only mode)
	service, err := NewTokenEnrichmentService(nil, logger)
	require.NoError(t, err)

	// Clear any cache that might have been loaded from actual whitelist files
	// This simulates having an empty whitelist without requiring file operations
	service.cacheMux.Lock()
	service.cache = make(map[string]*TokenMetadata)
	service.cacheMux.Unlock()

	// Test with token not in empty cache and no RPC client
	metadata, err := service.GetTokenMetadata("0x1234567890123456789012345678901234567890")
	assert.NoError(t, err)
	assert.Nil(t, metadata, "Token not in whitelist should return nil metadata when no RPC client")
}

func TestChainIDDetection(t *testing.T) {
	logger := &MockLogger{}

	service, err := NewTokenEnrichmentService(nil, logger)
	require.NoError(t, err)

	// Should default to Ethereum mainnet when no RPC client
	assert.Equal(t, ChainIDEthereum, service.GetChainID())
}

func BenchmarkFormatTokenValue(b *testing.B) {
	logger := &MockLogger{}
	service, _ := NewTokenEnrichmentService(nil, logger)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = service.FormatTokenValue("0xde0b6b3a7640000", 18) // 1 ETH
	}
}

func BenchmarkGetTokenMetadataFromCache(b *testing.B) {
	logger := &MockLogger{}

	// Create a temporary directory for test whitelist files
	tempDir, _ := os.MkdirTemp("", "token_bench")
	defer os.RemoveAll(tempDir)

	// Change to temp directory
	originalWd, _ := os.Getwd()
	os.Chdir(tempDir)
	defer os.Chdir(originalWd)

	// Create token_whitelist directory
	os.Mkdir("token_whitelist", 0755)

	// Create test ethereum.json
	testTokens := `[
		{
			"name": "Test Token",
			"symbol": "TEST",
			"decimals": 18,
			"address": "0x1234567890123456789012345678901234567890"
		}
	]`

	os.WriteFile(filepath.Join("token_whitelist", "ethereum.json"), []byte(testTokens), 0644)

	service, _ := NewTokenEnrichmentService(nil, logger)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = service.GetTokenMetadata("0x1234567890123456789012345678901234567890")
	}
}
