package testutil

import (
	"context"
	"fmt"
	"log"
	"os"
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
)

const (
	paymasterAddress = "0xB985af5f96EF2722DC99aEBA573520903B86505e"
)

func GetTestRPCURL() string {
	v := os.Getenv("RPC_URL")
	if v == "" {
		return "https://sepolia.drpc.org"
	}

	return v
}

func GetTestWsRPCURL() string {
	v := os.Getenv("WS_RPC_URL")
	if v == "" {
		return "wss://sepolia.drpc.org"
	}

	return v
}

func GetRpcClient() *ethclient.Client {
	client, err := ethclient.Dial(GetTestRPCURL())
	if err != nil {
		log.Fatalf("Failed to connect to Ethereum client: %v", err)
	}

	return client
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
	logger, err := sdklogging.NewZapLogger("development")
	if err != nil {
		panic(err)
	}
	return logger
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
			EthRpcUrl: GetTestRPCURL(),
			EthWsUrl:  GetTestWsRPCURL(),
			//	FactoryAddress:    common.HexToAddress(os.Getenv("FACTORY_ADDRESS")),
			FactoryAddress:    common.HexToAddress("0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7"),
			EntrypointAddress: common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"),
			PaymasterAddress:  common.HexToAddress(paymasterAddress),
		},
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
			&avsproto.RestAPINode{
				Url: "https://httpbin.org",
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
				Block: &avsproto.BlockCondition{
					Interval: 5,
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
				Source: "return 100",
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
				Block: &avsproto.BlockCondition{
					Interval: 5,
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
	controllerPrivateKey, err := crypto.HexToECDSA(os.Getenv("CONTROLLER_PRIVATE_KEY"))
	if err != nil {
		panic("Invalid controller private key from env. Ensure CONTROLLER_PRIVATE_KEY is ECDSA key of the controller wallet")
	}

	return &config.SmartWalletConfig{
		EthRpcUrl:  os.Getenv("RPC_URL"),
		BundlerURL: os.Getenv("BUNDLER_RPC"),
		EthWsUrl:   strings.Replace(os.Getenv("RPC_URL"), "https://", "wss://", 1),
		//FactoryAddress:       common.HexToAddress(os.Getenv("FACTORY_ADDRESS")),
		FactoryAddress:       common.HexToAddress("0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7"),
		EntrypointAddress:    common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"),
		ControllerPrivateKey: controllerPrivateKey,
		PaymasterAddress:     common.HexToAddress(paymasterAddress),
	}
}

// Get smart wallet config for base
// Using base sepolia to run test because it's very cheap and fast
func GetBaseTestSmartWalletConfig() *config.SmartWalletConfig {
	controllerPrivateKey, err := crypto.HexToECDSA(os.Getenv("CONTROLLER_PRIVATE_KEY"))
	if err != nil {
		panic("Invalid controller private key from env. Ensure CONTROLLER_PRIVATE_KEY is ECDSA key of the controller wallet")
	}

	return &config.SmartWalletConfig{
		EthRpcUrl:            os.Getenv("BASE_SEPOLIA_RPC_URL"),
		BundlerURL:           os.Getenv("BASE_SEPOLIA_BUNDLER_RPC"),
		EthWsUrl:             strings.Replace(os.Getenv("BASE_SEPOLIA_RPC_URL"), "https://", "wss://", 1),
		FactoryAddress:       common.HexToAddress(os.Getenv("FACTORY_ADDRESS")),
		EntrypointAddress:    common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"),
		ControllerPrivateKey: controllerPrivateKey,
		PaymasterAddress:     common.HexToAddress(paymasterAddress),
	}
}

func GetTestSecrets() map[string]string {
	return map[string]string{
		"my_awesome_secret": "my_awesome_secret_value",
	}
}

func GetTestEventTriggerReason() *avsproto.TriggerReason {
	return &avsproto.TriggerReason{
		BlockNumber: 7212417,
		TxHash:      "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		LogIndex:    98,
	}
}
