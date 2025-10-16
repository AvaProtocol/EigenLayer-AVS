package testutil

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/allegro/bigcache/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	// DefaultConfigPath is the default aggregator config path for tests (replaces old aggregator.yaml)
	DefaultConfigPath = "aggregator-sepolia.yaml"
)

var testConfig *config.Config

// LoadDotEnv loads environment variables from .env file in the repository root.
// This allows tests to access TEST_PRIVATE_KEY and other secrets from .env
func LoadDotEnv() error {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("failed to get caller information")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))
	envPath := filepath.Join(repoRoot, ".env")

	// Check if .env file exists
	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		// .env file doesn't exist, skip silently (tests may use env vars from other sources)
		return nil
	}

	// Read .env file
	file, err := os.Open(envPath)
	if err != nil {
		return fmt.Errorf("failed to open .env file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=VALUE
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Remove quotes if present
		value = strings.Trim(value, `"'`)

		// Only set if not already set in environment (env vars take precedence)
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading .env file: %w", err)
	}

	return nil
}

// init loads environment variables from .env file and test configuration.
// When commands use a non-default path via --config flag, testConfig will be nil
// and the test utility functions will panic if testConfig is not loaded.
func init() {
	// Load .env file first (if it exists)
	_ = LoadDotEnv()

	if _, thisFile, _, ok := runtime.Caller(0); ok {
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))
		configPath := filepath.Join(repoRoot, "config", DefaultConfigPath)

		var err error
		testConfig, err = config.NewConfig(configPath)
		if err != nil {
			// Config loading failed - test utilities will use fallback defaults
			testConfig = nil
		}
	}
}

// GetConfigPath returns the absolute path to a config file from the repo root.
// This is useful for tests that need to load config files explicitly.
// Example: GetConfigPath(testutil.DefaultConfigPath) or GetConfigPath("aggregator-base.yaml")
func GetConfigPath(configFileName string) string {
	if _, thisFile, _, ok := runtime.Caller(0); ok {
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))
		return filepath.Join(repoRoot, "config", configFileName)
	}
	// Fallback to relative path if caller info not available
	return filepath.Join("../../config", configFileName)
}

// GetTestConfig returns the loaded test configuration
func GetTestConfig() *config.Config {
	return testConfig
}

// GetTestRPC returns the RPC URL for tests from aggregator config
// Panics if config is not loaded or EthHttpRpcUrl is empty
func GetTestRPC() string {
	if testConfig == nil {
		panic("testConfig is nil - aggregator-sepolia.yaml config must be loaded")
	}
	if testConfig.EthHttpRpcUrl == "" {
		panic("EthHttpRpcUrl is empty in aggregator-sepolia.yaml config")
	}
	return testConfig.EthHttpRpcUrl
}

// GetTestWsRPC returns the WebSocket RPC URL for tests from aggregator config
// Panics if config is not loaded or EthWsRpcUrl is empty
func GetTestWsRPC() string {
	if testConfig == nil {
		panic("testConfig is nil - aggregator-sepolia.yaml config must be loaded")
	}
	if testConfig.EthWsRpcUrl != "" {
		return testConfig.EthWsRpcUrl
	}
	// Try to derive from HTTP RPC if available
	if http := GetTestRPC(); strings.HasPrefix(http, "https://") {
		return strings.Replace(http, "https://", "wss://", 1)
	}
	panic("EthWsRpcUrl is empty in aggregator-sepolia.yaml config and cannot derive from EthHttpRpcUrl")
}

// GetTestBundlerRPC returns the bundler RPC URL for tests from aggregator config
// Panics if config is not loaded or BundlerURL is empty
func GetTestBundlerRPC() string {
	if testConfig == nil {
		panic("testConfig is nil - aggregator-sepolia.yaml config must be loaded")
	}
	if testConfig.SmartWallet == nil {
		panic("SmartWallet config is nil in aggregator-sepolia.yaml")
	}
	if testConfig.SmartWallet.BundlerURL == "" {
		panic("BundlerURL is empty in aggregator-sepolia.yaml config")
	}
	return testConfig.SmartWallet.BundlerURL
}

