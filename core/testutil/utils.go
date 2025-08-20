package testutil

import (
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

var testConfig *config.Config

// init loads test configuration from config/aggregator.yaml
func init() {
	// Load test configuration from aggregator.yaml
	if _, thisFile, _, ok := runtime.Caller(0); ok {
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))
		configPath := filepath.Join(repoRoot, "config", "aggregator.yaml")

		var err error
		testConfig, err = config.NewConfig(configPath)
		if err != nil {
			// Fallback: try current working directory
			testConfig, err = config.NewConfig("config/aggregator.yaml")
			if err != nil {
				log.Printf("Warning: Could not load test config from aggregator.yaml: %v", err)
				testConfig = nil
			}
		}
	}
}

const (
	paymasterAddress = config.DefaultPaymasterAddressHex
)

// GetTestConfig returns the loaded test configuration from aggregator.yaml
func GetTestConfig() *config.Config {
	return testConfig
}

// GetTestRPC returns the RPC URL for tests from aggregator config
func GetTestRPC() string {
	if testConfig != nil && testConfig.EthHttpRpcUrl != "" {
		return testConfig.EthHttpRpcUrl
	}
	return "https://sepolia.drpc.org"
}

// GetTestWsRPC returns the WebSocket RPC URL for tests from aggregator config
func GetTestWsRPC() string {
	if testConfig != nil && testConfig.EthWsRpcUrl != "" {
		return testConfig.EthWsRpcUrl
	}
	// Derive from HTTP RPC
	if http := GetTestRPC(); strings.HasPrefix(http, "https://") {
		return strings.Replace(http, "https://", "wss://", 1)
	}
	return "wss://sepolia.drpc.org"
}

// GetTestBundlerRPC returns the bundler RPC URL for tests from aggregator config
func GetTestBundlerRPC() string {
	if testConfig != nil && testConfig.SmartWallet != nil && testConfig.SmartWallet.BundlerURL != "" {
		return testConfig.SmartWallet.BundlerURL
	}
	return ""
}

// GetTestTenderlyAccount returns the Tenderly account for tests from aggregator config
func GetTestTenderlyAccount() string {
	if testConfig != nil {
		return testConfig.TenderlyAccount
	}
	return ""
}

// GetTestTenderlyProject returns the Tenderly project for tests from aggregator config
func GetTestTenderlyProject() string {
	if testConfig != nil {
		return testConfig.TenderlyProject
	}
	return ""
}

// GetTestTenderlyAccessKey returns the Tenderly access key for tests from aggregator config
func GetTestTenderlyAccessKey() string {
	if testConfig != nil {
		return testConfig.TenderlyAccessKey
	}
	return ""
}

// GetTestPrivateKey returns the test private key from aggregator config
func GetTestPrivateKey() string {
	if testConfig != nil && testConfig.TestPrivateKey != "" {
		return testConfig.TestPrivateKey
	}
	return ""
}

// GetTestControllerPrivateKey returns the controller private key for tests from aggregator config
func GetTestControllerPrivateKey() string {
	if testConfig != nil && testConfig.SmartWallet != nil && testConfig.SmartWallet.ControllerPrivateKey != nil {
		return fmt.Sprintf("%x", testConfig.SmartWallet.ControllerPrivateKey.D)
	}
	// No fallback - controller key and test key serve different purposes
	return ""
}

// GetTestFactoryAddress returns the factory address for tests from aggregator config
func GetTestFactoryAddress() string {
	if testConfig != nil && testConfig.SmartWallet != nil {
		return testConfig.SmartWallet.FactoryAddress.Hex()
	}
	return config.DefaultFactoryProxyAddressHex
}

// GetTestEntrypointAddress returns the entrypoint address for tests from aggregator config
func GetTestEntrypointAddress() string {
	if testConfig != nil && testConfig.SmartWallet != nil {
		return testConfig.SmartWallet.EntrypointAddress.Hex()
	}
	return config.DefaultEntrypointAddressHex
}

// GetTestPaymasterAddress returns the paymaster address for tests from aggregator config
func GetTestPaymasterAddress() string {
	if testConfig != nil && testConfig.SmartWallet != nil {
		return testConfig.SmartWallet.PaymasterAddress.Hex()
	}
	return config.DefaultPaymasterAddressHex
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
					Lang:   avsproto.Lang_JavaScript,
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
