package taskengine

import (
	"os"
	"path/filepath"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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
			value:    "de0b6b3a7640000", // 1 ETH in wei
			decimals: 18,
			expected: "1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := service.FormatTokenValue(tt.value, tt.decimals)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEnrichTransferLog(t *testing.T) {
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

	// Create test ethereum.json with USDC
	testTokens := `[
		{
			"name": "USD Coin",
			"symbol": "USDC",
			"decimals": 6,
			"address": "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
		}
	]`

	err = os.WriteFile(filepath.Join("token_whitelist", "ethereum.json"), []byte(testTokens), 0644)
	require.NoError(t, err)

	service, err := NewTokenEnrichmentService(nil, logger)
	require.NoError(t, err)

	// Create test EVM log and transfer log
	evmLog := &avsproto.Evm_Log{
		Address: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
	}

	transferLog := &avsproto.EventTrigger_TransferLogOutput{
		Address:     "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		Value:       "0xf4240", // 1 USDC (1,000,000 micro USDC)
		FromAddress: "0x1234567890123456789012345678901234567890",
		ToAddress:   "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
	}

	// Test enrichment
	err = service.EnrichTransferLog(evmLog, transferLog)
	require.NoError(t, err)

	// Verify enrichment
	assert.Equal(t, "USD Coin", transferLog.TokenName)
	assert.Equal(t, "USDC", transferLog.TokenSymbol)
	assert.Equal(t, uint32(6), transferLog.TokenDecimals)
	assert.Equal(t, "1", transferLog.ValueFormatted)
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

func TestGetTokenMetadataNotFound(t *testing.T) {
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

	// Create empty ethereum.json
	err = os.WriteFile(filepath.Join("token_whitelist", "ethereum.json"), []byte("[]"), 0644)
	require.NoError(t, err)

	service, err := NewTokenEnrichmentService(nil, logger)
	require.NoError(t, err)

	// Test with token not in whitelist and no RPC client
	_, err = service.GetTokenMetadata("0x1234567890123456789012345678901234567890")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no RPC client available")
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