// GetTestTenderlyAccount returns the Tenderly account for tests from aggregator config
// Panics if config is not loaded or TenderlyAccount is empty
func GetTestTenderlyAccount() string {
	if testConfig == nil {
		panic("testConfig is nil - aggregator-sepolia.yaml config must be loaded")
	}
	if testConfig.TenderlyAccount == "" {
		panic("TenderlyAccount is empty in aggregator-sepolia.yaml config")
	}
	return testConfig.TenderlyAccount
}

// GetTestTenderlyProject returns the Tenderly project for tests from aggregator config
// Panics if config is not loaded or TenderlyProject is empty
func GetTestTenderlyProject() string {
	if testConfig == nil {
		panic("testConfig is nil - aggregator-sepolia.yaml config must be loaded")
	}
	if testConfig.TenderlyProject == "" {
		panic("TenderlyProject is empty in aggregator-sepolia.yaml config")
	}
	return testConfig.TenderlyProject
}

// GetTestTenderlyAccessKey returns the Tenderly access key for tests from aggregator config
// Panics if config is not loaded or TenderlyAccessKey is empty
func GetTestTenderlyAccessKey() string {
	if testConfig == nil {
		panic("testConfig is nil - aggregator-sepolia.yaml config must be loaded")
	}
	if testConfig.TenderlyAccessKey == "" {
		panic("TenderlyAccessKey is empty in aggregator-sepolia.yaml config")
	}
	return testConfig.TenderlyAccessKey
}

// GetTestPrivateKeyFromEnv returns TEST_PRIVATE_KEY from environment (auto-loaded from .env).
// Returns empty string if not set. Tests should check the return value and skip if empty.
func GetTestPrivateKeyFromEnv() string {
	key := os.Getenv("TEST_PRIVATE_KEY")
	// Remove 0x prefix if present
	if len(key) > 2 && key[:2] == "0x" {
		key = key[2:]
	}
	return key
}

// MustGetTestOwnerAddress parses TEST_PRIVATE_KEY and returns the owner EOA address.
// Returns nil and false if TEST_PRIVATE_KEY is not set (test should skip).
// Returns address and true if successful.
// Panics if TEST_PRIVATE_KEY is set but invalid.
func MustGetTestOwnerAddress() (*common.Address, bool) {
	privateKeyHex := GetTestPrivateKeyFromEnv()
	if privateKeyHex == "" {
		return nil, false
	}

	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		panic(fmt.Sprintf("Failed to parse TEST_PRIVATE_KEY: %v", err))
	}

	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	return &address, true
}

// GetTestControllerPrivateKey returns the controller private key for tests from aggregator config
// Panics if config is not loaded or SmartWallet is nil or ControllerPrivateKey is nil
func GetTestControllerPrivateKey() string {
	if testConfig == nil {
		panic("testConfig is nil - aggregator-sepolia.yaml config must be loaded")
	}
	if testConfig.SmartWallet == nil {
		panic("SmartWallet config is nil in aggregator-sepolia.yaml")
	}
	if testConfig.SmartWallet.ControllerPrivateKey == nil {
		panic("ControllerPrivateKey is nil in aggregator-sepolia.yaml config")
	}
	return fmt.Sprintf("%x", testConfig.SmartWallet.ControllerPrivateKey.D)
}

// GetTestFactoryAddress returns the factory address for tests from aggregator config
// Panics if config is not loaded or SmartWallet is nil
func GetTestFactoryAddress() string {
	if testConfig == nil {
		panic("testConfig is nil - aggregator-sepolia.yaml config must be loaded")
	}
	if testConfig.SmartWallet == nil {
		panic("SmartWallet config is nil in aggregator-sepolia.yaml")
	}
	return testConfig.SmartWallet.FactoryAddress.Hex()
}

// GetTestEntrypointAddress returns the entrypoint address for tests from aggregator config
// Panics if config is not loaded or SmartWallet is nil
func GetTestEntrypointAddress() string {
	if testConfig == nil {
		panic("testConfig is nil - aggregator-sepolia.yaml config must be loaded")
	}
	if testConfig.SmartWallet == nil {
		panic("SmartWallet config is nil in aggregator-sepolia.yaml")
	}
	return testConfig.SmartWallet.EntrypointAddress.Hex()
}

// GetTestPaymasterAddress returns the paymaster address for tests from aggregator config
// Panics if config is not loaded or SmartWallet is nil
func GetTestPaymasterAddress() string {
	if testConfig == nil {
		panic("testConfig is nil - aggregator-sepolia.yaml config must be loaded")
	}
	if testConfig.SmartWallet == nil {
		panic("SmartWallet config is nil in aggregator-sepolia.yaml")
	}
	return testConfig.SmartWallet.PaymasterAddress.Hex()
}

// GetTestMoralisApiKey returns the Moralis API key for tests from aggregator config
// Returns empty string if config is not loaded or key is not configured
func GetTestMoralisApiKey() string {
	if testConfig == nil || testConfig.MacroSecrets == nil {
		return ""
	}
	return testConfig.MacroSecrets["moralis_api_key"]
}

// TriggerData represents the flattened trigger information for testing
type TriggerData struct {
	Type   avsproto.TriggerType
	Output interface{} // Will hold the specific trigger output (BlockTrigger.Output, etc.)
}

func GetTestRPCURL() string {
	return GetTestRPC()
}

func GetTestWsRPCURL() string {
	return GetTestWsRPC()
}

// firstNonEmpty returns the first non-empty string in the provided list.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func GetRpcClient() *ethclient.Client {
	client, err := ethclient.Dial(GetTestRPCURL())
	if err != nil {
		log.Fatalf("Failed to connect to Ethereum client: %v", err)
	}

	return client
}

func MustGetEventForTx(txHash string, evtIndex uint64) *types.Log {
	event, err := GetEventForTx(txHash, evtIndex)
	if err != nil {
		panic(err)
	}
	return event
}

func GetEventForTx(txHash string, evtIndex uint64) (*types.Log, error) {
	client := GetRpcClient()

	receipt, err := client.TransactionReceipt(context.Background(), common.HexToHash(txHash))
	if err != nil {
		return nil, err
	}

	var event *types.Log
	for _, l := range receipt.Logs {
		if uint64(l.Index) == evtIndex {
			event = l
		}
	}

	if event == nil {
		return nil, fmt.Errorf("not found event")
	}
	return event, nil
}

// Shortcut to initialize a storage at the given path, panic if we cannot create db
func TestMustDB() storage.Storage {
	dir, err := os.MkdirTemp("", "aptest")
	if err != nil {
		panic(err)
	}

	// dir = "/tmp/ap-avs/test"
	db, err := storage.NewWithPath(dir)
	if err != nil {
		panic(err)
	}
	return db
}

func GetLogger() sdklogging.Logger {
	return &MockLogger{}
}

// MockLogger implements the sdklogging.Logger interface for testing
type MockLogger struct{}

func (l *MockLogger) Info(msg string, keysAndValues ...interface{})  {}
func (l *MockLogger) Infof(format string, args ...interface{})       {}
func (l *MockLogger) Debug(msg string, keysAndValues ...interface{}) {}
func (l *MockLogger) Debugf(format string, args ...interface{})      {}
func (l *MockLogger) Error(msg string, keysAndValues ...interface{}) {}
func (l *MockLogger) Errorf(format string, args ...interface{})      {}
func (l *MockLogger) Warn(msg string, keysAndValues ...interface{})  {}
func (l *MockLogger) Warnf(format string, args ...interface{})       {}
func (l *MockLogger) Fatal(msg string, keysAndValues ...interface{}) {
	panic(fmt.Sprintf(msg, keysAndValues...))
}
func (l *MockLogger) Fatalf(format string, args ...interface{}) {
	panic(fmt.Sprintf(format, args...))
}

func (l *MockLogger) With(keysAndValues ...interface{}) sdklogging.Logger {
	return l
}

func (l *MockLogger) WithComponent(componentName string) sdklogging.Logger {
	return l
}

func (l *MockLogger) WithName(name string) sdklogging.Logger {
	return l
}

func (l *MockLogger) WithServiceName(serviceName string) sdklogging.Logger {
	return l
}

func (l *MockLogger) WithHostName(hostName string) sdklogging.Logger {
	return l
}

func (l *MockLogger) Sync() error {
	return nil
}

func TestUser1() *model.User {
	address := common.HexToAddress("0xD7050816337a3f8f690F8083B5Ff8019D50c0E50")
	smartWalletAddress := common.HexToAddress("0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6")

	return &model.User{
		Address: address,
		// Factory https://sepolia.etherscan.io/address/0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7#readContract salt 0
		SmartAccountAddress: &smartWalletAddress,
	}
}

func TestUser2() *model.User {
	address := common.HexToAddress("0xd8da6bf26964af9d7eed9e03e53415d37aa96045")
	smartWalletAddress := common.HexToAddress("0xBdCcA49575918De45bb32f5ba75388e7c3fBB5e4")

	return &model.User{
		Address: address,
		// Factory https://sepolia.etherscan.io/address/0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7#readContract salt 0
		SmartAccountAddress: &smartWalletAddress,
	}
}

func GetAggregatorConfig() *config.Config {
	return &config.Config{
		SmartWallet: &config.SmartWalletConfig{
			EthRpcUrl:          GetTestRPCURL(),
			EthWsUrl:           GetTestWsRPCURL(),
			FactoryAddress:     common.HexToAddress(GetTestFactoryAddress()),
			EntrypointAddress:  common.HexToAddress(GetTestEntrypointAddress()),
			PaymasterAddress:   common.HexToAddress(GetTestPaymasterAddress()),
			WhitelistAddresses: []common.Address{},
		},
		// Include Tenderly credentials for tests
		TenderlyAccount:   GetTestTenderlyAccount(),
		TenderlyProject:   GetTestTenderlyProject(),
		TenderlyAccessKey: GetTestTenderlyAccessKey(),
	}
}

func GetDefaultCache() *bigcache.BigCache {
	config := bigcache.Config{

		// number of shards (must be a power of 2)
		Shards: 1024,

		// time after which entry can be evicted
		LifeWindow: 10 * time.Minute,

		// Interval between removing expired entries (clean up).
		// If set to <= 0 then no action is performed.
		// Setting to < 1 second is counterproductive — bigcache has a one second resolution.
		CleanWindow: 5 * time.Minute,

		// rps * lifeWindow, used only in initial memory allocation
		MaxEntriesInWindow: 1000 * 10 * 60,

		// max entry size in bytes, used only in initial memory allocation
		MaxEntrySize: 500,

		// prints information about additional memory allocation
		Verbose: true,

		// cache will not allocate more memory than this limit, value in MB
		// if value is reached then the oldest entries can be overridden for the new ones
		// 0 value means no size limit
		HardMaxCacheSize: 8192,

		// callback fired when the oldest entry is removed because of its expiration time or no space left
		// for the new entry, or because delete was called. A bitmask representing the reason will be returned.
		// Default value is nil which means no callback and it prevents from unwrapping the oldest entry.
		OnRemove: nil,

		// OnRemoveWithReason is a callback fired when the oldest entry is removed because of its expiration time or no space left
		// for the new entry, or because delete was called. A constant representing the reason will be passed through.
		// Default value is nil which means no callback and it prevents from unwrapping the oldest entry.
		// Ignored if OnRemove is specified.
		OnRemoveWithReason: nil,
	}
	cache, err := bigcache.New(context.Background(), config)
	if err != nil {
		panic(fmt.Errorf("error get default cache for test"))
	}
	return cache
}

func RestTask() *avsproto.CreateTaskReq {
	node := &avsproto.TaskNode{
		Id:   "ping1",
		Name: "ping",
		TaskType: &avsproto.TaskNode_RestApi{
			RestApi: &avsproto.RestAPINode{
				Config: &avsproto.RestAPINode_Config{
					Url:    "https://mock-api.ap-aggregator.local/post", // Use MockAPIEndpoint instead of httpbin.org
					Method: "POST",
					Body:   "test=data",
				},
			},
		},
	}
	edge := &avsproto.TaskEdge{
		Id:     "edge1",
		Source: "triggerabcde",
		Target: "ping1",
	}
	tr1 := avsproto.CreateTaskReq{
		Trigger: &avsproto.TaskTrigger{
			Id:   "triggerabcde",
			Name: "block",
			TriggerType: &avsproto.TaskTrigger_Block{
				Block: &avsproto.BlockTrigger{
					Config: &avsproto.BlockTrigger_Config{
						Interval: 10,
					},
				},
			},
		},
		MaxExecution: 1000,
		Nodes:        []*avsproto.TaskNode{node},
		Edges:        []*avsproto.TaskEdge{edge},
	}
	return &tr1
}

func JsFastTask() *avsproto.CreateTaskReq {
	node := &avsproto.TaskNode{
		Id:   "jsfast1",
		Name: "jsfast",
		TaskType: &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Config: &avsproto.CustomCodeNode_Config{
					Lang:   avsproto.Lang_LANG_JAVASCRIPT,
					Source: "({ message: 'Hello from test' })",
				},
			},
		},
	}
	edge := &avsproto.TaskEdge{
		Id:     "edge1",
		Source: "triggerabcde",
		Target: "jsfast1",
	}
	tr1 := avsproto.CreateTaskReq{
		Trigger: &avsproto.TaskTrigger{
			Id:   "triggerabcde",
			Name: "block",
			TriggerType: &avsproto.TaskTrigger_Block{
				Block: &avsproto.BlockTrigger{
					Config: &avsproto.BlockTrigger_Config{
						Interval: 10,
					},
				},
			},
		},
		MaxExecution: 1000,
		Nodes:        []*avsproto.TaskNode{node},
		Edges:        []*avsproto.TaskEdge{edge},
	}
	return &tr1
}

func GetTestSmartWalletConfig() *config.SmartWalletConfig {
	controllerKeyHex := GetTestControllerPrivateKey()
	// In CI or local without key, return a minimal config that avoids panicking; tests that need
	// real ERC-4337 will be gated and skipped.
	if controllerKeyHex == "" {
		return &config.SmartWalletConfig{
			EthRpcUrl:          GetTestRPC(),
			BundlerURL:         GetTestBundlerRPC(),
			EthWsUrl:           GetTestWsRPC(),
			FactoryAddress:     common.HexToAddress(GetTestFactoryAddress()),
			EntrypointAddress:  common.HexToAddress(GetTestEntrypointAddress()),
			PaymasterAddress:   common.HexToAddress(GetTestPaymasterAddress()),
			WhitelistAddresses: []common.Address{},
		}
	}
	controllerPrivateKey, err := crypto.HexToECDSA(controllerKeyHex)
	if err != nil {
		// Fallback to non-panicking minimal config
		return &config.SmartWalletConfig{
			EthRpcUrl:          GetTestRPC(),
			BundlerURL:         GetTestBundlerRPC(),
			EthWsUrl:           GetTestWsRPC(),
			FactoryAddress:     common.HexToAddress(GetTestFactoryAddress()),
			EntrypointAddress:  common.HexToAddress(GetTestEntrypointAddress()),
			PaymasterAddress:   common.HexToAddress(GetTestPaymasterAddress()),
			WhitelistAddresses: []common.Address{},
		}
	}

	return &config.SmartWalletConfig{
		EthRpcUrl:            GetTestRPC(),
		BundlerURL:           GetTestBundlerRPC(),
		EthWsUrl:             GetTestWsRPC(),
		FactoryAddress:       common.HexToAddress(GetTestFactoryAddress()),
		EntrypointAddress:    common.HexToAddress(GetTestEntrypointAddress()),
		ControllerPrivateKey: controllerPrivateKey,
		PaymasterAddress:     common.HexToAddress(GetTestPaymasterAddress()),
		WhitelistAddresses:   []common.Address{},
	}
}

// Get smart wallet config for base
// Using base sepolia to run test because it's very cheap and fast
// Note: Currently uses same config as Sepolia since Base Sepolia config is not in aggregator.yaml
func GetBaseTestSmartWalletConfig() *config.SmartWalletConfig {
	controllerKeyHex := GetTestControllerPrivateKey()
	if controllerKeyHex == "" {
		return &config.SmartWalletConfig{
			EthRpcUrl:          GetTestRPC(),
			BundlerURL:         GetTestBundlerRPC(),
			EthWsUrl:           GetTestWsRPC(),
			FactoryAddress:     common.HexToAddress(GetTestFactoryAddress()),
			EntrypointAddress:  common.HexToAddress(GetTestEntrypointAddress()),
			PaymasterAddress:   common.HexToAddress(GetTestPaymasterAddress()),
			WhitelistAddresses: []common.Address{},
		}
	}
	controllerPrivateKey, err := crypto.HexToECDSA(controllerKeyHex)
	if err != nil {
		return &config.SmartWalletConfig{
			EthRpcUrl:          GetTestRPC(),
			BundlerURL:         GetTestBundlerRPC(),
			EthWsUrl:           GetTestWsRPC(),
			FactoryAddress:     common.HexToAddress(GetTestFactoryAddress()),
			EntrypointAddress:  common.HexToAddress(GetTestEntrypointAddress()),
			PaymasterAddress:   common.HexToAddress(GetTestPaymasterAddress()),
			WhitelistAddresses: []common.Address{},
		}
	}

	return &config.SmartWalletConfig{
		EthRpcUrl:            GetTestRPC(),
		BundlerURL:           GetTestBundlerRPC(),
		EthWsUrl:             GetTestWsRPC(),
		FactoryAddress:       common.HexToAddress(GetTestFactoryAddress()),
		EntrypointAddress:    common.HexToAddress(GetTestEntrypointAddress()),
		ControllerPrivateKey: controllerPrivateKey,
		PaymasterAddress:     common.HexToAddress(GetTestPaymasterAddress()),
		WhitelistAddresses:   []common.Address{},
	}
}

func GetTestSecrets() map[string]string {
	return map[string]string{
		"my_awesome_secret": "my_awesome_secret_value",
	}
}

func GetTestEventTriggerData() *TriggerData {
	// Sample JSON event data that would come from parsed event
	eventData := map[string]interface{}{
		"blockNumber":     7212417,
		"transactionHash": "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		"logIndex":        98,
		"address":         "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"removed":         false,
	}

	// Convert to google.protobuf.Value
	protoValue, err := structpb.NewValue(eventData)
	if err != nil {
		panic(fmt.Sprintf("Failed to create protobuf value: %v", err))
	}

	return &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			Data: protoValue,
		},
	}
}

// GetTestEventTriggerDataWithTransferData provides trigger data with rich transfer log data for testing
func GetTestEventTriggerDataWithTransferData() *TriggerData {
	// Sample JSON event data for transfer events (parsed from Transfer event)
	transferEventData := map[string]interface{}{
		"tokenName":        "USDC",
		"tokenSymbol":      "USDC",
		"tokenDecimals":    6,
		"from":             "0x2A6CEbeDF9e737A9C6188c62A68655919c7314DB",
		"to":               "0xC114FB059434563DC65AC8D57e7976e3eaC534F4",
		"value":            "3453120",
		"valueFormatted":   "3.45312",
		"transactionHash":  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		"address":          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"blockNumber":      7212417,
		"blockTimestamp":   int64(1733351604000),
		"transactionIndex": 73,
		"logIndex":         0,
	}

	// Convert to google.protobuf.Value
	protoValue, err := structpb.NewValue(transferEventData)
	if err != nil {
		panic(fmt.Sprintf("Failed to create protobuf value: %v", err))
	}

	triggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			Data: protoValue,
		},
	}

	return triggerData
}
